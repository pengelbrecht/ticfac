package cli

// The README's command surface section (tick fi3). nwj put every command on
// one cobra tree wrapped by fang — styled help, --version, completion, man
// pages, all DERIVED from the tree — and the README, which predates the tree,
// said none of it: its only CLI prose was the Development targets. This test
// pins the section that folds the tree in, so the README cannot lag the
// tree the way it did again: a command added to newRootCommand fails here
// until the section names it.
import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheReadmeCarriesTheCommandSurface(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read the README: %v", err)
	}
	section := readmeSection(string(readme), "Command surface")
	if section == "" {
		t.Fatalf("README.md has no \"## Command surface\" section; the operator-facing tree " +
			"(nwj) is documented only by `ticfac --help`")
	}

	// Every visible command in the tree is named in the section: the table an
	// operator reads first must carry the whole tree, not the subset the
	// README happened to name before the section existed.
	tree := newRootCommand(io.Discard, io.Discard)
	for _, cmd := range tree.Commands() {
		if cmd.Hidden || cmd.Name() == "" {
			continue
		}
		if !strings.Contains(section, cmd.Name()) {
			t.Errorf("the README's command surface section does not name %q:\n%s", cmd.Name(), section)
		}
	}

	// The surfaces nwj ADDED are the ones the old README predates: they are
	// what makes the tree operator-facing rather than a parser detail.
	for _, surface := range []string{"--help", "--version", "completion", "man", "Exit codes"} {
		if !strings.Contains(section, surface) {
			t.Errorf("the README's command surface section does not mention %q:\n%s", surface, section)
		}
	}
}

// readmeSection extracts one `## <title>` section's body, up to the next
// `## ` heading. An empty string means the section is not there at all,
// which is the state this test exists to refuse.
func readmeSection(readme, title string) string {
	lines := strings.Split(readme, "\n")
	in := false
	var body []string
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			if in {
				break
			}
			in = strings.TrimSpace(strings.TrimPrefix(line, "##")) == title
			continue
		}
		if in {
			body = append(body, line)
		}
	}
	return strings.Join(body, "\n")
}
