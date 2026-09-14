// Package ticfac holds the files the binary must be able to answer for without
// reading a working tree.
//
// The precedent is ticks' own embedded.go, and the reason is the same one
// contracts/README.md gives for embedding the tk manifest: a pin read off disk
// at runtime could disagree with the binary beside it. `ticfac version --json`
// reports the contract bundle this build was compiled against, so the answer
// has to travel inside the executable.
package ticfac

import "embed"

// PinJSON is contracts.pin.json: which ticks ref the vendored bundle came
// from, and which bundle version this build's code was written against.
//
//go:embed contracts.pin.json
var PinJSON []byte

// BundleJSON is the vendored contracts/bundle.json manifest.
//
//go:embed contracts/bundle.json
var BundleJSON []byte

// ProfilesFS is `profiles/`: the versioned role profiles a run is dispatched
// with. It travels inside the executable for the reason the pin does — a
// profile read off disk at run time could disagree with the binary beside it,
// and the profile is what every attempt record's provenance names.
//
//go:embed profiles
var ProfilesFS embed.FS

// JobProtocolJSON is contracts/job-protocol.json, the schemas the reconciler
// validates a role-result envelope against before it acts on one. Embedded for
// the same reason: a controller run outside this checkout has no contracts
// directory to read, and a validation that silently does not happen is the
// hole the envelope exists to close.
//
//go:embed contracts/job-protocol.json
var JobProtocolJSON []byte

// TkJSONManifestJSON is contracts/tk-json-manifest.json, the command surface
// internal/tk drives tk through. Embedded for the same reason as the pin and
// the job protocol: a shipped binary has no checkout beside it to read this
// from, and internal/contracts.RepoRoot's go.mod walk finds nothing above a
// bare install directory.
//
//go:embed contracts/tk-json-manifest.json
var TkJSONManifestJSON []byte

// SourcePinJSON is factory.pin.json: which ticks ref the orchestrator image
// builds its tk from (ARG TK_SOURCE_REF / ARG TK_VERSION in the staged
// cloud/sandbox Dockerfile). Embedded for the reason every pin here is — a
// pin read off disk at run time could disagree with the binary beside it —
// and for the reason it is a committed file at all: which ticks built a
// deployment's sandbox is a property of the deployment, reviewable in a
// diff. See internal/factory/sourceref.go.
//
//go:embed factory.pin.json
var SourcePinJSON []byte

// factoryFS holds the deployable factory worker (cloud/factory), so `ticfac
// factory deploy` installs the bundle that shipped with this exact ticfac
// build — the version pin in D16 ("upgrades ride the repo").
//
// The patterns are enumerated rather than "all:cloud/factory" on purpose: a
// wildcard would sweep in node_modules/, .wrangler/ and dist/ from a
// developer who ran pnpm install in that directory, and go:embed resolves at
// compile time, so the binary's size would depend on the build machine's
// state. Only what `wrangler deploy` reads is embedded — the vitest suite
// stays out.
//
// The lockfile and the workspace file are embedded alongside package.json
// because the deployed Worker has a runtime dependency (the Cloudflare
// Sandbox SDK): `ticfac factory deploy` installs the staged bundle before
// deploying it, and an install without the lockfile would resolve a
// different dependency tree than this build was tested with — which is the
// version pin, undone.
//
// It travels in this package (and moved repository with the Go code, ticks
// tick b3a) because //go:embed cannot reach across modules: the payload and
// the deploy code that ships it have to live in one module.
//
//go:embed cloud/factory/wrangler.toml cloud/factory/package.json cloud/factory/tsconfig.json cloud/factory/README.md
//go:embed cloud/factory/pnpm-lock.yaml cloud/factory/pnpm-workspace.yaml
//go:embed cloud/factory/src cloud/factory/migrations cloud/factory/scripts
var factoryFS embed.FS

// FactoryFS returns the embedded factory worker bundle. Paths inside it are
// rooted at "cloud/factory", e.g. "cloud/factory/wrangler.toml".
func FactoryFS() embed.FS {
	return factoryFS
}

// sandboxFS holds the sandbox image's build context (cloud/sandbox) — the
// container a cloud run boots, in either of its two roles: the orchestrator
// entrypoint, the per-tick worker entrypoint, and the common half both source.
// It ships in the binary for the same reason the worker bundle does:
// `ticfac factory deploy` builds and pushes this image into the operator's
// own registry, so the image a deployment runs is the one that shipped with
// this ticfac build.
//
// Enumerated, not wildcarded, matching the factory bundle above: the build
// context is exactly the Dockerfile and what it copies.
//
//go:embed cloud/sandbox/Dockerfile cloud/sandbox/entrypoint.sh cloud/sandbox/preflight.sh
//go:embed cloud/sandbox/worker.sh cloud/sandbox/common.sh
//go:embed cloud/sandbox/build.sh cloud/sandbox/README.md
//go:embed cloud/sandbox/required-tk-commands
var sandboxFS embed.FS

// SandboxFS returns the embedded orchestrator image context. Paths inside it
// are rooted at "cloud/sandbox", e.g. "cloud/sandbox/Dockerfile".
func SandboxFS() embed.FS {
	return sandboxFS
}
