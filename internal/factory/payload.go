package factory

import (
	ticfac "github.com/pengelbrecht/ticfac"
)

// The embedded payload — cloudflare (the Worker bundle) and image (the
// orchestrator image's build context, at image/ since SPEC §12 Phase 4 item 4)
// — lives in the module-root package,
// because //go:embed can only reference paths at or below its own package
// directory. This file is the one line of glue: it wires the module-root
// embed.FS trees into the seams bundle.go reads through (see the seam
// declaration there), so every staging mechanic — materialization, pruning,
// hashing, the in-place config rewrites — runs against the real payload.
//
// The seams stay variables rather than being replaced by direct calls, for two
// reasons. The tests stage a fake payload through the same code paths (and
// restore the real one afterwards by calling wireEmbeddedPayload, which is the
// same function this init runs); and keeping one assignment point is what
// makes the missing-payload stop in bundle.go an honest guard rather than a
// dead branch — an unwired seam still fails loudly.
func init() {
	wireEmbeddedPayload()
}

// wireEmbeddedPayload assigns the module-root embeds to the payload seams and
// resets the lazy caches that memoize over them (paths, sandbox paths, the
// bundle SHA), so whatever is wired is what the next read walks. A fresh
// sync.Once is the zero value, which is the reset.
func wireEmbeddedPayload() {
	factoryFS = ticfac.FactoryFS()
	sandboxFS = ticfac.SandboxFS()
	cloudProfilesFS = ticfac.CloudProfiles()
	resetPayloadCaches()
}
