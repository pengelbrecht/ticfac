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
