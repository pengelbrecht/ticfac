package factory

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	ticfac "github.com/pengelbrecht/ticfac"
)

// The orchestrator image builds its own tk from source, so a deploy has to
// answer one question first: which source? In ticks the answer was "the
// source this binary was built from" — a tk pinned its own image, so the
// ref and the bundle were the same code by construction. That answer is gone
// here: ticfac does not ship tk, and ticfac's own commit says nothing about
// which tk a sandbox should run.
//
// The answer is a PIN, decided at review time and version-controlled in the
// repository: factory.pin.json at the ticfac root (embedded into the binary
// like every other pin this module carries, for the same reason — a pin read
// off disk at run time could disagree with the binary beside it). Which
// ticks built a deployment's sandbox is a property of the DEPLOYMENT, so it
// lives in a file a reviewer sees in a diff; bumping it is a deliberate,
// visible change, not an operator's local setting (~/.ticfacrc) and not a
// flag nobody recorded.
//
// It is deliberately NOT contracts.pin.json's ref. That pin records which
// ticks commit the vendored contract bundle was fetched at and moves for
// contract reasons; this one moves for tk reasons — a subcommand the
// entrypoint has learned to run, a fix the sandbox must carry. Tying them
// together would make every contract refresh an image rebuild, or every
// image bump a contract change, and both directions are wrong.

// SourcePinFile is the pin file's name, at the repository root.
const SourcePinFile = "factory.pin.json"

// commitPattern is a full git object name. A short ref is deliberately not
// accepted: the image resolves this through the Go module proxy, and an
// abbreviated commit is not a module version. A branch or tag name is not
// accepted either — a moving ref pins nothing, the same rule
// contracts.pin.json applies to its own ref.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// SourcePin is factory.pin.json: which ticks the orchestrator image's tk is
// built from.
type SourcePin struct {
	// Comment is the file's `$comment` — the rationale above, in the file,
	// where the reviewer who bumps the pin reads it.
	Comment string `json:"$comment"`

	// Repository is the GitHub repository the pinned tk comes from,
	// "owner/name".
	Repository string `json:"repository"`

	// Module is the Go module path the image installs the pinned tk from
	// (the Dockerfile's ARG TK_MODULE).
	Module string `json:"module"`

	// Version is the label the image stamps the built tk with (the
	// Dockerfile's ARG TK_VERSION): what `tk version` reports inside the
	// container and the entrypoint verifies on PATH. "dev" for a build from
	// an unreleased commit, exactly as ticks' own dev deploys were labelled.
	Version string `json:"version"`

	// Ref is the full commit sha the image builds tk from (the Dockerfile's
	// ARG TK_SOURCE_REF).
	Ref string `json:"ref"`
}

// PinnedSource reads the embedded pin and proves it can be honoured before
// handing it to a deploy: a pin that names a moving ref, an abbreviated
// commit, an unusable version label, or a module from some other repository
// is a stop that names what is wrong, not a deploy that discovers it in a
// failed docker build twenty minutes later.
func PinnedSource() (SourcePin, error) {
	var pin SourcePin
	if err := json.Unmarshal(ticfac.SourcePinJSON, &pin); err != nil {
		return SourcePin{}, fmt.Errorf("%s does not parse: %w", SourcePinFile, err)
	}
	if err := pin.Validate(); err != nil {
		return SourcePin{}, err
	}
	return pin, nil
}

// Validate refuses a pin the image could not honour.
func (p SourcePin) Validate() error {
	if !commitPattern.MatchString(strings.TrimSpace(p.Ref)) {
		return fmt.Errorf("%s: ref %q is not a full commit sha — a moving ref pins nothing and an abbreviated commit is not a module version; pin the exact commit the image should build from",
			SourcePinFile, p.Ref)
	}
	if !validTkPin.MatchString(strings.TrimSpace(p.Version)) {
		return fmt.Errorf("%s: version %q is not a usable tk version label for the image", SourcePinFile, p.Version)
	}
	module := strings.TrimSpace(p.Module)
	if module == "" {
		return fmt.Errorf("%s: no Go module to install the pinned tk from", SourcePinFile)
	}
	if !strings.HasPrefix(module, "github.com/"+strings.TrimSpace(p.Repository)+"/") {
		return fmt.Errorf("%s: module %q does not come from repository %q", SourcePinFile, module, p.Repository)
	}
	return nil
}
