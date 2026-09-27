package runconfig

import (
	"strings"
	"testing"
)

// The [findings] table: the repositories a run may file a routed finding into
// itself. A finding routed to another repository used to wait for a person and
// hold the close-out (the epic-2jn stall); this table is the explicit,
// reviewed authorisation that lets the run file it there instead.

func TestAFindingRouteIsReadFromTheFile(t *testing.T) {
	cfg, err := Parse([]byte(`version = 2

[roles.implement]
kind = "claude"

[findings.route."pengelbrecht/ticks"]
file = true

[findings.route."example/elsewhere"]
file = false
remote = "git@example.com:example/elsewhere.git"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	route, ok := cfg.FindingRoute("pengelbrecht/ticks")
	if !ok || !route.File || route.Remote != "" {
		t.Fatalf("route for pengelbrecht/ticks = %+v, %v; want file = true, remote derived", route, ok)
	}
	route, ok = cfg.FindingRoute("example/elsewhere")
	if !ok || route.File || route.Remote != "git@example.com:example/elsewhere.git" {
		t.Fatalf("route for example/elsewhere = %+v, %v; want file = false with its remote", route, ok)
	}
	if _, ok := cfg.FindingRoute("example/undeclared"); ok {
		t.Fatal("an undeclared target has a route: the allowlist must be explicit")
	}
	var nilCfg *Config
	if _, ok := nilCfg.FindingRoute("pengelbrecht/ticks"); ok {
		t.Fatal("a nil config answered a route")
	}
}

func TestAMalformedFindingRouteIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, route, want string
	}{
		{"a target that is not owner/name", "[findings.route.\"ticks\"]\nfile = true\n", "is not a repository a finding can be routed to"},
		{"a remote git would read as an option", "[findings.route.\"a/b\"]\nfile = true\nremote = \"--upload-pack=touch x\"\n", "must not begin with '-'"},
		{"an empty remote", "[findings.route.\"a/b\"]\nfile = true\nremote = \"\"\n", "must not be empty"},
		{"a remote with whitespace", "[findings.route.\"a/b\"]\nfile = true\nremote = \"git@x:a/b c\"\n", "whitespace"},
		{"a typo'd key", "[findings.route.\"a/b\"]\nfiel = true\n", "unknown key"},
		{"a file flag that is not a boolean", "[findings.route.\"a/b\"]\nfile = \"yes\"\n", "file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte("version = 2\n\n[roles.implement]\nkind = \"claude\"\n\n" + tc.route))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// ticfac's own runners.toml files findings routed to pengelbrecht/ticks: the
// operator owns both repositories, and a finding for ticks must never hold a
// ticfac run.
func TestThisRepositoryFilesFindingsIntoTicks(t *testing.T) {
	cfg, err := LoadRepoFor(repoRootForTest(t), SubstrateAuto)
	if err != nil {
		t.Fatal(err)
	}
	route, ok := cfg.FindingRoute("pengelbrecht/ticks")
	if !ok || !route.File {
		t.Fatalf("ticfac's .tick/runners.toml does not let a run file findings into pengelbrecht/ticks: %+v, %v", route, ok)
	}
}
