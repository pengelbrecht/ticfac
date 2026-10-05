package forge

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"
)

// MaxBodyChars is GitHub's limit on a pull request body, in characters (not
// bytes): a longer body is refused with a bare 422 "Validation Failed".
// Epic 43y's close-out halted on exactly that refusal (2026-10-05): its body
// listed 75 findings with their full text, and no person could have helped.
const MaxBodyChars = 65536

// retryBodyChars is the shorter fit a length refusal is retried with: GitHub
// refused a body this surface had already fitted under MaxBodyChars, so its
// count and ours disagree, and the retry leaves a wide margin rather than
// guessing how far apart they are.
const retryBodyChars = 30000

// FitBody returns body when it is at most limit characters, and otherwise its
// head — cut at a line boundary where one is near — followed by a note that
// the rest was truncated and where the full record lives. The result is
// always at most limit characters and never splits a rune.
func FitBody(body string, limit int) string {
	if utf8.RuneCountInString(body) <= limit {
		return body
	}
	const note = "\n\n---\n\n_This body was truncated to fit GitHub's limit on a pull request body. " +
		"The full record is the run's own state under `.ticfac/runs/` on this PR's branch._\n"
	keep := limit - utf8.RuneCountInString(note)
	if keep < 0 {
		keep = 0
	}
	// The byte offset of the keep'th rune.
	cut, n := len(body), 0
	for i := range body {
		if n == keep {
			cut = i
			break
		}
		n++
	}
	head := body[:cut]
	if nl := strings.LastIndexByte(head, '\n'); nl > len(head)/2 {
		head = head[:nl]
	}
	return head + note
}

// APIErrorDetail is one entry of the errors[] list GitHub attaches to a 422:
// the resource, field and code it refused, and its message when it gave one.
type APIErrorDetail struct {
	Resource string `json:"resource"`
	Field    string `json:"field"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

func (d APIErrorDetail) String() string {
	var parts []string
	for _, kv := range [][2]string{{"resource", d.Resource}, {"field", d.Field}, {"code", d.Code}} {
		if kv[1] != "" {
			parts = append(parts, kv[0]+"="+kv[1])
		}
	}
	s := strings.Join(parts, " ")
	if d.Message != "" {
		if s != "" {
			s += ": "
		}
		s += d.Message
	}
	return s
}

// APIError is a non-2xx answer from GitHub: the status, the call, the API's
// own message and — for a validation failure — its errors[] detail, so a
// refusal names WHY ("body is too long") and not only "Validation Failed".
type APIError struct {
	Status  int
	Method  string
	Path    string
	Message string
	Errors  []APIErrorDetail
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("GitHub answered %d for %s %s: %s", e.Status, e.Method, e.Path, firstLine(e.Message))
	if len(e.Errors) > 0 {
		details := make([]string, 0, len(e.Errors))
		for _, d := range e.Errors {
			details = append(details, d.String())
		}
		msg += " (" + strings.Join(details, "; ") + ")"
	}
	return msg
}

// isBodyLengthRefusal reports whether err is GitHub refusing a PR write
// because its body is too long.
func isBodyLengthRefusal(err error) bool {
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusUnprocessableEntity {
		return false
	}
	for _, d := range api.Errors {
		text := strings.ToLower(d.Message + " " + d.Code)
		if strings.EqualFold(d.Field, "body") || (strings.Contains(text, "body") && strings.Contains(text, "long")) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(api.Message), "body is too long")
}

// writeBody performs a PR write whose payload carries body: fitted under
// GitHub's limit before it is sent, and — should GitHub still refuse it for
// length — retried once with a much shorter fit. A body's length is never a
// reason for a run to halt for a person: the full record lives on the
// branch, and the body is only a view of it.
func writeBody(body string, write func(body string) error) error {
	err := write(FitBody(body, MaxBodyChars))
	if err != nil && isBodyLengthRefusal(err) {
		return write(FitBody(body, retryBodyChars))
	}
	return err
}
