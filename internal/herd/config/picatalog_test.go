package config

import (
	"errors"
	"strings"
	"testing"
)

// fixturePiCatalog is a verbatim excerpt of the listing `pi --list-models`
// prints (pi 0.85.1, verified live 2026-09-10, tick gjk — the full listing
// printed 84 rows on that machine): provider, model, context, max-out,
// thinking, images. The rows are the real catalog rows for the ids the tests
// route to, including the per-model thinking variation this rung exists to
// see: glm-5.3 has the dimension, llama-3.3-70b does not.
const fixturePiCatalog = `provider               model                                         context  max-out  thinking  images
anthropic              claude-opus-4-6                               1M       128K     yes       yes
cloudflare-workers-ai  @cf/meta/llama-3.3-70b-instruct-fp8-fast      24K      24K      no        no
cloudflare-workers-ai  @cf/zai-org/glm-5.3                           1.3M     8.2K     yes       no
cloudflare-workers-ai  @cf/zai-org/glm-5.3-flash                     1.3M     8.2K     yes       yes
openai-codex           gpt-5.6-sol                                   272K     128K     yes       yes
`

// fixturePiCatalogResolver is what testEnv wires in: the fixture listing,
// parsed once. The production resolver is [ReadPiCatalog], which runs the real
// command on the spawn host.
var fixturePiCatalogResolver = func() func() (*PiCatalog, error) {
	cat, err := ParsePiCatalog(strings.NewReader(fixturePiCatalog))
	if err != nil {
		panic(err)
	}
	return func() (*PiCatalog, error) { return cat, nil }
}()

// piEnv is a spawn context with the fixture oracle and a counting resolver,
// for the laziness assertions below. It carries a git common dir too, so a
// codex control case in the same tests compiles; pi declares no git-metadata
// fragment, so the value is never consulted for pi.
func piEnv(count *int) SpawnContext {
	return SpawnContext{
		GitCommonDir: docGitCommonDir,
		ResolvePiCatalog: func() (*PiCatalog, error) {
			if count != nil {
				*count++
			}
			return fixturePiCatalogResolver()
		},
	}
}

// piSpawn compiles a minimal pi role table with the given model and effort.
func piSpawn(t *testing.T, model, effort string, env SpawnContext) (*Spawn, error) {
	t.Helper()
	toml := "[roles.implement]\nkind = \"pi\"\n"
	if model != "" {
		toml += "model = \"" + model + "\"\n"
	}
	if effort != "" {
		toml += "effort = \"" + effort + "\"\n"
	}
	cfg, err := Parse([]byte(toml))
	if err != nil {
		t.Fatalf("Parse: %v — a spawn-time refusal must be shape-valid first", err)
	}
	return cfg.SpawnFor(RoleImplement, "", env)
}

// --------------------------------------------------------------------------
// The parser: pi's own listing, parsed strictly.
// --------------------------------------------------------------------------

func TestParsePiCatalogReadsTheRealFormat(t *testing.T) {
	cat, err := ParsePiCatalog(strings.NewReader(fixturePiCatalog))
	if err != nil {
		t.Fatalf("ParsePiCatalog: %v", err)
	}
	if got := cat.Len(); got != 5 {
		t.Errorf("Len = %d, want 5", got)
	}
	for id, wantThinking := range map[string]bool{
		"cloudflare-workers-ai/@cf/zai-org/glm-5.3":                      true,
		"cloudflare-workers-ai/@cf/meta/llama-3.3-70b-instruct-fp8-fast": false,
		"openai-codex/gpt-5.6-sol":                                       true,
	} {
		inCatalog, thinking := cat.Lookup(id)
		if !inCatalog {
			t.Errorf("Lookup(%q) = not in catalog, want in", id)
			continue
		}
		if thinking != wantThinking {
			t.Errorf("Lookup(%q) thinking = %v, want %v", id, thinking, wantThinking)
		}
	}
	// The typo the whole rung exists to catch: a provider-qualified id the
	// catalog does not list is "not in catalog", not a guess.
	if inCatalog, _ := cat.Lookup("cloudflare-workers-ai/@cf/zai-org/glm-9-imaginary"); inCatalog {
		t.Error("Lookup of a typo'd id reported it in catalog")
	}
	// An id carrying pi's model:thinking shorthand never matches: effort
	// compiles the suffix, so the model field must be bare.
	if inCatalog, _ := cat.Lookup("openai-codex/gpt-5.6-sol:high"); inCatalog {
		t.Error("Lookup of a :suffixed id reported it in catalog")
	}
}

// A listing that parses to no rows validates nothing, so it is an error, not
// an empty catalog. The same strictness applies line by line: a format change
// in pi's output must surface here as a loud parse error, never as a catalog
// that silently lacks rows.
func TestParsePiCatalogRefusesDegenerateListings(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "no models"},
		{name: "header only", in: "provider               model     context  max-out  thinking  images\n", want: "no models"},
		{
			name: "a row with a column missing",
			in:   "provider               model     context  max-out  thinking  images\nopenai-codex           gpt-5.6-sol     272K     128K     yes\n",
			want: "want at least 6",
		},
		{
			name: "a thinking column that reads neither yes nor no",
			in:   "provider               model     context  max-out  thinking  images\nopenai-codex           gpt-5.6-sol     272K     128K     maybe     yes\n",
			want: "the thinking column reads \"maybe\", want yes or no",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePiCatalog(strings.NewReader(tc.in))
			if err == nil {
				t.Fatalf("ParsePiCatalog accepted a degenerate listing")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}

// --------------------------------------------------------------------------
// The catalog rung, through Compile — the four cases the tick names.
// --------------------------------------------------------------------------

// Case 1 — a good pi tier: the id is in the catalog, the model has the
// thinking dimension, and the compile carries the verified full-auto template
// with the effort riding the model id as pi's suffix.
func TestAGoodPiTierCompiles(t *testing.T) {
	sp, err := piSpawn(t, "cloudflare-workers-ai/@cf/zai-org/glm-5.3", "high", piEnv(nil))
	if err != nil {
		t.Fatalf("SpawnFor: %v", err)
	}
	want := []string{"--approve", "--model", "cloudflare-workers-ai/@cf/zai-org/glm-5.3:high"}
	if !equalArgs(sp.Argv, want) {
		t.Errorf("Argv  = %q\n want = %q", sp.Argv, want)
	}
	if len(sp.Warnings) != 0 {
		t.Errorf("Warnings = %q, want none", sp.Warnings)
	}
}

// Case 2 — a typo'd model: provider-qualified (so the shape check passes,
// exactly the gap the tick names: every typo used to pass) but in no catalog
// pi prints. Refused at compile time, naming the tier, the model and the
// oracle — gjk's evidence for what this catches: pi passes an unknown id
// through as a "custom model id", warns on stderr, returns an EMPTY turn and
// exits 0.
func TestATypoModelIsRefusedAtCompileTime(t *testing.T) {
	sp, err := piSpawn(t, "cloudflare-workers-ai/@cf/zai-org/glm-9-imaginary", "high", piEnv(nil))
	if err == nil {
		t.Fatalf("SpawnFor succeeded with argv %q, want a refusal", sp.Argv)
	}
	if sp != nil {
		t.Error("a refusal must return no spawn")
	}
	var ref *RefusalError
	if !errors.As(err, &ref) {
		t.Fatalf("error is %T, want *RefusalError", err)
	}
	if ref.Reason != RefusalModelCatalog {
		t.Errorf("Reason = %q, want %q", ref.Reason, RefusalModelCatalog)
	}
	msg := err.Error()
	for _, want := range []string{
		FileName,
		"[roles.implement]", // the tier
		`"cloudflare-workers-ai/@cf/zai-org/glm-9-imaginary"`, // the model
		"pi --list-models", // the oracle
		"lists no such model",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not contain %q:\n  %s", want, msg)
		}
	}
}

// Case 3 — a bare id that names no provider: refused by the first rung, the
// family check, before the oracle is even consulted (asserted by the resolver
// call count).
func TestABareIdIsRefusedBeforeTheOracleIsConsulted(t *testing.T) {
	calls := 0
	sp, err := piSpawn(t, "glm-5.3", "high", piEnv(&calls))
	if err == nil {
		t.Fatalf("SpawnFor succeeded with argv %q, want a refusal", sp.Argv)
	}
	var ref *RefusalError
	if !errors.As(err, &ref) {
		t.Fatalf("error is %T, want *RefusalError", err)
	}
	if ref.Reason != RefusalModelFamily {
		t.Errorf("Reason = %q, want %q", ref.Reason, RefusalModelFamily)
	}
	if calls != 0 {
		t.Errorf("the oracle ran %d time(s); a bare id is refused before it is reached", calls)
	}
	for _, want := range []string{`"pi"`, `"glm-5.3"`, "provider-qualified"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message does not contain %q:\n  %s", want, err)
		}
	}
}

// Case 4 — an effort level on a model whose catalog thinking column reads no:
// refused naming the tier, the model AND the column, matching the opencode
// precedent that a missing effort dimension is a refusal, not a silent drop.
// gjk verified the live behaviour this pins: the suffix is silently accepted,
// the run works, and the level is ignored — so economy-at-low and
// strong-at-high would be the same dispatch, which is a tier that does not
// mean what it names.
func TestEffortOnANonThinkingModelIsRefusedNamingTheColumn(t *testing.T) {
	sp, err := piSpawn(t, "cloudflare-workers-ai/@cf/meta/llama-3.3-70b-instruct-fp8-fast", "high", piEnv(nil))
	if err == nil {
		t.Fatalf("SpawnFor succeeded with argv %q, want a refusal", sp.Argv)
	}
	if sp != nil {
		t.Error("a refusal must return no spawn — never an argv with the level silently dropped")
	}
	var ref *RefusalError
	if !errors.As(err, &ref) {
		t.Fatalf("error is %T, want *RefusalError", err)
	}
	if ref.Reason != RefusalModelThinking {
		t.Errorf("Reason = %q, want %q", ref.Reason, RefusalModelThinking)
	}
	msg := err.Error()
	for _, want := range []string{
		FileName,
		"[roles.implement]", // the tier
		`"cloudflare-workers-ai/@cf/meta/llama-3.3-70b-instruct-fp8-fast"`, // the model
		"the `thinking` column", // the column
		"reads no",
		"effort = \"high\"",
		"silently accepted and ignored",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not contain %q:\n  %s", want, msg)
		}
	}
}

// The same model without an effort level compiles: the dimension is only a
// problem when a tier asks for one. Omitting effort means the model's own
// default, exactly as it does for the model field.
func TestANonThinkingModelWithoutEffortCompiles(t *testing.T) {
	sp, err := piSpawn(t, "cloudflare-workers-ai/@cf/meta/llama-3.3-70b-instruct-fp8-fast", "", piEnv(nil))
	if err != nil {
		t.Fatalf("SpawnFor: %v", err)
	}
	if !equalArgs(sp.Argv, []string{"--approve", "--model", "cloudflare-workers-ai/@cf/meta/llama-3.3-70b-instruct-fp8-fast"}) {
		t.Errorf("Argv = %q, want the model without a suffix", sp.Argv)
	}
}

// --------------------------------------------------------------------------
// The pi-absent decision, logged: refuse, never degrade to the shape check.
// --------------------------------------------------------------------------

// A spawn context with no oracle resolver is the "pi absent" case, and the
// logged decision is a refusal: the catalog is a live command on the host
// that will run the worker, and a host that cannot run the oracle cannot host
// the worker either — the oracle names the worker's own CLI. Degrading to the
// shape check would reintroduce exactly the failure gjk measured: every typo
// passes, and a typo'd pi model costs a turn that returns empty at exit 0.
func TestPiWithoutItsCatalogIsRefused(t *testing.T) {
	sp, err := piSpawn(t, "cloudflare-workers-ai/@cf/zai-org/glm-5.3", "high", SpawnContext{})
	if err == nil {
		t.Fatalf("SpawnFor succeeded with argv %q, want a refusal", sp.Argv)
	}
	var ref *RefusalError
	if !errors.As(err, &ref) {
		t.Fatalf("error is %T, want *RefusalError", err)
	}
	if ref.Reason != RefusalCatalogUnavailable {
		t.Errorf("Reason = %q, want %q", ref.Reason, RefusalCatalogUnavailable)
	}
	msg := err.Error()
	for _, want := range []string{
		FileName, "[roles.implement]", `"pi"`, `"cloudflare-workers-ai/@cf/zai-org/glm-5.3"`,
		"could not be read", "pi --list-models", "no pi catalog resolver",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not contain %q:\n  %s", want, msg)
		}
	}
}

// A resolver that errors is the same refusal — the oracle ran and failed
// (pi on the PATH but the listing errored, or output that would not parse),
// and the failure mode is identical: nothing is silently degraded.
func TestAFailingOracleIsRefusedWithItsOwnReason(t *testing.T) {
	env := SpawnContext{ResolvePiCatalog: func() (*PiCatalog, error) {
		return nil, errors.New("pi: command not found")
	}}
	sp, err := piSpawn(t, "cloudflare-workers-ai/@cf/zai-org/glm-5.3", "high", env)
	if err == nil {
		t.Fatalf("SpawnFor succeeded with argv %q, want a refusal", sp.Argv)
	}
	var ref *RefusalError
	if !errors.As(err, &ref) {
		t.Fatalf("error is %T, want *RefusalError", err)
	}
	if ref.Reason != RefusalCatalogUnavailable {
		t.Errorf("Reason = %q, want %q", ref.Reason, RefusalCatalogUnavailable)
	}
	if !strings.Contains(err.Error(), "pi: command not found") {
		t.Errorf("message drops the oracle's own reason:\n  %s", err)
	}
}

// --------------------------------------------------------------------------
// Laziness: the oracle is paid only by the compile that needs it.
// --------------------------------------------------------------------------

// The resolver costs a `pi --list-models` on the spawn host, so — exactly like
// the git common dir — it is resolved only by a kind and only in the compile
// whose validation needs it. claude and codex ship no oracle, and a pi spawn
// that names no model has no id to validate.
func TestTheOracleIsResolvedOnlyWhenAModelIsNamed(t *testing.T) {
	for _, tc := range []struct {
		name string
		toml string
		env  SpawnContext
	}{
		{
			name: "claude names a model but ships no oracle",
			toml: "[roles.implement]\nkind = \"claude\"\nmodel = \"sonnet\"\neffort = \"high\"\n",
			env:  piEnv(nil),
		},
		{
			name: "codex names a model but ships no oracle",
			toml: codexImplement,
			env:  piEnv(nil),
		},
		{
			name: "pi names no model, so there is no id to validate",
			toml: "[roles.implement]\nkind = \"pi\"\neffort = \"high\"\n",
			env: SpawnContext{ResolvePiCatalog: func() (*PiCatalog, error) {
				t.Fatal("the oracle ran for a spawn with no model to validate")
				return nil, nil
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tc.toml))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			sp, err := cfg.SpawnFor(RoleImplement, "", tc.env)
			if err != nil {
				t.Fatalf("SpawnFor: %v", err)
			}
			if sp == nil {
				t.Fatal("SpawnFor returned no spawn and no error")
			}
		})
	}
}

// A pi spawn that names a model resolves the oracle exactly once.
func TestTheOracleIsResolvedExactlyOnceForAPiModel(t *testing.T) {
	calls := 0
	if _, err := piSpawn(t, "openai-codex/gpt-5.6-sol", "high", piEnv(&calls)); err != nil {
		t.Fatalf("SpawnFor: %v", err)
	}
	if calls != 1 {
		t.Errorf("the oracle was resolved %d time(s), want exactly 1", calls)
	}
}

// ReadPiCatalog is the production resolver; the machine this was written on
// has pi 0.85.1, and the real listing must parse. The test is skipped when pi
// is absent, which is exactly the state the refusal above documents: this
// host could not then host a pi worker.
func TestReadPiCatalogParsesTheRealListing(t *testing.T) {
	if _, err := ReadPiCatalog(); err != nil {
		if strings.Contains(err.Error(), "command not found") || strings.Contains(err.Error(), "executable file not found") {
			t.Skipf("pi is not on this host's PATH: %v", err)
		}
		t.Fatalf("ReadPiCatalog: %v", err)
	}
}
