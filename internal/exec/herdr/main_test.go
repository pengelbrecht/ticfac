package herdr

import (
	"fmt"
	"os"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/tempdir"
)

// TestMain keeps every executor this package's tests build off the operator's
// real Claude Code config (tick o35): an executor built without an explicit
// ClaudeConfigPath resolves Claude Code's own location, and a claude launch
// registers folder trust there. CLAUDE_CONFIG_DIR points that resolution at a
// directory of the suite's own, so a test that forgets the option writes
// into a scratch file, never ~/.claude.json.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", tempdir.Pattern("ticfac-herdr-claude-config-"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	_ = os.Setenv("CLAUDE_CONFIG_DIR", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
