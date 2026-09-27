package release

// The distribution contract's three files, pinned against each other (tick
// 4yb). These tests are the cheap half — readers and drift guards, no build,
// no subprocess — so the gate pays for them on every tick. The expensive
// half, the one that actually installs from a fake forge and runs the
// installed ticfac, is install_test.go's load-bearing test.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/contracts"
)

// readDistFile reads one of the distribution files from the repository root,
// refusing with the file's own name rather than an os error nobody can act on.
func readDistFile(t *testing.T, name string) string {
	t.Helper()
	root, err := contracts.RepoRoot()
	if err != nil {
		t.Fatalf("find the repository root: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// indentOf counts a YAML line's leading spaces.
func indentOf(line string) int {
	for i, r := range line {
		if r != ' ' {
			return i
		}
	}
	return len(line)
}

// blocksIn returns every block nested under a `key:` line: the lines that
// follow it, more indented, up to the first line back at the key's own
// indent. Blank lines do not end a block.
//
// This is deliberately not a YAML parser. goreleaser's config is one small
// hand-written file with a fixed shape, and what these tests owe is that the
// matrix and the names cannot drift — not that arbitrary YAML is valid.
func blocksIn(lines []string, key string) [][]string {
	var out [][]string
	for i, line := range lines {
		if strings.TrimSpace(line) != key+":" {
			continue
		}
		indent := indentOf(line)
		var block []string
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "" {
				continue
			}
			if indentOf(lines[j]) <= indent {
				break
			}
			block = append(block, lines[j])
		}
		out = append(out, block)
	}
	return out
}

// yamlBlocks is blocksIn over a whole file.
func yamlBlocks(src, key string) [][]string {
	return blocksIn(strings.Split(src, "\n"), key)
}

// scalarIn reads `key: value` off a block's lines, quotes stripped. A
// `- ` list prefix is tolerated so the first line of a `builds:` entry
// (`- id: ticfac`) reads like the rest.
func scalarIn(lines []string, key string) (string, bool) {
	for _, line := range lines {
		trimmed := strings.TrimPrefix(strings.TrimSpace(line), "- ")
		rest, ok := strings.CutPrefix(trimmed, key+":")
		if !ok || !strings.HasPrefix(rest, " ") {
			continue
		}
		return strings.Trim(strings.TrimSpace(rest), `"'`), true
	}
	return "", false
}

// yamlScalar is scalarIn over a whole file.
func yamlScalar(src, key string) (string, bool) {
	return scalarIn(strings.Split(src, "\n"), key)
}

// listEntries splits a `builds:` block into its entries, keeping each
// entry's lines at their ORIGINAL indentation (the nested readers below
// need it). An entry starts at every `- ` line at the list's own minimum
// indent — deeper `- ` lines (env vars, goos items) belong to the entry they
// sit under, and must not split it.
func listEntries(block []string) [][]string {
	entryIndent := -1
	for _, line := range block {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			if entryIndent < 0 || indentOf(line) < entryIndent {
				entryIndent = indentOf(line)
			}
		}
	}
	if entryIndent < 0 {
		return nil
	}
	var out [][]string
	for _, line := range block {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") && indentOf(line) == entryIndent {
			out = append(out, []string{line})
			continue
		}
		if len(out) > 0 {
			out[len(out)-1] = append(out[len(out)-1], line)
		}
	}
	return out
}

// listItems reads the `- item` lines under a block's nested `key:`.
func listItems(block []string, key string) []string {
	var items []string
	for _, nested := range blocksIn(block, key) {
		for _, line := range nested {
			if item, ok := strings.CutPrefix(strings.TrimSpace(line), "- "); ok {
				items = append(items, strings.TrimSpace(item))
			}
		}
	}
	return items
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// TestTheGoreleaserConfigShipsEveryPlatform pins the release matrix: BOTH
// binaries for every platform install.sh serves — darwin and linux, amd64 and
// arm64 — so a platform dropped from the config is a platform the script
// starts failing to serve, caught here rather than on the machine of the
// person who tried to install.
//
// short: reads two small files and asserts the matrix; no build, no subprocess
func TestTheGoreleaserConfigShipsEveryPlatform(t *testing.T) {
	src := readDistFile(t, ".goreleaser.yaml")

	if project, ok := yamlScalar(src, "project_name"); !ok || project != "ticfac" {
		t.Errorf("project_name is %q — the archives, the checksums and install.sh all name the project", project)
	}

	// The distribution pair: ticfac refuses to run without
	// ticfac-exec-subprocess beside it (internal/reconcile's supervisorArgv),
	// so a release that ships only the one binary is a release a run
	// refuses to start with.
	builds := yamlBlocks(src, "builds")
	if len(builds) != 1 {
		t.Fatalf("`builds:` has %d blocks, want 1 — the config's shape is not what these tests read", len(builds))
	}
	entries := listEntries(builds[0])
	if len(entries) != 2 {
		t.Fatalf("`builds:` has %d entries, want exactly the distribution pair (ticfac, ticfac-exec-subprocess)", len(entries))
	}
	for _, entry := range entries {
		id, ok := scalarIn(entry, "id")
		if !ok {
			t.Errorf("a build entry carries no id:\n%s", strings.Join(entry, "\n"))
			continue
		}
		switch id {
		case "ticfac":
			if main, _ := scalarIn(entry, "main"); main != "./cmd/ticfac" {
				t.Errorf("build %q has main %q, want ./cmd/ticfac", id, main)
			}
			if binary, _ := scalarIn(entry, "binary"); binary != "ticfac" {
				t.Errorf("build %q has binary %q, want ticfac", id, binary)
			}
		case "ticfac-exec-subprocess":
			if main, _ := scalarIn(entry, "main"); main != "./cmd/ticfac-exec-subprocess" {
				t.Errorf("build %q has main %q, want ./cmd/ticfac-exec-subprocess", id, main)
			}
			if binary, _ := scalarIn(entry, "binary"); binary != "ticfac-exec-subprocess" {
				t.Errorf("build %q has binary %q, want ticfac-exec-subprocess", id, binary)
			}
		default:
			t.Errorf("build %q is neither half of the distribution pair", id)
			continue
		}
		goos := listItems(entry, "goos")
		for _, platform := range []string{"darwin", "linux"} {
			if !contains(goos, platform) {
				t.Errorf("build %q does not list goos %q (has %v) — install.sh serves it, so the release must cut it", id, platform, goos)
			}
		}
		if contains(goos, "windows") {
			t.Errorf("build %q lists goos windows — the tick's matrix is darwin and linux, and a build nobody serves is a release that fails late", id)
		}
		goarch := listItems(entry, "goarch")
		for _, arch := range []string{"amd64", "arm64"} {
			if !contains(goarch, arch) {
				t.Errorf("build %q does not list goarch %q (has %v)", id, arch, goarch)
			}
		}
	}

	// The version flag is stamped into the variable `ticfac version`
	// actually reads, so the installed binary reports the release it came
	// from. The symbol is pinned by reading the package too, because an
	// ldflags path that names a variable nobody renamed is a release that
	// quietly says "dev" forever.
	cliSrc := readDistFile(t, filepath.Join("internal", "cli", "cli.go"))
	if !strings.Contains(cliSrc, "var Version =") {
		t.Errorf("internal/cli/cli.go no longer declares `var Version` — the ldflags below stamp it, and a moved variable is a silent \"dev\"")
	}
	ldflag := "-X github.com/pengelbrecht/ticfac/internal/cli.Version={{.Version}}"
	if !strings.Contains(src, ldflag) {
		t.Errorf(".goreleaser.yaml does not carry %q — the release binary would report the default build, not the tag", ldflag)
	}

	// ONE archive per platform, carrying BOTH binaries — by goreleaser's
	// default, since every built binary goes into the archive unless a
	// filter says otherwise, and a `builds:` filter is deprecated (CI's
	// `goreleaser check` refuses the config while one is there). So the
	// guard here is the negative one: the archive must NOT filter builds,
	// and the builds it would then drop are the pair the matrix test just
	// pinned — an archive without the executor is an install a run refuses
	// to start.
	archives := yamlBlocks(src, "archives")
	if len(archives) != 1 {
		t.Fatalf("`archives:` has %d blocks, want 1", len(archives))
	}
	if _, filtered := scalarIn(archives[0], "builds"); filtered {
		t.Errorf("the archive carries a `builds:` filter — it is deprecated, and if it named anything but both binaries the install would lack the pair a run needs\n%s", strings.Join(archives[0], "\n"))
	}
	formats := listItems(archives[0], "formats")
	if !contains(formats, "tar.gz") {
		t.Errorf("the archive's formats are %v, want tar.gz — install.sh untars what it downloads", formats)
	}
	if name, ok := scalarIn(archives[0], "name_template"); !ok ||
		name != "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}" {
		t.Errorf("the archive's name_template is %q — install.sh constructs the download URL from it, and the two must not drift", name)
	}

	// Checksums, so a mirror can be verified.
	if sum := yamlBlocks(src, "checksum"); len(sum) != 1 {
		t.Errorf("`checksum:` has %d blocks, want 1 (checksums.txt, sha256)", len(sum))
	} else {
		if name, _ := scalarIn(sum[0], "name_template"); name != "checksums.txt" {
			t.Errorf("the checksum file is %q, want checksums.txt", name)
		}
		if algo, _ := scalarIn(sum[0], "algorithm"); algo != "sha256" {
			t.Errorf("the checksum algorithm is %q, want sha256", algo)
		}
	}

	// Release notes, generated from the commit log rather than maintained
	// beside it — the same recipe ticks releases with.
	changelog := yamlBlocks(src, "changelog")
	if len(changelog) != 1 {
		t.Errorf("`changelog:` has %d blocks, want 1 — release notes are part of the distribution", len(changelog))
	} else if sort, _ := scalarIn(changelog[0], "sort"); sort != "asc" {
		t.Errorf("the changelog sort is %q, want asc", sort)
	}
}

// TestTheReleaseAndInstallScriptNameTheSameRepository pins the pairing
// between where goreleaser publishes and where install.sh downloads from: the
// script's REPO is derived from the config's release owner and name, so a
// repository move is caught by the guard rather than by every fresh install
// 404ing.
//
// short: reads two small files and compares two strings; no build, no subprocess
func TestTheReleaseAndInstallScriptNameTheSameRepository(t *testing.T) {
	src := readDistFile(t, ".goreleaser.yaml")
	script := readDistFile(t, "install.sh")

	owner, ok := yamlScalar(src, "owner")
	if !ok {
		t.Fatal(".goreleaser.yaml's release block does not carry `owner:`")
	}
	name, ok := yamlScalar(src, "name")
	if !ok {
		t.Fatal(".goreleaser.yaml's release block does not carry `name:`")
	}
	slug := owner + "/" + name
	if !strings.Contains(script, "REPO=\""+slug+"\"") {
		t.Errorf("install.sh does not name REPO=%q — the config publishes to that repository, and the script must download from the same one", slug)
	}
}

// TestTheInstallScriptRequestsTheArchiveGoreleaserBuilds pins the one
// pairing a broken release would hide: the archive name goreleaser's
// name_template cuts is the name install.sh requests. The template is
// rendered into the script's own shell form ($-variables), so editing either
// side alone fails here — and install_test.go's fake forge serves the
// rendered name for the real download path.
//
// short: renders one template and greps one script; no build, no subprocess
func TestTheInstallScriptRequestsTheArchiveGoreleaserBuilds(t *testing.T) {
	src := readDistFile(t, ".goreleaser.yaml")
	script := readDistFile(t, "install.sh")

	archives := yamlBlocks(src, "archives")
	if len(archives) != 1 {
		t.Fatal("`archives:` has no block to read the name_template from")
	}
	template, ok := scalarIn(archives[0], "name_template")
	if !ok {
		t.Fatal("the archive block carries no name_template")
	}
	project, _ := yamlScalar(src, "project_name")
	rendered := strings.NewReplacer(
		"{{ .ProjectName }}", project,
		"{{ .Version }}", "${VERSION}",
		"{{ .Os }}", "${OS}",
		"{{ .Arch }}", "${ARCH}",
	).Replace(template)
	if rendered == template {
		t.Fatalf("the name_template %q does not name the four fields the script substitutes", template)
	}
	if !strings.Contains(script, rendered+".tar.gz") {
		t.Errorf("install.sh does not request %q — goreleaser's name_template cuts that archive, and the two must agree", rendered+".tar.gz")
	}
}

// TestTheReleaseWorkflowCutsABinariesReleaseOnTags pins the workflow that
// makes "a tagged release produces all platform binaries" true: tags run
// goreleaser with the pinned config, over the full history the changelog
// reads, with the permission to publish.
//
// short: reads one small workflow file; no build, no subprocess
func TestTheReleaseWorkflowCutsABinariesReleaseOnTags(t *testing.T) {
	wf := readDistFile(t, filepath.Join(".github", "workflows", "release.yml"))

	for _, want := range []string{
		`tags:`,
		`- "v*"`,
		`goreleaser/goreleaser-action`,
		`release --clean`,
		`contents: write`,
		`fetch-depth: 0`,
	} {
		if !strings.Contains(wf, want) {
			t.Errorf(".github/workflows/release.yml does not carry %q — without it a tagged release does not cut the binaries", want)
		}
	}
}

// TestTheReadmeNamesTheOneCommandInstall pins the README's install section:
// the stable URL and the one command, both binaries, doctor as the first
// check — the README cannot lag the distribution the way it once lagged the
// command tree.
//
// short: reads the README and greps one section; no build, no subprocess
func TestTheReadmeNamesTheOneCommandInstall(t *testing.T) {
	section := readmeSection(readDistFile(t, "README.md"), "Install")
	if section == "" {
		t.Fatal("README.md has no \"## Install\" section — the one-command install (tick 4yb) is documented nowhere")
	}
	for _, want := range []string{
		"curl -fsSL https://raw.githubusercontent.com/pengelbrecht/ticfac/main/install.sh | sh",
		"ticfac-exec-subprocess",
		"ticfac doctor",
		"goreleaser",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the README's Install section does not mention %q:\n%s", want, section)
		}
	}
}

// TestTheMakefileCutsTheTagTheReleaseWorkflowBuilds pins the one command
// that turns a merge into an installable release. The tag IS the release:
// nothing but a pushed v* tag triggers .github/workflows/release.yml (the
// workflow test above pins that), and until the FIRST tag exists the stable
// install URL answers nothing — releases/latest has no release to resolve
// and install.sh aborts — so the first cut is part of the first merge to
// main, not a remembered procedure performed by hand afterwards (tick 5o5).
// The target is what makes it one command: it refuses a VERSION that is
// missing or does not start with v — a tag the workflow does not listen
// for is a release nobody can install — and it pushes the annotated tag
// the workflow builds from.
//
// short: reads the Makefile and greps one target; no build, no subprocess
func TestTheMakefileCutsTheTagTheReleaseWorkflowBuilds(t *testing.T) {
	recipe := makefileRecipe(t, "release")
	if recipe == "" {
		t.Fatal("the Makefile has no `release` target — cutting a release is `make release VERSION=vX.Y.Z`, and without the target it is a remembered procedure again (tick 5o5)")
	}
	for _, want := range []string{
		`test -n "$(VERSION)"`,
		`case "$(VERSION)" in v*)`,
		`git tag -a "$(VERSION)"`,
		`git push origin "$(VERSION)"`,
	} {
		if !strings.Contains(recipe, want) {
			t.Errorf("the Makefile's release target does not carry %q:\n%s", want, recipe)
		}
	}
}

// TestTheReadmeNamesHowTheFirstReleaseIsCut pins the other half of the
// stable install URL's story: the URL answers nothing until main carries
// install.sh AND a v* tag exists, and the README must say so — that cutting
// a release is one command, and that the FIRST tag is what makes the
// one-command install reachable at all. A README that documents the
// install and not the cut leaves the first release to whoever notices the
// 404 (tick 5o5).
//
// short: reads the README and greps one section; no build, no subprocess
func TestTheReadmeNamesHowTheFirstReleaseIsCut(t *testing.T) {
	section := readmeSection(readDistFile(t, "README.md"), "Install")
	if section == "" {
		t.Fatal("README.md has no \"## Install\" section")
	}
	for _, want := range []string{
		"make release VERSION=vX.Y.Z",
		"until a `v*` tag exists",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the README's Install section does not mention %q:\n%s", want, section)
		}
	}
}

// makefileRecipe reads one target's recipe lines from the Makefile, the
// leading tab stripped. An empty answer means the target is not there —
// which is itself the finding, so the caller names what it wanted.
func makefileRecipe(t *testing.T, target string) string {
	t.Helper()
	lines := strings.Split(readDistFile(t, "Makefile"), "\n")
	for i, line := range lines {
		if line != target+":" {
			continue
		}
		var recipe []string
		for _, next := range lines[i+1:] {
			if !strings.HasPrefix(next, "\t") {
				break
			}
			recipe = append(recipe, strings.TrimPrefix(next, "\t"))
		}
		return strings.Join(recipe, "\n")
	}
	return ""
}

// readmeSection extracts one `## <title>` section's body, up to the next
// `## ` heading. An empty string means the section is not there at all.
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
