// Package release holds the distribution contract (tick 4yb): the
// goreleaser config, install.sh and the release workflow that put ticfac on
// a machine that has never built it, and the tests that keep those files
// honest against each other.
//
// The three files are one contract wearing three shapes, which is exactly
// the failure mode to guard: the archive goreleaser cuts must be the archive
// install.sh asks for; the repository the release workflow publishes to must
// be the one install.sh downloads from; the platforms the config builds
// must be the platforms the script serves. Each of those pairings has a
// test, because a distribution that drifts between its halves is a release
// that installs nothing, discovered only on the machine of the person who
// tried.
package release

// (throwaway: probes affected-only PR CI; never merged)
