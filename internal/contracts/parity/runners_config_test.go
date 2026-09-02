package parity

import (
	"regexp"
	"strings"
	"testing"
)

// contracts/runners-config-contract.json — EXECUTABLE.
//
// Two rules over `.tick/runners.toml`: what `[sandbox].image` may be, and what
// `[orchestration].max_parallel` may be. ticfac reads neither file yet, but
// both rules are pinned as data — a regexp with a maximum length, and a
// minimum — so both are executable here from the fixture alone. The accepted
// and refused lists are the parity: a pattern relaxed on one side and not the
// other shows up as an accepted string this reader refuses.
//
// The image rule is the one that matters to ticfac: it is what stands between
// a repository's configuration and a container reference, and
// `orchestrator; rm -rf /` is in the refused list because an image reference
// is a name and never a place to hide a shell fragment.

const runnersConfigFile = "runners-config-contract.json"

type runnersConfig struct {
	Image struct {
		Path           string   `json:"path"`
		Pattern        string   `json:"pattern"`
		MaxLength      int      `json:"max_length"`
		BoundaryChar   string   `json:"boundary_char"`
		RefusalMessage string   `json:"refusal_message"`
		Accepted       []string `json:"accepted"`
		Refused        []string `json:"refused"`
	} `json:"image"`
	MaxParallel struct {
		Path              string   `json:"path"`
		Minimum           int      `json:"minimum"`
		RefusalMessage    string   `json:"refusal_message"`
		Accepted          []int    `json:"accepted"`
		Refused           []int    `json:"refused"`
		RefusedTOMLValues []string `json:"refused_toml_values"`
	} `json:"max_parallel"`
}

// acceptImage is ticfac's implementation of the image rule.
func acceptImage(c runnersConfig, value string) bool {
	if value == "" || len(value) > c.Image.MaxLength {
		return false
	}
	return regexp.MustCompile(c.Image.Pattern).MatchString(value)
}

func TestImageReferenceAcceptsAndRefusesTheContractsLists(t *testing.T) {
	var c runnersConfig
	readContract(t, runnersConfigFile, &c)

	if c.Image.Pattern == "" || c.Image.MaxLength <= 0 {
		t.Fatalf("the image rule is not pinned: %+v", c.Image)
	}
	if _, err := regexp.Compile(c.Image.Pattern); err != nil {
		t.Fatalf("the pinned image pattern does not compile in Go: %v", err)
	}
	if len(c.Image.Accepted) == 0 || len(c.Image.Refused) == 0 {
		t.Fatal("a rule with only accepted or only refused cases tests one direction")
	}

	for _, value := range c.Image.Accepted {
		if !acceptImage(c, value) {
			t.Errorf("the contract accepts %q and this reader refuses it", value)
		}
	}
	for _, value := range c.Image.Refused {
		if acceptImage(c, value) {
			t.Errorf("the contract refuses %q and this reader accepts it", value)
		}
	}
}

// The length boundary, from both sides: `max_length` characters is accepted
// and one more is not. `boundary_char` exists so the two implementations build
// the same string rather than each choosing a filler.
func TestImageLengthBoundaryIsExact(t *testing.T) {
	var c runnersConfig
	readContract(t, runnersConfigFile, &c)

	if c.Image.BoundaryChar == "" {
		t.Fatal("the contract names no boundary character, so each reader would invent its own")
	}
	atLimit := strings.Repeat(c.Image.BoundaryChar, c.Image.MaxLength)
	if !acceptImage(c, atLimit) {
		t.Errorf("a reference of exactly max_length (%d) was refused", c.Image.MaxLength)
	}
	if acceptImage(c, atLimit+c.Image.BoundaryChar) {
		t.Errorf("a reference of max_length+1 (%d) was accepted", c.Image.MaxLength+1)
	}
}

func TestMaxParallelAcceptsAndRefusesTheContractsLists(t *testing.T) {
	var c runnersConfig
	readContract(t, runnersConfigFile, &c)

	if c.MaxParallel.Minimum <= 0 {
		t.Fatalf("max_parallel has no positive minimum: %+v", c.MaxParallel)
	}
	for _, value := range c.MaxParallel.Accepted {
		if value < c.MaxParallel.Minimum {
			t.Errorf("the contract accepts %d, which is under its own minimum %d", value, c.MaxParallel.Minimum)
		}
	}
	for _, value := range c.MaxParallel.Refused {
		if value >= c.MaxParallel.Minimum {
			t.Errorf("the contract refuses %d, which satisfies its own minimum %d", value, c.MaxParallel.Minimum)
		}
	}
	// The typed refusals are what a TOML reader has to catch: a float, a
	// string and a boolean are refusals, never coercions.
	if len(c.MaxParallel.RefusedTOMLValues) == 0 {
		t.Error("no typed value is refused; a config reader that coerces \"3\" to 3 would pass")
	}
}

// Both rules name the config path they govern and the exact words they refuse
// with. The message is contract surface: an operator greps for it, and a
// reworded refusal on one side is a rule nobody can match against the other.
func TestBothRulesNameTheirPathAndRefusal(t *testing.T) {
	var c runnersConfig
	readContract(t, runnersConfigFile, &c)

	for name, pair := range map[string][2]string{
		"image":        {c.Image.Path, c.Image.RefusalMessage},
		"max_parallel": {c.MaxParallel.Path, c.MaxParallel.RefusalMessage},
	} {
		if pair[0] == "" {
			t.Errorf("%s names no config path", name)
		}
		if pair[1] == "" {
			t.Errorf("%s names no refusal message", name)
		}
	}
	if c.Image.RefusalMessage == c.MaxParallel.RefusalMessage {
		t.Error("two distinct rules share one refusal message; a reader cannot tell which refused")
	}
}
