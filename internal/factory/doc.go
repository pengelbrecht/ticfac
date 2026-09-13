// Package factory deploys the ticks cloud factory into an operator's own
// Cloudflare account — moved here from ticks' internal/factory (ticfac
// Phase 2, ticks tick v3i, split into ek7/b3a/0e1).
//
// This package is the FOUNDATION half of that move (tick ek7): the shared
// plumbing the deploy and read paths build on — the embedded-bundle staging
// mechanics, the credential ladder's GitHub device flow and App identity, the
// factory's bearer-token auth, the Workers AI billing assertion, and the pin
// that says which ticks the orchestrator image builds its tk from. The deploy
// and read paths themselves (deploy.go, setup.go, status.go, rollout.go,
// migrate.go, the wrangler/docker drivers, the dashboard) land with ticks b3a
// and 0e1.
//
// Two decisions shape everything downstream and are recorded at their sites:
//
//   - The required-tk-commands gate (tkcommands.go): the fast preflight that
//     asked a caller-supplied predicate whether THIS binary implements every
//     entrypoint subcommand is GONE — ticfac does not ship tk, so there is no
//     cobra tree to ask, and shelling out to a `tk` on PATH would test a
//     different binary from the one the image actually runs. The check that
//     remains is the Dockerfile-embedded one, which runs against the tk that
//     will be in the image, at image build time.
//   - The source pin (factory.pin.json, read by sourceref.go): which ticks
//     ref the orchestrator image builds its tk from is a property of the
//     deployment, pinned in a reviewable repository file — not derived from
//     the deploying binary, not a per-operator credential, not a flag.
package factory
