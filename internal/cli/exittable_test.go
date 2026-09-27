package cli

// The exit table's other half of tick 8v3's acceptance: "the exit code table
// is documented and tested". ExitTable (exit.go) is the authority the codes
// and their names live in; this test pins the README's exit-codes section
// to it, the same way readme_test.go pins the README's command table to the
// tree — a code changed in the code, or a meaning changed in the README,
// fails here until both sides say the same thing.
import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// readmeExitCodes finds the exit-codes block inside the command-surface
// section and returns the text from its opening phrase to the section's end.
func readmeExitCodes(t *testing.T) string {
	t.Helper()
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
		t.Fatalf("README.md has no \"## Command surface\" section")
	}
	at := strings.Index(section, "Exit codes are the contract")
	if at < 0 {
		t.Fatalf("the README's command surface section lost its exit-codes paragraph")
	}
	return section[at:]
}

func TestTheReadmeCarriesTheExitTable(t *testing.T) {
	t.Parallel()

	codes := readmeExitCodes(t)
	for _, entry := range ExitTable {
		// The row, verbatim: code, name, meaning — one authority, printed.
		row := "| `" + strconv.Itoa(entry.Code) + "` | " + entry.Name + " | " + entry.Meaning + " |"
		if !strings.Contains(codes, row) {
			t.Errorf("the README's exit codes do not carry code %d verbatim:\nwant row: %s", entry.Code, row)
		}
	}

	// The documented exceptions are part of the contract: a reader who meets
	// `ticfac status`'s liveness exit with no note reads 1 as "failed" on a
	// command that did its work.
	if !strings.Contains(codes, "liveness") {
		t.Errorf("the README's exit codes do not document the status liveness exception")
	}
}

// TestTheExitTableIsOneCodePerName pins the shape the table promises: every
// code distinct, every name stable — the vocabulary an agent matches on.
func TestTheExitTableIsOneCodePerName(t *testing.T) {
	t.Parallel()

	seenCode := map[int]string{}
	seenName := map[string]int{}
	for _, entry := range ExitTable {
		if other, ok := seenCode[entry.Code]; ok {
			t.Errorf("codes %q and %q share %d — a collision must be deliberate, and then the entry's meaning names both meanings", other, entry.Name, entry.Code)
		}
		seenCode[entry.Code] = entry.Name
		if other, ok := seenName[entry.Name]; ok && other != entry.Code {
			t.Errorf("the name %q is used for both %d and %d", entry.Name, other, entry.Code)
		}
		seenName[entry.Name] = entry.Code
	}
	// The five classes the tick names are the table's, one code each.
	for _, want := range []string{"done", "failed", "usage", "held", "running"} {
		if _, ok := seenName[want]; !ok {
			t.Errorf("the exit table has no class %q — the tick's five outcomes must each have a code", want)
		}
	}
}
