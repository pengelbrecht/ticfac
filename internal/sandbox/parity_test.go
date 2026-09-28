package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The port of ticks internal/sandbox's image_parity_test.go: the `[sandbox].image`
// declaration has two readers.
//
// This package's is authoritative — it is `ticfac sandbox image`, running in
// the checkout, and the entrypoint refuses a boot on its verdict. The factory
// Worker has a second one (cloudflare/src/repo-config.ts), because the control
// plane has to know which image to boot BEFORE a container exists to read
// anything.
//
// Two readers of one format is exactly the shape of the bug this repository
// has already paid for once: a fix landed in TypeScript only, the Go half kept
// minting unusable records, and both suites were green because each was
// internally consistent. So the two read the same cases from one file, and a
// divergence fails here rather than in a container.

const parityCases = "../../contracts/sandbox-image-cases.json"

type imageParityCase struct {
	Name    string `json:"name"`
	Why     string `json:"why"`
	TOML    string `json:"toml"`
	Image   string `json:"image"`
	Refused bool   `json:"refused"`
}

func TestDeclaredImageParityWithTheFactoryReader(t *testing.T) {
	body, err := os.ReadFile(parityCases)
	if err != nil {
		t.Fatalf("the shared cases are what both readers are pinned to: %v", err)
	}
	var file struct {
		Cases []imageParityCase `json:"cases"`
	}
	if err := json.Unmarshal(body, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Cases) == 0 {
		t.Fatal("no cases: a parity guard with nothing in it guards nothing")
	}

	const base = "ticks-orchestrator:0.31.0"
	for _, tc := range file.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".tick"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".tick", "runners.toml"), []byte(tc.TOML), 0o644); err != nil {
				t.Fatal(err)
			}

			image, declared, err := Image(root, base)
			if tc.Refused {
				if err == nil {
					t.Fatalf("read %q from a declaration that must be refused (%s)", image, tc.Why)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: %v", tc.Why, err)
			}
			want, wantDeclared := tc.Image, tc.Image != ""
			if !wantDeclared {
				want = base
			}
			if image != want || declared != wantDeclared {
				t.Errorf("Image() = (%q, %v), want (%q, %v) — %s", image, declared, want, wantDeclared, tc.Why)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The worker prompt template, pinned where the result contract depends on it.
// ---------------------------------------------------------------------------

// TestBuildPrompt pins the parts of the worker prompt the result contract
// depends on (the port of ticks internal/workerprompt's prompt_test.go).
func TestBuildPrompt(t *testing.T) {
	got := BuildPrompt(PromptInput{
		TickID:      "1aw",
		Title:       "tk herd spawn",
		Description: "do the thing",
		Acceptance:  "tests green",
		EpicID:      "gyz",
		EpicTitle:   "Herd helper CLI",
		Branch:      "tick/1aw",
		Base:        "abc1234",
	})
	for _, want := range []string{
		"branch tick/1aw",
		"RESULT-1aw.md",
		"abc1234",
		"Herd helper CLI (gyz)",
		"do the thing",
		"tests green",
		"Do NOT run any `tk` command",
		"STATUS: DONE",
		"STATUS: BLOCKED",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "RESULT.md\n") {
		t.Error("prompt names a shared RESULT.md, which collides on the second merge of a wave")
	}
}
