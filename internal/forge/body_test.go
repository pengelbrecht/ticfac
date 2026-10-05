package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

// GitHub refuses a PR body over 65,536 characters with a bare 422
// "Validation Failed" — the detail of WHY sits in the answer's errors[] list.
// Epic 43y's close-out halted on exactly that refusal (2026-10-05) with only
// "Validation Failed" to go on; these tests pin the three repairs: the error
// carries GitHub's detail, no over-limit body is ever sent, and a length
// refusal is answered by a shorter body rather than by a person.

func bodyTooLong(t *testing.T, w http.ResponseWriter) {
	writeJSON(t, w, http.StatusUnprocessableEntity, map[string]any{
		"message": "Validation Failed",
		"errors": []map[string]string{{
			"resource": "Issue", "field": "body", "code": "custom",
			"message": "body is too long (maximum is 65536 characters)",
		}},
		"documentation_url": "https://docs.github.com/rest/pulls/pulls#create-a-pull-request",
	})
}

func TestA422CarriesGitHubsErrorDetail(t *testing.T) {
	t.Parallel()
	g, _ := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusUnprocessableEntity, map[string]any{
			"message": "Validation Failed",
			"errors": []map[string]string{{
				"resource": "PullRequest", "field": "head", "code": "invalid",
			}},
		})
	})
	_, err := g.Open(context.Background(), "epic/43y", "main", "title", "body")
	if err == nil {
		t.Fatal("the refusal was accepted")
	}
	for _, want := range []string{"422", "Validation Failed", "PullRequest", "head", "invalid"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}
}

// sentBody reads the body field of a PR write the test server saw.
func sentBody(t *testing.T, r *http.Request) string {
	t.Helper()
	var asked map[string]string
	if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
		t.Fatal(err)
	}
	return asked["body"]
}

func TestOpenAndUpdateBodyNeverSendAnOverLimitBody(t *testing.T) {
	t.Parallel()
	// Multi-byte runes on purpose: GitHub counts characters, and a byte count
	// would truncate a body it would have taken.
	long := "HEAD of the body\n" + strings.Repeat("finding — text\n", 6000)
	if utf8.RuneCountInString(long) <= MaxBodyChars {
		t.Fatalf("the test body is only %d characters", utf8.RuneCountInString(long))
	}
	var bodies []string
	g, _ := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		body := sentBody(t, r)
		bodies = append(bodies, body)
		if utf8.RuneCountInString(body) > MaxBodyChars {
			bodyTooLong(t, w)
			return
		}
		writeJSON(t, w, http.StatusOK, map[string]any{"number": 7})
	})
	if _, err := g.Open(context.Background(), "epic/43y", "main", "title", long); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := g.UpdateBody(context.Background(), PullRequest{Number: 7}, long); err != nil {
		t.Fatalf("UpdateBody: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("%d writes, want 2 (no over-limit body may be sent and refused first)", len(bodies))
	}
	for _, body := range bodies {
		if !strings.HasPrefix(body, "HEAD of the body\n") {
			t.Errorf("the fitted body lost its head: %.40q", body)
		}
		if !strings.Contains(body, "truncated") {
			t.Errorf("the fitted body does not say it was truncated")
		}
	}
}

// A body under ticfac's own limit that GitHub still refuses for length (its
// count and ours disagreeing) is retried shorter — never handed to a person.
func TestALengthRefusalIsRetriedWithAShorterBody(t *testing.T) {
	t.Parallel()
	body := "HEAD\n" + strings.Repeat("x", 50_000)
	var bodies []string
	g, _ := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		sent := sentBody(t, r)
		bodies = append(bodies, sent)
		if len(sent) > 40_000 {
			bodyTooLong(t, w)
			return
		}
		writeJSON(t, w, http.StatusOK, map[string]any{"number": 7})
	})
	if err := g.UpdateBody(context.Background(), PullRequest{Number: 7}, body); err != nil {
		t.Fatalf("UpdateBody after a length refusal: %v", err)
	}
	if _, err := g.Open(context.Background(), "epic/43y", "main", "title", body); err != nil {
		t.Fatalf("Open after a length refusal: %v", err)
	}
	if len(bodies) != 4 {
		t.Fatalf("%d writes, want 4 (one refused and one retried each)", len(bodies))
	}
	for _, i := range []int{1, 3} {
		if !strings.HasPrefix(bodies[i], "HEAD\n") || !strings.Contains(bodies[i], "truncated") {
			t.Errorf("the retried body %d is not the head plus a truncation note: %.40q", i, bodies[i])
		}
	}
}

// A 422 for anything but the body's length is not retried: it is the forge's
// answer, and the caller's typed refusal carries it.
func TestANonLengthRefusalIsNotRetried(t *testing.T) {
	t.Parallel()
	calls := 0
	g, _ := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(t, w, http.StatusUnprocessableEntity,
			map[string]string{"message": "A pull request already exists for this branch"})
	})
	if _, err := g.Open(context.Background(), "epic/43y", "main", "title", "body"); err == nil {
		t.Fatal("the refusal was accepted")
	}
	if calls != 1 {
		t.Errorf("%d calls, want 1", calls)
	}
}

func TestFitBody(t *testing.T) {
	t.Parallel()
	if got := FitBody("short", 100); got != "short" {
		t.Errorf("a body under the limit changed: %q", got)
	}
	long := strings.Repeat("line ——\n", 1000)
	got := FitBody(long, 500)
	if n := utf8.RuneCountInString(got); n > 500 {
		t.Errorf("the fitted body is %d characters, over 500", n)
	}
	if !utf8.ValidString(got) {
		t.Error("the fitted body split a rune")
	}
	if !strings.HasPrefix(got, "line ——\n") || !strings.Contains(got, "truncated") {
		t.Errorf("the fitted body is not the head plus a note: %q", got)
	}
}
