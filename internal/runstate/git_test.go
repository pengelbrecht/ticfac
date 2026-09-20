package runstate

import (
	"strings"
	"testing"
)

// TestTheTransportIsBoundedUnlessTheOperatorBoundItThemselves.
//
// The prompt was bounded and the network was not, and the network is the one
// that stopped a run: a `git fetch` held its ssh open for two and a half hours
// while the reconciler waited inside it, alive and emitting nothing. ssh's
// defaults have no connect timeout and no keepalive, so a connection that dies
// without a FIN is waited on forever.
// short: TransportEnv() over one environment variable
func TestTheTransportIsBoundedUnlessTheOperatorBoundItThemselves(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "")
	env := TransportEnv()
	if len(env) != 1 {
		t.Fatalf("TransportEnv() = %v, want one GIT_SSH_COMMAND setting", env)
	}
	for _, want := range []string{"ConnectTimeout=", "ServerAliveInterval=", "ServerAliveCountMax="} {
		if !strings.Contains(env[0], want) {
			t.Errorf("%q does not set %s: a transport that can wait forever is a run that can stall forever",
				env[0], want)
		}
	}

	// An operator who has said how to reach their remote is not overruled.
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /custom/key")
	if env := TransportEnv(); env != nil {
		t.Errorf("TransportEnv() = %v with an operator's own GIT_SSH_COMMAND set, want none", env)
	}
}
