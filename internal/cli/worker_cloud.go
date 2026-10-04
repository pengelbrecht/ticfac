package cli

// The factory-hosted half of `ticfac watch <run> <tick>` and `ticfac steer`
// (tick y03): a cloud worker is a WorkerAgent Durable Object, and the
// factory serves the operator a window into it on the operator's own token —
// GET /api/runs/:run/workers/:tick/:attempt (its state), …/watch (its watch
// WebSocket) and POST …/steer (cloudflare/src/sandbox-dispatch.ts documents
// the routes). The watch socket sends the same frames the local steer
// socket does, plus the attempt's state and log, so internal/workerview
// reads both.
//
// The WebSocket client below is the smallest RFC 6455 client the watch
// needs — text messages in, pings answered, the close honoured, and masked
// frames out — because the standard library has none and a dependency for
// one read loop is not worth its weight.

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"

	"github.com/pengelbrecht/ticfac/internal/httpnet"
	"github.com/pengelbrecht/ticfac/internal/workerview"
)

// cloudWorker is a worker the factory hosts: one attempt's WorkerAgent.
type cloudWorker struct {
	client  *cloudClient
	runID   string
	tickID  string
	attempt int
}

func (w *cloudWorker) title() string {
	return fmt.Sprintf("%s#%d · %s", w.tickID, w.attempt, w.runID)
}

// workerPath is the attempt's operator route, suffixed.
func (w *cloudWorker) workerPath(suffix string) string {
	path := "/api/runs/" + url.PathEscape(w.runID) + "/workers/" + url.PathEscape(w.tickID) + "/" + strconv.Itoa(w.attempt)
	if suffix != "" {
		path += "/" + suffix
	}
	return path
}

// errWorkerRefused is the factory's own answer about the worker — no such
// run, no such attempt, a run whose workers are not WorkerAgents: terminal,
// unlike a socket that broke on the way.
type errWorkerRefused struct{ err error }

func (e errWorkerRefused) Error() string { return e.err.Error() }
func (e errWorkerRefused) Unwrap() error { return e.err }

func (w *cloudWorker) attach(ctx context.Context) (<-chan workerview.Frame, error) {
	conn, err := dialWebSocket(ctx, w.client, w.workerPath("watch"))
	if err != nil {
		var refused cloudAPIError
		if errors.As(err, &refused) && refused.status >= 400 && refused.status < 500 {
			return nil, errWorkerRefused{err: err}
		}
		return nil, err
	}
	out := make(chan workerview.Frame, 64)
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	go func() {
		defer close(out)
		defer conn.Close()
		for {
			message, err := conn.readMessage()
			if err != nil {
				return
			}
			f, err := workerview.ParseFrame(message)
			if err != nil {
				// The socket's own error frame (a refused steer message)
				// has a type; anything else unreadable is skipped, not
				// the end of the watch.
				continue
			}
			select {
			case out <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// settled is the agent's own word: its phase is settled.
func (w *cloudWorker) settled(m *workerview.Model) bool {
	return m.State != nil && m.State.Phase == "settled"
}

func (w *cloudWorker) steer(ctx context.Context, text, requestID string) error {
	_, err := w.client.request(ctx, http.MethodPost, w.workerPath("steer"),
		map[string]string{"text": text, "request_id": requestID})
	return err
}

// resolveCloudWorker finds the tick's attempt in the factory: the run is a
// cloud run id as given, or the factory's run for an epic id this
// checkout's project holds; the attempt is the one asked for, or the
// newest the factory booted.
func resolveCloudWorker(ctx context.Context, repo, runArg, tickID string, attempt int) (*cloudWorker, error) {
	runID := runArg
	if !looksLikeCloudRunID(runArg) {
		resolved, _, err := cloudRunForEpic(ctx, repo, runArg)
		if err != nil {
			return nil, err
		}
		if resolved == "" {
			return nil, fmt.Errorf("the factory holds no run for %s for this checkout (or no factory is configured)", runArg)
		}
		runID = resolved
	}
	client, err := newCloudClient()
	if err != nil {
		return nil, err
	}
	which := "latest"
	if attempt > 0 {
		which = strconv.Itoa(attempt)
	}
	data, err := client.request(ctx, http.MethodGet,
		"/api/runs/"+url.PathEscape(runID)+"/workers/"+url.PathEscape(tickID)+"/"+which, nil)
	if err != nil {
		return nil, err
	}
	var answer struct {
		Attempt int `json:"attempt"`
	}
	if err := json.Unmarshal(data, &answer); err != nil || answer.Attempt < 1 {
		return nil, fmt.Errorf("the factory's answer about tick %s's worker is not the worker route's shape: %s", tickID, data)
	}
	return &cloudWorker{client: client, runID: runID, tickID: tickID, attempt: answer.Attempt}, nil
}

// ------------------------------------------------------------ websocket ---

// websocketGUID is RFC 6455's handshake constant.
const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// maxWebSocketMessage bounds one message: a snapshot carries the whole
// conversation, as on the local stream.
const maxWebSocketMessage = 256 * 1024 * 1024

// workerWSClient opens the watch socket. HTTP/1.1 only — the upgrade is an
// HTTP/1.1 mechanism, and a transport that negotiated h2 would never see a
// 101 — and no overall timeout: a watch lasts as long as the worker does.
var workerWSClient = func() *http.Client {
	transport := httpnet.NewTransport(httpnet.NewDialer())
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	client := httpnet.Client(0)
	client.Transport = transport
	return client
}()

// wsConn is the client side of one WebSocket.
type wsConn struct {
	rwc     io.ReadWriteCloser
	r       *bufio.Reader
	writeMu sync.Mutex
	once    sync.Once
}

// dialWebSocket upgrades GET path on the factory to a WebSocket, on the
// operator's token. A refusal is the factory's typed answer (cloudAPIError).
func dialWebSocket(ctx context.Context, client *cloudClient, path string) (*wsConn, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(raw)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+client.token)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)
	resp, err := workerWSClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("open the worker's watch socket: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, cloudAPIError{status: resp.StatusCode, body: body}
	}
	sum := sha1.Sum([]byte(key + websocketGUID))
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		resp.Body.Close()
		return nil, errors.New("the factory's watch socket answered a handshake that is not this one's")
	}
	rwc, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		return nil, errors.New("the upgraded connection is not writable")
	}
	return &wsConn{rwc: rwc, r: bufio.NewReader(rwc)}, nil
}

// WebSocket opcodes.
const (
	wsContinuation = 0x0
	wsText         = 0x1
	wsBinary       = 0x2
	wsClose        = 0x8
	wsPing         = 0x9
	wsPong         = 0xA
)

// readMessage reads one whole data message, answering pings on the way. A
// close frame (or the connection's end) is io.EOF.
func (c *wsConn) readMessage() ([]byte, error) {
	var message []byte
	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case wsPing:
			if err := c.writeFrame(wsPong, payload); err != nil {
				return nil, err
			}
			continue
		case wsPong:
			continue
		case wsClose:
			_ = c.writeFrame(wsClose, payload)
			return nil, io.EOF
		case wsText, wsBinary, wsContinuation:
			message = append(message, payload...)
			if len(message) > maxWebSocketMessage {
				return nil, fmt.Errorf("a watch message is over %d bytes", maxWebSocketMessage)
			}
			if fin {
				return message, nil
			}
		default:
			return nil, fmt.Errorf("an unknown WebSocket opcode %#x", opcode)
		}
	}
}

func (c *wsConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var head [2]byte
	if _, err = io.ReadFull(c.r, head[:]); err != nil {
		return
	}
	fin, opcode = head[0]&0x80 != 0, head[0]&0x0F
	masked := head[1]&0x80 != 0
	length := uint64(head[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.r, ext[:]); err != nil {
			return
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.r, ext[:]); err != nil {
			return
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if length > maxWebSocketMessage {
		err = fmt.Errorf("a WebSocket frame of %d bytes", length)
		return
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.r, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.r, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return
}

// writeFrame writes one final frame, masked as a client's must be.
func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	frame := []byte{0x80 | opcode}
	switch n := len(payload); {
	case n < 126:
		frame = append(frame, 0x80|byte(n))
	case n <= 0xFFFF:
		frame = append(frame, 0x80|126, byte(n>>8), byte(n))
	default:
		frame = append(frame, 0x80|127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(n))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	frame = append(frame, mask[:]...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	_, err := c.rwc.Write(frame)
	return err
}

// Close sends the close frame once and closes the connection.
func (c *wsConn) Close() {
	c.once.Do(func() {
		_ = c.writeFrame(wsClose, []byte{0x03, 0xE8}) // 1000, normal closure
		_ = c.rwc.Close()
	})
}

// isWorkerRefusal says an attach error is the factory's terminal answer.
func isWorkerRefusal(err error) bool {
	var refused errWorkerRefused
	return errors.As(err, &refused)
}
