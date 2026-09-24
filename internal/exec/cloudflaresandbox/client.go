package cloudflaresandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
)

// The door's client: two routes, one mechanism.
//
//   - POST /api/sandbox/attempts — start one attempt's worker container,
//     named by the attempt's identity, and return its handle without
//     waiting for it.
//   - GET  /api/sandbox/attempts/:tick_id/:attempt — the state of the named
//     sandbox: running, finished with a result, or absent.
//
// Both are authorized by the run's OWN gateway token (D17) — the credential
// the orchestrator container holds and the only one it may hold — verified
// by the same function the model path, the git door, the wave door and the
// branch door authorize through. The run is never stated in a path or a
// body field the caller can choose: the credential says which run is
// speaking, so a container cannot dispatch or read on behalf of a run it
// is not.
//
// One rule this client holds that the route cannot: a door it cannot REACH
// is a transport error, never a `lost`. Reading an outage as absence is how
// an attempt gets written off while its container is still running — tick
// avx's rule, "unreachable is not absent", which the route's own header
// says lives in the client's error handling.

// doorPathStart is the start route, and doorPathAttempts is the prefix both
// routes live under — spelled once, because a path typed twice is a path
// that can drift.
const (
	doorPathStart    = "/api/sandbox/attempts"
	doorPathAttempts = doorPathStart + "/"
)

// DefaultRequestTimeout bounds one door call. The start route waits for the
// dispatch to be CONFIRMED — the green-start probe and the confirmed-dispatch
// wait the Worker-side spawn machinery runs — but never for the attempt to
// finish, so the budget is a container boot, not a tick: generous enough for
// a cold container, and short enough that a door gone quiet is an error a
// caller sees inside its own patience.
const DefaultRequestTimeout = 90 * time.Second

// doorError is the door's own refusal, kept typed: the status it answered
// with, the error class it named, and the detail it said. The classes are
// the route's own vocabulary — run_token_required, run_token_unknown,
// run_token_revoked, run_not_active, lease_lost, lease_held_by,
// sandbox_dispatch_not_wired, invalid_request, and the truthful-adoption
// refusal adoption_model_unknown (tick dyo) — and a caller that has to
// recover which one fired by matching on prose is the failure Appendix A #9
// is about.
type doorError struct {
	Status int
	Class  string
	Detail string
}

func (e *doorError) Error() string {
	return fmt.Sprintf("the sandbox dispatch door refused (%d %s): %s", e.Status, e.Class, e.Detail)
}

// AsDoorError reports whether err is the door's own refusal, and which one.
func AsDoorError(err error) (*doorError, bool) {
	var d *doorError
	if errors.As(err, &d) {
		return d, true
	}
	return nil, false
}

// Client is the door, addressed over HTTP.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
	now     func() time.Time
}

// NewClient points a client at one factory. The token is the run's own
// gateway token, never the operator's: the door refuses the operator's
// credential on purpose, because a container holding it would be the leak
// D17 exists to prevent.
func NewClient(baseURL, token string, timeout time.Duration, now func() time.Time) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("the factory's base URL is not configured: the sandbox dispatch door is reached " +
			"over HTTP at <factory>/api/sandbox/attempts, and a client with no factory to ask dispatches nothing")
	}
	if token == "" {
		return nil, fmt.Errorf("the run's gateway token is not configured: the door authorizes by the run's own " +
			"credential, and a client with no credential cannot start or read a sandbox")
	}
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	if now == nil {
		now = time.Now
	}
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: timeout},
		now:     now,
	}, nil
}

// startAttempt asks the door to start one attempt's container. It returns
// the handle the door minted and whether the container was adopted rather
// than freshly dispatched. It NEVER waits for the attempt to finish: what
// the door waits for is the dispatch being confirmed, which is a boot, not
// the tick's work.
func (c *Client) startAttempt(ctx context.Context, req *startRequest) (*subprocess.JobHandle, bool, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, false, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+doorPathStart, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	raw, err := c.roundTrip(httpReq, http.StatusOK, http.StatusCreated)
	if err != nil {
		return nil, false, err
	}
	var answer startResponse
	if err := strictUnmarshal(raw, &answer); err != nil {
		return nil, false, fmt.Errorf("the start route's answer is not the shape it documents: %w", err)
	}
	if answer.Handle == nil {
		return nil, false, fmt.Errorf("the start route answered with no handle: a start that names no job is a " +
			"job nobody can re-address")
	}
	// The handle arrives as JSON inside the answer; re-parse it through the
	// strict reader so the closed half of the record is refused loudly here
	// rather than trusted wherever the handle is next used.
	rawHandle, err := json.Marshal(answer.Handle)
	if err != nil {
		return nil, false, err
	}
	handle, err := parseHandle(rawHandle)
	if err != nil {
		return nil, false, err
	}
	return handle, answer.Adopted, nil
}

// attemptStatus reads the named sandbox's state BY IDENTITY: run from the
// credential, tick and attempt from the path. Nothing persisted by a caller
// that may since have died is consulted — the door re-addresses the
// container by name, which is what makes a handle re-queriable after the
// process that created it is gone.
func (c *Client) attemptStatus(ctx context.Context, tickID string, attempt int) (*subprocess.JobStatus, error) {
	path := c.baseURL + doorPathAttempts + url.PathEscape(tickID) + "/" + fmtInt(attempt)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	raw, err := c.roundTrip(httpReq, http.StatusOK)
	if err != nil {
		return nil, err
	}
	// The job id is not known to the client — the door derives it from the
	// credential — so the cross-check parseStatus makes on it is skipped
	// here and made by the caller, which does know.
	return parseStatus(raw, "")
}

// roundTrip performs one request and returns the body of an answer in the
// expected set, or the door's typed refusal.
func (c *Client) roundTrip(req *http.Request, expected ...int) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		// Unreachable is not absent: a door this client cannot reach is a
		// transport error, never a `lost` and never a status a caller could
		// mistake for one. The route's own header leaves this rule to the
		// client, and this is the line that holds it.
		return nil, fmt.Errorf("the sandbox dispatch door could not be reached at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDoorBody))
	if err != nil {
		return nil, fmt.Errorf("read the sandbox dispatch door's answer: %w", err)
	}
	for _, want := range expected {
		if resp.StatusCode == want {
			return raw, nil
		}
	}
	return nil, doorRefusal(resp.StatusCode, raw)
}

// maxDoorBody bounds one answer. The door's answers are records — a handle,
// a status — never streams; a body beyond this bound is not an answer this
// client can use, and reading one whole would be trusting a length nobody
// promised.
const maxDoorBody = 1 << 20

// doorRefusal turns a non-2xx answer into the door's typed refusal. A body
// this client cannot read as {error, detail} keeps the status and says so:
// the refusal is still the door's, and a status a caller cannot classify is
// better surfaced than swallowed.
func doorRefusal(status int, raw []byte) error {
	var body struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Error == "" {
		return &doorError{
			Status: status,
			Class:  "unreadable_refusal",
			Detail: fmt.Sprintf("the door answered %d with a body that is not its documented {error, detail} shape", status),
		}
	}
	return &doorError{Status: status, Class: body.Error, Detail: body.Detail}
}

// strictUnmarshal decodes a closed record: an unknown field is refused rather
// than ignored, and trailing content is refused rather than left unread. The
// door and this client are the two consumers of one mechanism, so a field
// one side invented is a drift to catch, not a courtesy to ignore.
func strictUnmarshal(data []byte, value any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing content after the record")
	}
	return nil
}

// fmtInt exists so the attempt in a path is spelled one way.
func fmtInt(n int) string {
	return fmt.Sprintf("%d", n)
}
