package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"syscall"
	"time"
)

// Conn is one newline-delimited-JSON connection to the herdr server.
//
// Implementations need not be safe for concurrent use; the client owns a Conn
// for the lifetime of a single call or a single event subscription.
type Conn interface {
	// WriteMessage writes one message as a single line.
	WriteMessage(ctx context.Context, line []byte) error
	// ReadMessage reads the next line, without its trailing newline.
	ReadMessage(ctx context.Context) ([]byte, error)
	// Close releases the connection. It is safe to call more than once.
	Close() error
}

// Transport opens connections to the herdr API endpoint. It is injected so
// tests can drive the client against a fake server.
type Transport interface {
	// Dial opens a new connection. Because the herdr server closes a
	// connection after answering a single request, every call dials afresh.
	Dial(ctx context.Context) (Conn, error)
	// Endpoint describes what this transport connects to, for error messages.
	Endpoint() string
}

// UnixTransport dials a herdr unix stream socket. It is the transport [New]
// uses when [Options.Transport] is nil.
type UnixTransport struct {
	// SocketPath is the unix socket to dial.
	SocketPath string
	// DialTimeout bounds a single dial. Zero means DefaultDialTimeout.
	DialTimeout time.Duration
}

// DefaultDialTimeout bounds a connection attempt when none is configured.
const DefaultDialTimeout = 5 * time.Second

// NewUnixTransport returns a UnixTransport for the given socket path.
func NewUnixTransport(socketPath string) *UnixTransport {
	return &UnixTransport{SocketPath: socketPath}
}

// Endpoint reports the socket path this transport dials.
func (t *UnixTransport) Endpoint() string { return t.SocketPath }

// Dial opens a unix stream connection to the herdr socket.
//
// Two dial failures mean specifically that NO herdr is listening: the socket
// file does not exist (herdr was never started) and connection refused (the
// file is a leftover of a herdr that exited). Both are typed as
// [NotRunningError] so callers can tell "herdr is not running" from "herdr
// is the wrong version" ([ProtocolMismatchError]) — an exit path that
// flattens both into one generic failure sends the user hunting for a
// version fix when the real problem is `herdr daemon` never ran.
func (t *UnixTransport) Dial(ctx context.Context) (Conn, error) {
	timeout := t.DialTimeout
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var d net.Dialer
	nc, err := d.DialContext(dialCtx, "unix", t.SocketPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, &NotRunningError{Endpoint: t.SocketPath, Err: err}
		}
		return nil, fmt.Errorf("herd/client: dial %s: %w", t.SocketPath, err)
	}
	return newNetConn(nc), nil
}

// NotRunningError means nothing is listening at the herdr endpoint: herdr
// was never started, or it exited and left its socket file behind. It is one
// of the two ways connecting to herdr fails, and the one that means "herdr
// is not running" as opposed to "herdr is the wrong version"
// ([ProtocolMismatchError]). The underlying cause stays wrapped, so
// callers can still inspect it.
type NotRunningError struct {
	// Endpoint is the socket path that was dialled.
	Endpoint string
	// Err is the underlying dial error: fs.ErrNotExist when no socket file
	// exists, syscall.ECONNREFUSED when one exists but nothing accepts on it.
	Err error
}

// Error implements error.
func (e *NotRunningError) Error() string {
	return fmt.Sprintf("herd/client: herdr is not running at %s: %v", e.Endpoint, e.Err)
}

// Unwrap keeps the underlying dial error inspectable.
func (e *NotRunningError) Unwrap() error { return e.Err }

// IsNotRunning reports whether err means no herdr is listening at the
// endpoint — herdr was never started, or it exited and left its socket file
// behind. It is the classifier exit paths need to separate "not running"
// from "wrong version": see [NotRunningError].
func IsNotRunning(err error) bool {
	var nre *NotRunningError
	return errors.As(err, &nre)
}

// maxLineBytes caps a single protocol line. session.snapshot on a large
// session is the biggest realistic payload; 64 MiB is generous headroom.
const maxLineBytes = 64 << 20

// netConn adapts a net.Conn to Conn with newline framing and context-aware
// deadlines. Cancelling the context closes the connection, which unblocks a
// pending read — the only way to interrupt one.
type netConn struct {
	conn   net.Conn
	reader *bufio.Reader
}

func newNetConn(nc net.Conn) *netConn {
	return &netConn{conn: nc, reader: bufio.NewReaderSize(nc, 64<<10)}
}

func (c *netConn) WriteMessage(ctx context.Context, line []byte) error {
	stop := c.applyContext(ctx)
	defer stop()

	buf := make([]byte, 0, len(line)+1)
	buf = append(buf, line...)
	buf = append(buf, '\n')
	if _, err := c.conn.Write(buf); err != nil {
		return contextErr(ctx, err)
	}
	return nil
}

func (c *netConn) ReadMessage(ctx context.Context) ([]byte, error) {
	stop := c.applyContext(ctx)
	defer stop()

	var line []byte
	for {
		chunk, isPrefix, err := c.reader.ReadLine()
		if err != nil {
			return nil, contextErr(ctx, err)
		}
		line = append(line, chunk...)
		if len(line) > maxLineBytes {
			return nil, fmt.Errorf("herd/client: protocol line exceeds %d bytes", maxLineBytes)
		}
		if !isPrefix {
			return line, nil
		}
	}
}

func (c *netConn) Close() error { return c.conn.Close() }

// applyContext installs the context deadline on the socket and arranges for
// cancellation to close it. The returned func undoes both.
func (c *netConn) applyContext(ctx context.Context) func() {
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(dl)
	} else {
		_ = c.conn.SetDeadline(time.Time{})
	}
	stop := context.AfterFunc(ctx, func() { _ = c.conn.Close() })
	return func() { stop() }
}

// contextErr prefers the context's own error, so a cancelled call reports
// context.Canceled rather than "use of closed network connection".
func contextErr(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}
