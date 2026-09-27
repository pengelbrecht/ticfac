package forge

import (
	"errors"
	"strings"
	"testing"
)

// The credential ladder (tick vo4): GITHUB_TOKEN first, gh's own auth
// second. The ladder is ONE because two surfaces that each answered for a
// credential were the gap — doctor accepted `gh auth token` while the run's
// surface read the environment only, so a machine with gh authed and no
// GITHUB_TOKEN passed doctor and was refused by the run it was checked for.
// Both ask the same ladder now, so one answer is one answer.
//
// The gh rung is a seam for the same reason doctor's probes are: the real
// answer is the host's, and a test that consulted it would report the host's
// gh once and its absence another time — not a test.

// saveGHRung overrides the gh rung with a controlled answer and restores the
// production one on cleanup.
func saveGHRung(t *testing.T, answer func() ([]byte, error)) {
	t.Helper()
	saved := ghAuthToken
	ghAuthToken = answer
	t.Cleanup(func() { ghAuthToken = saved })
}

// The env rung answers first, and gh is never consulted once it has — an
// operator who provisioned a GITHUB_TOKEN keeps the narrower credential, the
// one the factory's own ladder names, without gh seeing it asked for.
func TestResolveTokenFromAnswersTheEnvRungFirst(t *testing.T) {
	saveGHRung(t, func() ([]byte, error) {
		t.Error("the gh rung was consulted while the env rung had answered")
		return nil, errors.New("unreachable")
	})
	t.Setenv(TokenEnv, " from-env \n")

	token, source, err := ResolveTokenFrom()
	if err != nil {
		t.Fatalf("the env rung did not answer: %v", err)
	}
	if token != "from-env" {
		t.Errorf("token = %q, want the trimmed env value", token)
	}
	if source != TokenSourceEnv {
		t.Errorf("source = %q, want the env rung", source)
	}
}

// The gh rung answers when the environment holds nothing — the half of the
// ladder this tick exists for: a machine with gh authed and no GITHUB_TOKEN
// is a machine the run's own surface accepts, not one doctor smiles at while
// the run refuses it.
func TestResolveTokenFromAnswersTheGHRungWhenTheEnvHoldsNothing(t *testing.T) {
	saveGHRung(t, func() ([]byte, error) { return []byte(" from-gh \n"), nil })
	t.Setenv(TokenEnv, "")

	token, source, err := ResolveTokenFrom()
	if err != nil {
		t.Fatalf("the gh rung did not answer: %v", err)
	}
	if token != "from-gh" {
		t.Errorf("token = %q, want the trimmed gh value", token)
	}
	if source != TokenSourceGH {
		t.Errorf("source = %q, want the gh rung", source)
	}
}

// Neither rung answering names BOTH — the refusal a person reads says the
// two fixes there are, not just the one half of the ladder.
func TestResolveTokenFromNamesBothRungsWhenNeitherAnswers(t *testing.T) {
	saveGHRung(t, func() ([]byte, error) { return nil, errors.New("gh is not installed") })
	t.Setenv(TokenEnv, "")

	token, _, err := ResolveTokenFrom()
	if token != "" {
		t.Errorf("a token answered where none should have: %q", token)
	}
	if err == nil {
		t.Fatal("the ladder answered ok with no credential")
	}
	for _, want := range []string{TokenEnv, "gh", "gh is not installed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// A gh that answers with no token is a refusal, not a pass: the rung exists
// to carry a credential, and an empty one carries none.
func TestResolveTokenFromRefusesAGHThatPrintsNoToken(t *testing.T) {
	saveGHRung(t, func() ([]byte, error) { return []byte("  \n"), nil })
	t.Setenv(TokenEnv, "")

	if token, _, err := ResolveTokenFrom(); token != "" || err == nil {
		t.Fatalf("an empty gh answer built a credential: %q, %v", token, err)
	} else if !strings.Contains(err.Error(), TokenEnv) || !strings.Contains(err.Error(), "printed no token") {
		t.Errorf("the refusal does not say what was empty: %v", err)
	}
}
