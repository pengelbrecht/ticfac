package parity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// The compare-and-swap fake: a tiny in-memory model of the one git behaviour
// SPEC §4.2's idempotency rules depend on, described by
// contracts/ticfac-run-state.json's `cas.fake` and replayed by its
// `cas.sequences`.
//
// Origin is shared; each actor has its own fetched VIEW of it, which is the
// only thing its sha guard may use. That is the whole point: a static table
// cannot express "B's update is refused because A moved the ref after B
// fetched" — that is a sequence, and the guard is only observable as the
// difference between two orderings.
//
// ticks implements this fake independently (internal/factory/runstate), and so
// does the factory's TypeScript. This is ticfac's — the third, and the one
// that will sit under the real run-state store.

type casFake struct {
	// origin is the durable layer: path -> content, and path -> blob sha.
	origin    map[string]map[string]any
	originSHA map[string]string

	// views is each actor's fetched view of origin. Stale by construction:
	// nothing refreshes it but a fetch, an observe, or that actor's own
	// successful push.
	views map[string]map[string]string

	// local is a working tree: written by commit_local, and durable to nobody.
	local map[string]map[string]map[string]any

	originWrites int

	// guardOff disables the compare-and-swap. It exists for the negative
	// control: a CAS that has stopped guarding does not raise — it lets a
	// second reconciler dispatch the same attempt, and the run pays twice.
	guardOff bool
}

func newCASFake() *casFake {
	return &casFake{
		origin:    map[string]map[string]any{},
		originSHA: map[string]string{},
		views:     map[string]map[string]string{},
		local:     map[string]map[string]map[string]any{},
	}
}

func blobSHA(content map[string]any) string {
	raw, err := json.Marshal(content)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (f *casFake) view(actor string) map[string]string {
	v, ok := f.views[actor]
	if !ok {
		v = map[string]string{}
		f.views[actor] = v
	}
	return v
}

// push writes to origin and refreshes the WRITER's view of the path it wrote,
// and no other actor's.
func (f *casFake) push(actor, path string, content map[string]any) {
	f.origin[path] = content
	f.originSHA[path] = blobSHA(content)
	f.view(actor)[path] = f.originSHA[path]
	f.originWrites++
}

func (f *casFake) run(t *testing.T, s casStep) string {
	t.Helper()
	switch s.Op {
	case "fetch":
		fresh := map[string]string{}
		for path, sha := range f.originSHA {
			fresh[path] = sha
		}
		f.views[s.Actor] = fresh
		return "fetched"

	case "observe":
		// A poll that learns nothing: refreshes the view and writes nothing.
		fresh := map[string]string{}
		for path, sha := range f.originSHA {
			fresh[path] = sha
		}
		f.views[s.Actor] = fresh
		return "no_change"

	case "commit_local":
		tree, ok := f.local[s.Actor]
		if !ok {
			tree = map[string]map[string]any{}
			f.local[s.Actor] = tree
		}
		tree[s.Path] = s.Content
		// Origin is untouched. Durable means pushed.
		return "local_only"

	case "create_if_absent":
		if _, exists := f.origin[s.Path]; exists && !f.guardOff {
			// A refused push writes nothing: origin_writes does not move.
			return "conflict_exists"
		}
		f.push(s.Actor, s.Path, s.Content)
		return "created"

	case "update_if_sha":
		fetched, haveBase := f.view(s.Actor)[s.Path]
		if !f.guardOff {
			if !haveBase {
				// A writer that never read origin cannot compare against it.
				return "conflict_missing_base"
			}
			if f.originSHA[s.Path] != fetched {
				return "conflict_stale_sha"
			}
		}
		f.push(s.Actor, s.Path, s.Content)
		return "updated"

	default:
		t.Fatalf("the fixture uses op %q, which this fake does not implement", s.Op)
		return ""
	}
}
