package runconfig

import (
	"strings"
	"testing"
)

// In ticks' copy of this package this file opens with two tests that read the
// authoring doc (runners-config.md, pi-runner.md) and prove its fenced TOML
// blocks load. Those tests need ticks' skill references and stayed there; the
// transcribed examples they were checking are asserted as fixture documents
// here (docexamples_test.go, load_test.go).

// TestCommandSurfaceParses proves worked example 6 — routing plus all four
// command tables — loads and exposes what the doc says it does. This is the
// shape `.tick/config.md`'s structured sections migrate into, so a repo that
// has migrated must still be loadable by `tk herd spawn`.
func TestCommandSurfaceParses(t *testing.T) {
	cfg, err := Parse([]byte(docExample6))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if cfg.Testing == nil || cfg.Evidence == nil || cfg.Environment == nil {
		t.Fatalf("Testing/Evidence/Environment = %v/%v/%v, want all present", cfg.Testing, cfg.Evidence, cfg.Environment)
	}
	if !strings.Contains(cfg.Testing.Notes, "internal/worktree") {
		t.Errorf("testing.notes = %q, want the narrative caveat", cfg.Testing.Notes)
	}
	if got := strings.Join(sortedKeys(cfg.Testing.Commands), ","); got != "go,runner" {
		t.Errorf("testing.commands = %q, want go,runner", got)
	}
	if got := cfg.Testing.Commands["go"].Command; got != "go test -short -count=1 ./..." {
		t.Errorf("testing.commands.go.command = %q", got)
	}
	if got := cfg.Testing.Commands["go"].Description; got != "Go suite, short mode" {
		t.Errorf("testing.commands.go.description = %q", got)
	}
	if got := strings.Join(sortedKeys(cfg.Evidence.Commands), ","); got != "herd-helper-quick,herd-plugin-offline,package-rpc" {
		t.Errorf("evidence.commands = %q", got)
	}
	if got := strings.Join(sortedKeys(cfg.Environment.Commands), ","); got != "git-identity,go-toolchain,pnpm" {
		t.Errorf("environment.commands = %q", got)
	}
	if got := cfg.Evidence.Acceptance["A4"]; got != "go" {
		t.Errorf("evidence.acceptance.A4 = %q, want go", got)
	}
	if got := len(cfg.Evidence.Acceptance); got != 4 {
		t.Errorf("len(acceptance) = %d, want 4", got)
	}
}

// TestCommandSurfaceIsOptional: the tables are optional, and a config without
// them is exactly as valid as it was before they existed.
func TestCommandSurfaceIsOptional(t *testing.T) {
	cfg, err := Parse([]byte(docExample1))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Testing != nil || cfg.Evidence != nil || cfg.Environment != nil {
		t.Errorf("absent tables decoded to non-nil: %v/%v/%v", cfg.Testing, cfg.Evidence, cfg.Environment)
	}
}

// TestCommandNegatives covers runners-config.md's new rows in "Caught by the
// schema" and every row of "Caught by the config loader". Each must be a hard
// stop with a nil config: a typo that silently degraded a repo to no-evidence
// is the failure this table exists to prevent.
func TestCommandNegatives(t *testing.T) {
	const roles = "\n[roles.implement]\nkind = \"claude\"\n"

	tests := []struct {
		name     string
		toml     string
		wantPath string
		wantMsg  string
	}{
		{
			name:     "unknown key in testing",
			toml:     roles + "\n[testing]\nnotez = \"typo\"\n",
			wantPath: "testing.notez",
			wantMsg:  "unknown key",
		},
		{
			name:     "unknown key in a command",
			toml:     roles + "\n[testing.commands]\ngo = { command = \"go test ./...\", describtion = \"typo\" }\n",
			wantPath: "testing.commands.go.describtion",
			wantMsg:  "unknown key",
		},
		{
			name:     "command key missing",
			toml:     roles + "\n[testing.commands]\ngo = { description = \"label only\" }\n",
			wantPath: "testing.commands.go.command",
			wantMsg:  "required",
		},
		{
			name:     "empty command",
			toml:     roles + "\n[testing.commands]\ngo = { command = \"\" }\n",
			wantPath: "testing.commands.go.command",
			wantMsg:  "must not be empty",
		},
		{
			name:     "control character in a command",
			toml:     roles + "\n[testing.commands]\ngo = { command = \"go test\\u0000rm -rf /\" }\n",
			wantPath: "testing.commands.go.command",
			wantMsg:  "control character",
		},
		{
			name:     "command id out of shape",
			toml:     roles + "\n[testing.commands]\nGo = { command = \"go test ./...\" }\n",
			wantPath: "testing.commands.Go",
			wantMsg:  "command id",
		},
		{
			name:     "acceptance id out of shape",
			toml:     roles + "\n[evidence.commands]\ngo = { command = \"go test ./...\" }\n[evidence.acceptance]\nItem1 = \"go\"\n",
			wantPath: "evidence.acceptance.Item1",
			wantMsg:  "A<n>",
		},
		{
			name:     "acceptance item zero",
			toml:     roles + "\n[evidence.commands]\ngo = { command = \"go test ./...\" }\n[evidence.acceptance]\nA0 = \"go\"\n",
			wantPath: "evidence.acceptance.A0",
			wantMsg:  "A<n>",
		},
		{
			name:     "acceptance points at a command not defined in the file",
			toml:     roles + "\n[evidence.commands]\npackage-rpc = { command = \"node verify.ts\" }\n[evidence.acceptance]\nA1 = \"package-rcp\"\n",
			wantPath: "evidence.acceptance.A1",
			wantMsg:  "not a command defined in",
		},
		{
			name:     "acceptance points at an environment command",
			toml:     roles + "\n[environment.commands]\ngo-toolchain = { command = \"which go\" }\n[evidence.acceptance]\nA1 = \"go-toolchain\"\n",
			wantPath: "evidence.acceptance.A1",
			wantMsg:  "not a command defined in",
		},
		{
			name:     "command id defined in two tables",
			toml:     roles + "\n[testing.commands]\ngo = { command = \"go test ./...\" }\n[environment.commands]\ngo = { command = \"which go\" }\n",
			wantPath: "environment.commands.go",
			wantMsg:  "already defined in testing.commands",
		},
		{
			name:     "evidence command reused as a testing command",
			toml:     roles + "\n[testing.commands]\nsmoke = { command = \"bash scripts/smoke.sh\" }\n[evidence.commands]\nlive = { command = \"bash scripts/smoke.sh\" }\n",
			wantPath: "evidence.commands.live",
			wantMsg:  "already authorised as testing.commands.smoke",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tc.toml))
			if err == nil {
				t.Fatalf("Parse succeeded, want a validation error")
			}
			if cfg != nil {
				t.Errorf("config = %v, want nil on a validation failure", cfg)
			}
			var errs ValidationErrors
			if !asValidationErrors(err, &errs) {
				t.Fatalf("error %T = %v, want ValidationErrors", err, err)
			}
			if !hasError(errs, tc.wantPath, tc.wantMsg) {
				t.Errorf("errors = %v, want one at %q containing %q", errs, tc.wantPath, tc.wantMsg)
			}
		})
	}
}

// TestAcceptanceMayCiteATestingCommand: close-out may run a testing command as
// evidence. Only the reverse is forbidden, and that is enforced by which table
// a command lives in, not by the acceptance mapping.
func TestAcceptanceMayCiteATestingCommand(t *testing.T) {
	toml := "\n[roles.implement]\nkind = \"claude\"\n\n[testing.commands]\ngo = { command = \"go test ./...\" }\n\n[evidence.acceptance]\nA1 = \"go\"\n"
	if _, err := Parse([]byte(toml)); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

func hasError(errs ValidationErrors, path, msg string) bool {
	for _, e := range errs {
		if e.Path == path && strings.Contains(e.Msg, msg) {
			return true
		}
	}
	return false
}

func asValidationErrors(err error, out *ValidationErrors) bool {
	errs, ok := err.(ValidationErrors)
	if ok {
		*out = errs
	}
	return ok
}
