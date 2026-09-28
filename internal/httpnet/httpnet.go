// Package httpnet is the one HTTP transport every ticfac client dials
// through: the factory, the forge (GitHub API), the gateway and Jev, the run
// signal and the Cloudflare APIs.
//
// It exists because Go's own dual-stack fallback depends on a single
// AF_UNSPEC lookup returning both address families, and on macOS it does not
// always. getaddrinfo there gives up on the slower family after about two
// seconds and returns whatever arrived first — measured on the operator's Mac,
// 20 lookups of fresh names under one workers.dev subdomain returned A-only
// three times and AAAA-only once, while per-family lookups of the same names
// always answered. A lookup that comes back AAAA-only
// leaves the default dialer nothing to fall back to, so on a host whose IPv6
// is broken (AAAA resolves, connect fails at once) the request dies with
//
//	dial tcp [2606:4700:…]:443: connect: no route to host
//
// while curl and git, which retry the other family, work. That is what
// stopped `ticfac factory setup` and `ticfac factory deploy`'s verification.
//
// So this dialer does not ask for "both families" in one question. It asks
// for each family separately and concurrently (a per-family lookup waits for
// its own answer), dials IPv6 first as the default dialer would, and starts
// IPv4 as soon as IPv6 has failed, has no addresses, or has had its
// head start — Happy Eyeballs (RFC 8305) over two lookups instead of one.
package httpnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// Resolver is the lookup seam: net.DefaultResolver in production, a fake in
// tests. LookupNetIP is asked with network "ip4" or "ip6", never "ip".
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Dialer dials TCP with a per-family lookup and an IPv4 fallback. The zero
// value is not usable; see NewDialer.
type Dialer struct {
	// Resolver answers the per-family lookups.
	Resolver Resolver
	// Dial connects to one literal address. It is net.Dialer's DialContext
	// in production and the seam a test uses to make an address unreachable.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// FallbackDelay is IPv6's head start: how long IPv4 waits for an IPv6
	// attempt that has neither connected nor failed. 300ms, as in net.Dialer.
	FallbackDelay time.Duration
	// ResolutionDelay is how long IPv4 addresses that arrive first wait for
	// the IPv6 lookup to answer before being dialed anyway (RFC 8305 §3).
	ResolutionDelay time.Duration
}

// NewDialer is the production dialer: the system resolver and net.Dialer's
// timeouts, which are the ones http.DefaultTransport uses.
func NewDialer() *Dialer {
	nd := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return &Dialer{
		Resolver:        net.DefaultResolver,
		Dial:            nd.DialContext,
		FallbackDelay:   300 * time.Millisecond,
		ResolutionDelay: 50 * time.Millisecond,
	}
}

var sharedTransport = NewTransport(NewDialer())

// Transport is the shared transport: one connection pool for the process.
func Transport() *http.Transport { return sharedTransport }

// Client is an http.Client over the shared transport with the given timeout —
// what every `&http.Client{Timeout: …}` in ticfac used to be.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: sharedTransport}
}

// NewTransport is http.DefaultTransport's configuration (proxy from the
// environment, HTTP/2, idle pool) dialing through d.
func NewTransport(d *Dialer) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = d.DialContext
	return t
}

// DialContext dials address over network. Only "tcp" to a host name gets the
// per-family treatment; an IP literal or a family-pinned network ("tcp4",
// "tcp6") has nothing to fall back between and is dialed as asked.
func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || network != "tcp" {
		return d.Dial(ctx, network, address)
	}
	if _, perr := netip.ParseAddr(strings.Trim(host, "[]")); perr == nil {
		return d.Dial(ctx, network, address)
	}
	return d.dialHost(ctx, host, port)
}

type family int

const (
	v6 family = iota
	v4
)

func (f family) String() string {
	if f == v6 {
		return "IPv6"
	}
	return "IPv4"
}

type lookupResult struct {
	fam   family
	addrs []netip.Addr
	err   error
}

type dialResult struct {
	fam  family
	conn net.Conn
	err  error
}

// familyState is one family's progress through lookup and dial.
type familyState struct {
	looked  bool // the lookup answered
	addrs   []netip.Addr
	started bool // its dial racer is running or has run
	done    bool // nothing more will come from this family
	// lookupErr is why the lookup found nothing; dialErr why its addresses
	// did not connect. A family with no addresses is not a failure unless
	// neither family had any.
	lookupErr, dialErr error
}

func (d *Dialer) dialHost(ctx context.Context, host, port string) (net.Conn, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lookups := make(chan lookupResult, 2)
	for _, f := range []family{v6, v4} {
		go func(f family) {
			network := "ip6"
			if f == v4 {
				network = "ip4"
			}
			addrs, err := d.Resolver.LookupNetIP(ctx, network, host)
			lookups <- lookupResult{fam: f, addrs: addrs, err: err}
		}(f)
	}

	// Room for both racers, so neither blocks once this returns; a
	// connection that loses the race is closed below rather than leaked.
	results := make(chan dialResult, 2)
	racers := 0
	defer func() {
		// Drain racers that are still running; close any late winner.
		go func(n int) {
			for i := 0; i < n; i++ {
				if r := <-results; r.conn != nil {
					r.conn.Close()
				}
			}
		}(racers)
	}()

	var fams [2]familyState
	v4Allowed := false // IPv6 failed, had nothing, or used up its head start
	var timer *time.Timer
	var timerC <-chan time.Time
	armTimer := func(delay time.Duration) {
		if timer != nil {
			timer.Stop()
		}
		timer = time.NewTimer(delay)
		timerC = timer.C
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	start := func(f family) {
		fams[f].started = true
		racers++
		addrs := fams[f].addrs
		go func() {
			c, err := d.dialSerial(ctx, addrs, port)
			results <- dialResult{fam: f, conn: c, err: err}
		}()
	}
	// advance starts whatever the state now allows.
	advance := func() {
		if s := &fams[v6]; s.looked && !s.started && !s.done {
			start(v6)
			if !v4Allowed {
				armTimer(d.FallbackDelay)
			}
		}
		if s := &fams[v6]; s.done {
			v4Allowed = true
		}
		if s := &fams[v4]; s.looked && !s.started && !s.done {
			if v4Allowed {
				start(v4)
			} else if !fams[v6].looked && timerC == nil {
				armTimer(d.ResolutionDelay)
			}
		}
	}

	for {
		if fams[v6].done && fams[v4].done {
			return nil, d.failure(host, port, fams)
		}
		select {
		case <-ctx.Done():
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: ctx.Err()}
		case l := <-lookups:
			s := &fams[l.fam]
			s.looked = true
			s.addrs = l.addrs
			if l.err != nil || len(l.addrs) == 0 {
				s.done = true
				s.lookupErr = l.err
			}
			advance()
		case <-timerC:
			timerC = nil
			v4Allowed = true
			advance()
		case r := <-results:
			racers--
			if r.err == nil {
				return r.conn, nil
			}
			fams[r.fam].done = true
			fams[r.fam].dialErr = r.err
			advance()
		}
	}
}

// dialSerial tries one family's addresses in order and returns the first
// connection, or the first error — the one that names the likeliest cause.
func (d *Dialer) dialSerial(ctx context.Context, addrs []netip.Addr, port string) (net.Conn, error) {
	var first error
	for _, a := range addrs {
		if err := ctx.Err(); err != nil {
			if first == nil {
				first = err
			}
			break
		}
		c, err := d.Dial(ctx, "tcp", net.JoinHostPort(a.String(), port))
		if err == nil {
			return c, nil
		}
		if first == nil {
			first = err
		}
	}
	return nil, first
}

// failure says what each family did, IPv6 first, so a broken-IPv6 host reads
// as that rather than as one address that did not answer. A family that
// resolved to nothing is left out unless neither family resolved at all.
func (d *Dialer) failure(host, port string, fams [2]familyState) error {
	e6, e4 := fams[v6].dialErr, fams[v4].dialErr
	switch {
	case e6 != nil && e4 != nil:
		return &DialError{Address: net.JoinHostPort(host, port), IPv6: e6, IPv4: e4}
	case e6 != nil:
		return e6
	case e4 != nil:
		return e4
	}
	for _, f := range []family{v6, v4} {
		if fams[f].lookupErr != nil {
			return fams[f].lookupErr
		}
	}
	return &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// DialError is both families failing. It unwraps to both, so errors.Is and
// errors.As (a net.Error's Timeout, a syscall errno) see through it.
type DialError struct {
	Address    string
	IPv6, IPv4 error
}

func (e *DialError) Error() string {
	return fmt.Sprintf("dial tcp %s: IPv6: %v; IPv4: %v", e.Address, e.IPv6, e.IPv4)
}

func (e *DialError) Unwrap() []error { return []error{e.IPv6, e.IPv4} }

// IsUnreachable reports a dial the network refused to route — "no route to
// host" or "network is unreachable". A route can come back (an interface up,
// a VPN reconnecting, IPv6 withdrawn so the other family is tried), so a
// caller with a retry loop treats it as transient.
func IsUnreachable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no route to host") || strings.Contains(msg, "network is unreachable")
}
