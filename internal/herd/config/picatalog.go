package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// PiCatalog is pi's own model catalog: the listing `pi --list-models` prints
// (provider, model, context, max-out, THINKING, images — 84 rows on the
// machine the format was verified on, 2026-09-10, pi 0.85.1), parsed to the
// one question a tier's model id must survive: does pi know this id, and does
// the model have the thinking dimension an effort level compiles into?
//
// It exists because the alternative — a shape check that asks only whether a
// slash appears somewhere in the id — passes every typo, and pi's own failure
// mode for a typo is the worst of the three CLIs observed: not opencode's
// silent substitution to a cheaper default, but not a clean failure either.
// Verified live (tick gjk, 2026-09-10):
//
//	pi -p --model cloudflare-workers-ai/@cf/zai-org/glm-9-imaginary "…"
//	  -> stdout: (empty)   stderr: Warning: … Using custom model id.   exit 0
//
// The id is passed THROUGH to the provider as a "custom model id", the turn
// returns nothing, and the process exits 0 — so any automation that reads exit
// codes believes it worked. The catalog is the only oracle that closes this at
// compile time; `pi auth check --model <nonsense>` reports ready because it
// validates the PROVIDER only, and is not one.
type PiCatalog struct {
	// thinking maps each catalog row's exact "<provider>/<model>" id to
	// whether its thinking column reads yes. The second value of the pair
	// is the model's effort dimension, and it is PER MODEL, not per kind:
	// several cloudflare models in the verified listing read no.
	thinking map[string]bool
}

// Lookup reports whether id — spelled exactly as the config names it,
// "provider/model" — is in the catalog, and whether the model's thinking
// column reads yes. Ids are matched exactly: the catalog is the authority, not
// a pattern-matcher, and an id that merely looks like a catalog entry is the
// typo this type exists to catch. A `:<effort>` suffix never matches — the
// config's model field must be bare; effort compiles the suffix.
func (c *PiCatalog) Lookup(id string) (inCatalog bool, thinking bool) {
	if c == nil {
		return false, false
	}
	thinking, ok := c.thinking[id]
	return ok, thinking
}

// Len reports how many models the catalog lists. A listing that parsed to zero
// rows is not a catalog, and ParsePiCatalog refuses to return one.
func (c *PiCatalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.thinking)
}

// catalogSpec is one kind's own model oracle, declared on its row in kinds.go
// so that every validation decision stays in the capability matrix. Today
// only pi ships one; a kind with no oracle leaves the field nil and keeps the
// green-start-trap arrangement herdr-kinds.md documents (a well-formed
// imaginary id is the first-round-trip gate's problem).
type catalogSpec struct {
	// command is the oracle as refusal messages name it, e.g.
	// "pi --list-models".
	command string
	// effortColumn is the catalog column that answers whether the model has
	// the dimension `effort` compiles into — pi's is "thinking". The column
	// is named in the refusal so the message points at the exact evidence,
	// the way the opencode precedent names the missing flag.
	effortColumn string
	// resolve runs the oracle and parses its listing. Consulted ONLY by a
	// compile that names a model for a kind with a catalog — lazily, so a
	// claude spawn never pays for it (the same rule the git common dir
	// resolver follows).
	resolve func(env SpawnContext) (*PiCatalog, error)
}

// piCatalog is the pi row's oracle declaration: how to run it and which column
// is the effort dimension. The behaviour of the thinking column's "no" — the
// suffix is silently accepted and ignored, verified live on
// @cf/meta/llama-3.3-70b-instruct-fp8-fast (tick gjk) — is exactly the class
// of error opencode's row already refuses: a tier that asks for a dimension
// the model has none of must be a refusal naming the tier, the model and the
// column, not a silent drop that makes economy-at-low and strong-at-high the
// same dispatch.
var piCatalog = &catalogSpec{
	command:      "pi --list-models",
	effortColumn: "thinking",
	resolve: func(env SpawnContext) (*PiCatalog, error) {
		if env.ResolvePiCatalog == nil {
			// The pi-absent decision, logged: refuse, never degrade to the
			// shape check. The catalog is a live command on the host that
			// will run the worker, and a host without pi cannot host a pi
			// worker anyway — the oracle names the worker's own CLI. The
			// caller that cannot wire a resolver is a caller that cannot
			// honour the config it was given, which is a stop.
			return nil, errors.New("the spawn context carries no pi catalog resolver (wire SpawnContext.ResolvePiCatalog)")
		}
		return env.ResolvePiCatalog()
	},
}

// validate enforces the catalog rung for one compile: the id must be in the
// kind's own catalog, and an effort level must name a model whose catalog
// column for that dimension reads yes. Every refusal names the role/tier
// (carried by label), the kind, the model — and, for the thinking refusal, the
// column, per the fail-closed rule herdr-kinds.md states.
func (s *catalogSpec) validate(w Worker, label string, env SpawnContext) error {
	cat, err := s.resolve(env)
	if err != nil {
		return &RefusalError{
			Label: label, Kind: w.Kind, Model: w.Model, Effort: w.Effort,
			Reason: RefusalCatalogUnavailable,
			Detail: fmt.Sprintf("kind = %q names model = %q and its own catalog could not be read (`%s`: %v)",
				w.Kind, w.Model, s.command, err),
			Fix: "run the oracle on the spawn host and wire it into the spawn context — the shape check alone passes every typo, and a typo'd pi model " +
				"costs a turn that returns empty at exit 0 (verified live, tick gjk)",
		}
	}
	if inCatalog, _ := cat.Lookup(w.Model); !inCatalog {
		return &RefusalError{
			Label: label, Kind: w.Kind, Model: w.Model, Effort: w.Effort,
			Reason: RefusalModelCatalog,
			Detail: fmt.Sprintf("kind = %q cannot run model = %q: `%s` lists no such model (the catalog is the oracle; `pi auth check` "+
				"validates the provider only and reports ready for an id it does not serve)",
				w.Kind, w.Model, s.command),
			Fix: "name the id exactly as `" + s.command + "` prints it, or choose a model from that listing",
		}
	}
	if w.Effort != "" {
		if _, thinking := cat.Lookup(w.Model); !thinking {
			return &RefusalError{
				Label: label, Kind: w.Kind, Model: w.Model, Effort: w.Effort,
				Reason: RefusalModelThinking,
				Detail: fmt.Sprintf("kind = %q cannot run effort = %q on model = %q: the `%s` column of `%s` reads no for this model, "+
					"so the level would be silently accepted and ignored (verified live, tick gjk) — the tier would not mean what it names",
					w.Kind, string(w.Effort), w.Model, s.effortColumn, s.command),
				Fix: "drop `effort` for this tier, or point it at a model whose `" + s.effortColumn + "` column reads yes",
			}
		}
	}
	return nil
}

// ParsePiCatalog parses the listing `pi --list-models` prints: a header row
// (provider, model, context, max-out, thinking, images) followed by one row
// per model, columns separated by runs of whitespace.
//
// It is strict on purpose. A line that is neither the header nor a row whose
// thinking column reads yes/no is a PARSE ERROR, not a skipped line: pi's
// output format is this type's only contract, and a future release that
// changes it must surface here as a loud refusal (see
// [RefusalCatalogUnavailable]) rather than as a catalog that silently lacks
// rows — the failure this package exists to prevent, turned on itself. An
// empty or header-only listing is refused for the same reason: a catalog with
// no models validates nothing.
func ParsePiCatalog(r io.Reader) (*PiCatalog, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read the pi catalog: %w", err)
	}
	out := &PiCatalog{thinking: map[string]bool{}}
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if isCatalogHeader(fields) {
			continue
		}
		if len(fields) < 6 {
			return nil, fmt.Errorf("pi catalog line %d has %d columns, want at least 6 (provider, model, context, max-out, thinking, images): %q",
				i+1, len(fields), line)
		}
		switch fields[4] {
		case "yes", "no":
		default:
			return nil, fmt.Errorf("pi catalog line %d: the thinking column reads %q, want yes or no: %q", i+1, fields[4], line)
		}
		out.thinking[fields[0]+"/"+fields[1]] = fields[4] == "yes"
	}
	if out.Len() == 0 {
		return nil, errors.New("the pi catalog listing parsed to no models — a listing with no rows validates nothing")
	}
	return out, nil
}

// isCatalogHeader recognises the listing's own header row so it can be
// skipped. The thinking-column position doubles as the check on the row shape:
// a header is a line whose fifth column says "thinking", which no data row
// can (data rows read yes or no there).
func isCatalogHeader(fields []string) bool {
	return len(fields) >= 5 && fields[0] == "provider" && fields[4] == "thinking"
}

// ReadPiCatalog runs pi's own catalog listing and parses it. It is what a
// spawner on the spawn host wires into [SpawnContext.ResolvePiCatalog]:
//
//	env := SpawnContext{ResolvePiCatalog: ReadPiCatalog}
//
// Errors here (pi absent from the PATH, a non-zero exit, output that will not
// parse) are the caller's to refuse on — see the pi-absent decision on
// [SpawnContext.ResolvePiCatalog] and [RefusalCatalogUnavailable].
func ReadPiCatalog() (*PiCatalog, error) {
	out, err := exec.Command("pi", "--list-models").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("pi --list-models: %w: %s", err, bytes.TrimSpace(ee.Stderr))
		}
		return nil, fmt.Errorf("pi --list-models: %w", err)
	}
	return ParsePiCatalog(bytes.NewReader(out))
}
