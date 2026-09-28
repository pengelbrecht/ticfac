package httpnet

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// unreachableV6 is from the IPv6 documentation prefix (RFC 3849): it resolves
// here and routes nowhere, which is what a broken-IPv6 host does to every
// AAAA record.
var unreachableV6 = netip.MustParseAddr("2001:db8::1")

// fakeResolver answers each family on its own schedule, so a test can say
// "AAAA at once, A later" — the order macOS's lookup got wrong.
type fakeResolver struct {
	v6, v4           []netip.Addr
	v6Delay, v4Delay time.Duration
	v6Err, v4Err     error
}

func (r fakeResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	addrs, delay, err := r.v4, r.v4Delay, r.v4Err
	switch network {
	case "ip6":
		addrs, delay, err = r.v6, r.v6Delay, r.v6Err
	case "ip4":
	default:
		return nil, fmt.Errorf("asked for %q: the dialer must ask per family", network)
	}
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return addrs, err
}

// ipv4OnlyServer is a factory stand-in listening on 127.0.0.1 alone: the
// only way to reach it is IPv4.
func ipv4OnlyServer(t *testing.T) (port string) {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	_, port, _ = net.SplitHostPort(ln.Addr().String())
	return port
}

// hostUnreachable is what connect(2) says on a host with no IPv6 route.
func hostUnreachable(address string) error {
	return &net.OpError{Op: "dial", Net: "tcp", Addr: nil,
		Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}
}

// v6Unreachable wraps the real dialer: every IPv6 address fails at once with
// "no route to host", every IPv4 one is dialed for real.
func v6Unreachable(calls *atomic.Int32) func(ctx context.Context, network, address string) (net.Conn, error) {
	var nd net.Dialer
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		host, _, _ := net.SplitHostPort(address)
		if a, err := netip.ParseAddr(host); err == nil && a.Is6() {
			return nil, hostUnreachable(address)
		}
		return nd.DialContext(ctx, network, address)
	}
}

func get(t *testing.T, d *Dialer, port string, bound time.Duration) (time.Duration, error) {
	t.Helper()
	tr := NewTransport(d)
	tr.Proxy = nil // the listener is local; an environment proxy is not
	client := &http.Client{Timeout: bound, Transport: tr}
	t.Cleanup(client.CloseIdleConnections)
	began := time.Now()
	resp, err := client.Get("http://factory.example:" + port + "/health")
	took := time.Since(began)
	if err != nil {
		return took, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return took, fmt.Errorf("status %d", resp.StatusCode)
	}
	return took, nil
}

// short: loopback listener and a fake resolver; well under a second.
//
// The reported failure: IPv6 resolves first and fails at once with "no route
// to host", the A record is slower. The IPv4 attempt must start on the
// failure, not after a head start — FallbackDelay is set far past the bound
// so only that path can pass.
func TestAnUnreachableIPv6FallsBackToIPv4AtOnce(t *testing.T) {
	port := ipv4OnlyServer(t)
	var calls atomic.Int32
	d := &Dialer{
		Resolver: fakeResolver{
			v6: []netip.Addr{unreachableV6},
			v4: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, v4Delay: 100 * time.Millisecond,
		},
		Dial:            v6Unreachable(&calls),
		FallbackDelay:   time.Minute,
		ResolutionDelay: time.Minute,
	}
	took, err := get(t, d, port, 5*time.Second)
	if err != nil {
		t.Fatalf("the request should have reached the IPv4 listener: %v", err)
	}
	if took > 2*time.Second {
		t.Fatalf("took %s: IPv4 should start the moment IPv6 fails", took)
	}
	if calls.Load() != 2 {
		t.Fatalf("dialed %d addresses, want 2 (the IPv6 one, then the IPv4 one)", calls.Load())
	}
}

// short: loopback listener and a fake resolver; the real dialer tries a
// documentation-prefix address for at most the fallback delay.
//
// The same host with the real dialer and no seam on the connect: whatever
// this machine does with 2001:db8::1 — refuse it at once or let it hang —
// IPv4 answers within the bound.
func TestADocumentationPrefixIPv6FallsBackToIPv4WithTheRealDialer(t *testing.T) {
	port := ipv4OnlyServer(t)
	d := NewDialer()
	d.Resolver = fakeResolver{
		v6: []netip.Addr{unreachableV6},
		v4: []netip.Addr{netip.MustParseAddr("127.0.0.1")},
	}
	took, err := get(t, d, port, 5*time.Second)
	if err != nil {
		t.Fatalf("the request should have reached the IPv4 listener: %v", err)
	}
	if took > 3*time.Second {
		t.Fatalf("took %s, want within the fallback delay plus a connect", took)
	}
}

// short: loopback listener and a fake resolver; well under a second.
//
// An IPv6 connect that neither answers nor fails gets FallbackDelay's head
// start and no more, and the hanging attempt is cancelled once IPv4 wins.
func TestAHangingIPv6LosesToIPv4AfterTheHeadStart(t *testing.T) {
	port := ipv4OnlyServer(t)
	cancelled := make(chan struct{})
	var nd net.Dialer
	d := &Dialer{
		Resolver: fakeResolver{
			v6: []netip.Addr{unreachableV6},
			v4: []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		},
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if strings.HasPrefix(address, "[") {
				<-ctx.Done()
				close(cancelled)
				return nil, ctx.Err()
			}
			return nd.DialContext(ctx, network, address)
		},
		FallbackDelay:   50 * time.Millisecond,
		ResolutionDelay: 50 * time.Millisecond,
	}
	if _, err := get(t, d, port, 5*time.Second); err != nil {
		t.Fatalf("the request should have reached the IPv4 listener: %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the losing IPv6 attempt was never cancelled")
	}
}

// short: fake resolver and a fake dialer; no sockets.
//
// A name with no AAAA record at all dials IPv4 without waiting on anything,
// and one with no A record dials IPv6 alone.
func TestASingleFamilyNameDialsThatFamily(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    fakeResolver
		want string
	}{
		{"A only", fakeResolver{v4: []netip.Addr{netip.MustParseAddr("192.0.2.7")},
			v6Err: &net.DNSError{Err: "no such host", IsNotFound: true}}, "192.0.2.7:443"},
		{"AAAA only", fakeResolver{v6: []netip.Addr{netip.MustParseAddr("2001:db8::7")}}, "[2001:db8::7]:443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dialed string
			d := &Dialer{Resolver: tc.r, FallbackDelay: time.Minute, ResolutionDelay: time.Minute,
				Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
					dialed = address
					c, _ := net.Pipe()
					return c, nil
				}}
			c, err := d.DialContext(context.Background(), "tcp", "h.example:443")
			if err != nil {
				t.Fatal(err)
			}
			c.Close()
			if dialed != tc.want {
				t.Fatalf("dialed %q, want %q", dialed, tc.want)
			}
		})
	}
}

// short: fake resolver and a fake dialer; no sockets.
//
// Both families failing says what each did, and stays classifiable: the
// errno is reachable through errors.Is and IsUnreachable says transient.
func TestBothFamiliesFailingNamesBoth(t *testing.T) {
	d := &Dialer{
		Resolver: fakeResolver{
			v6: []netip.Addr{unreachableV6},
			v4: []netip.Addr{netip.MustParseAddr("192.0.2.1")},
		},
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return nil, hostUnreachable(address)
		},
		FallbackDelay: time.Minute, ResolutionDelay: time.Minute,
	}
	_, err := d.DialContext(context.Background(), "tcp", "h.example:443")
	var de *DialError
	if !errors.As(err, &de) {
		t.Fatalf("got %v (%T), want a *DialError", err, err)
	}
	if !errors.Is(err, syscall.EHOSTUNREACH) || !IsUnreachable(err) {
		t.Fatalf("%v should classify as unreachable", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "IPv6:") || !strings.Contains(msg, "IPv4:") {
		t.Fatalf("%q should name both families", msg)
	}
}

// short: fake resolver and a fake dialer; no sockets.
//
// An IP literal has no other family to fall back to and is dialed as given,
// without a lookup.
func TestAnIPLiteralIsDialedAsGiven(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:80", "[::1]:80"} {
		var dialed string
		d := &Dialer{
			Resolver: fakeResolver{v6Err: errors.New("looked up"), v4Err: errors.New("looked up")},
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				dialed = address
				c, _ := net.Pipe()
				return c, nil
			},
		}
		c, err := d.DialContext(context.Background(), "tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		if dialed != addr {
			t.Fatalf("dialed %q, want %q", dialed, addr)
		}
	}
}

// short: a table of errors.
func TestIsUnreachable(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{hostUnreachable("x"), true},
		{&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}, true},
		{fmt.Errorf(`Get "https://f.example/health": %w`, hostUnreachable("x")), true},
		{errors.New(`Get "https://f.example/health": dial tcp [2001:db8::1]:443: connect: no route to host`), true},
		{errors.New("dial tcp 192.0.2.1:443: connect: network is unreachable"), true},
		{&net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, false},
		{errors.New("HTTP 401"), false},
	} {
		if got := IsUnreachable(tc.err); got != tc.want {
			t.Errorf("IsUnreachable(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// short: reads the repository's Go sources; no network.
//
// Every client goes through the shared transport. A `&http.Client{...}` built
// anywhere else dials with Go's default dialer, which is the one that had no
// IPv4 to fall back to — so a new one is refused here rather than found on
// the next host with broken IPv6.
func TestEveryHTTPClientUsesTheSharedTransport(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	self, _ := filepath.Abs(".")
	var offenders []string
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			switch e.Name() {
			case ".git", "node_modules", "vendor", ".claude":
				return filepath.SkipDir
			}
			if path == self {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "//") {
				continue
			}
			if strings.Contains(code, "http.Client{") || strings.Contains(code, "http.DefaultClient") ||
				strings.Contains(code, "http.Transport{") {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", rel, i+1, code))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("build clients with httpnet.Client (or httpnet.Transport), not by hand:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
