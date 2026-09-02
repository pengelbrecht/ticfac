// Package ticfac holds the files the binary must be able to answer for without
// reading a working tree.
//
// The precedent is ticks' own embedded.go, and the reason is the same one
// contracts/README.md gives for embedding the tk manifest: a pin read off disk
// at runtime could disagree with the binary beside it. `ticfac version --json`
// reports the contract bundle this build was compiled against, so the answer
// has to travel inside the executable.
package ticfac

import _ "embed"

// PinJSON is contracts.pin.json: which ticks ref the vendored bundle came
// from, and which bundle version this build's code was written against.
//
//go:embed contracts.pin.json
var PinJSON []byte

// BundleJSON is the vendored contracts/bundle.json manifest.
//
//go:embed contracts/bundle.json
var BundleJSON []byte
