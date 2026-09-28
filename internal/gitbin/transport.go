package gitbin

import (
	"os"
	"strings"
)

// TransportEnv bounds a git transport that has gone silent, for the same
// reason every runner here sets GIT_TERMINAL_PROMPT=0: a hang is worse than a
// refusal. Every git this repository starts that can reach a remote carries
// it (TestEveryGitThatCanReachARemoteIsBounded holds the line), and it lives
// here, beside the git binary itself, because gitbin is the one package every
// runner already imports.
//
// The prompt was bounded and the network was not, and the network is what
// has actually stopped runs — twice, once per transport:
//
//   - ssh (tick pul). A `git fetch` of epic/9pd held its ssh open for two and
//     a half hours while the reconciler waited inside it — alive, holding the
//     run, emitting nothing. ssh's defaults have no connect timeout and no
//     keepalive, so a connection that dies without a FIN is waited on
//     forever. Ten seconds to establish, and four missed fifteen-second
//     keepalives to conclude a live connection has gone.
//
//   - https (epic-6in, 2026-09-28). The orchestrator sat thirty minutes
//     inside its checkpoint push, `git remote-https origin` alive and silent,
//     until a person killed the helper by pid. The ssh bound never applied:
//     the origin was https, and curl, under git, waits on a dead connection
//     forever by default. GIT_HTTP_LOW_SPEED_LIMIT/TIME are git's own knobs
//     for curl's low-speed abort: below 1000 bytes a second for 60 seconds
//     and the request fails with curl 28, "Operation too slow" — which
//     runstate.ClassifyRemote reads as transient, so the run's bounded retry
//     and the supervisor's remote_transient resume answer it with nobody
//     watching.
//
// Why 1000 bytes/s over 60s: a transfer that is actually moving, to GitHub or
// anything like it, moves at hundreds of kilobytes a second or more, so the
// limit is three orders of magnitude below a working connection and only a
// dead one sits under it. The minute is the ssh bound's minute — one sentence
// about both transports, "about a minute of silence and git gives up" — and
// is long enough that a remote pausing to run its receive hooks or to count
// objects for a pack is not mistaken for a dead one: git's own server sends
// keepalives while it works, and a minute of nothing at all is not work.
//
// The CONNECT is not these variables' business, and git has no knob of its
// own for it (there is no http.connectTimeout); curl's default connect
// timeout of 300 seconds already bounds it, so a remote that never answers
// the SYN costs five minutes, not forever. The stall seen was a connection
// that was established and then went quiet, which is what the low-speed abort
// is for.
//
// The respect rule is per variable. An operator who has set GIT_SSH_COMMAND
// has said how to reach their remote; one who has set either low-speed
// variable has said how patient to be with it. Neither is overwritten, and
// setting one does not switch the other off. (A bound set only in git config,
// http.lowSpeedLimit, is overridden by these variables — git reads the
// environment above its config — and the operator's way to keep theirs is the
// environment, which is respected.)
func TransportEnv() []string {
	var env []string
	for _, bound := range transportBounds {
		if strings.TrimSpace(os.Getenv(bound.name)) != "" {
			continue
		}
		env = append(env, bound.name+"="+bound.value)
	}
	return env
}

var transportBounds = []struct{ name, value string }{
	{"GIT_SSH_COMMAND", "ssh -o ConnectTimeout=10 -o ServerAliveInterval=15 -o ServerAliveCountMax=4"},
	{"GIT_HTTP_LOW_SPEED_LIMIT", "1000"},
	{"GIT_HTTP_LOW_SPEED_TIME", "60"},
}
