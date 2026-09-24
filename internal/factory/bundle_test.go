package factory

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

// The embedded payload (cloudflare — moved there from cloud/factory by
// SPEC §12 Phase 4 item 1 — and image, moved there from cloud/sandbox by
// item 4) landed with ticks tick
// b3a ("Factory move B"), wired into the seams at the top of bundle.go by
// payload.go's init. The tests here still split in two, and the split stays
// deliberate:
//
//   - The MECHANICS (materialization, pruning, hashing, the in-place config
//     rewrites) run against a fake payload staged through the same seams,
//     so they exercise the staging code on bytes that were written for the
//     purpose, not against the real payload.
//   - The CONTENT assertions (the bundle really declares the container, the
//     image context really ships the entrypoints, the image path really
//     resolves) carry requireEmbeddedPayload below: it skips — loudly — if a
//     test runs with the seams unwired, so a wiring regression surfaces as a
//     wall of skips rather than as a silent pass.

// requireEmbeddedPayload skips a content assertion when this test binary
// carries no payload through the seams. With payload.go's init wiring the
// real trees it never fires on an ordinary run; it exists so an unwired
// seam (or a future split build) is visible rather than invisible.
func requireEmbeddedPayload(t *testing.T) {
	t.Helper()
	if factoryFS == nil || sandboxFS == nil {
		t.Skip("the embedded payload (cloudflare, image) is not wired into this test binary; these assertions run only when payload.go's init has assigned the module-root embeds")
	}
}

// fakeBundle is a minimal factory bundle and orchestrator image context,
// shaped to exercise every rewrite and every check this package performs on
// them: wrangler.toml carries the placeholder database_id, one container
// binding, the capacity numbers whose agreement the deploy refuses to ship
// without (tick 7fl), and a bucket; the Dockerfile carries both tk ARGs to
// pin.
//
// The bytes are fixtures, not copies: the REAL files' content is what the
// payload-guarded tests assert once the payload lands.
func fakeBundle() fstest.MapFS {
	return fstest.MapFS{
		"cloudflare/wrangler.toml": &fstest.MapFile{Data: []byte(
			`name = "ticks-factory"
main = "src/index.ts"
[[d1_databases]]
binding = "DB"
database_id = "` + placeholderDatabaseID + `"
[[containers]]
class_name = "Sandbox"
new_sqlite_classes = ["Sandbox"]
image = "../image/Dockerfile"
max_instances = 3
[[r2_buckets]]
binding = "ARTIFACTS"
bucket_name = "ticks-factory-artifacts"
[vars]
FACTORY_MAX_INSTANCES = "3"
`)},
		"cloudflare/src/index.ts":             &fstest.MapFile{Data: []byte("export {};")},
		"cloudflare/src/auth.ts":              &fstest.MapFile{Data: []byte("export {};")},
		"cloudflare/migrations/0001_init.sql": &fstest.MapFile{Data: []byte("-- fake migration")},
		"cloudflare/package.json":             &fstest.MapFile{Data: []byte("{}")},
		"image/Dockerfile": &fstest.MapFile{Data: []byte(
			"FROM docker.io/cloudflare/sandbox:fake\n" +
				"ARG TK_VERSION=0.31.0\n" +
				"ARG TK_SOURCE_REF=v0.31.0\n" +
				"ARG TK_MODULE=github.com/pengelbrecht/ticks/cmd/tk\n")},
		"image/entrypoint.sh": &fstest.MapFile{Data: []byte("# fake entrypoint\ntk version\n")},
		"image/worker.sh":     &fstest.MapFile{Data: []byte("tk sandbox worker-prompt\ntk sandbox environment\n")},
		"image/common.sh":     &fstest.MapFile{Data: []byte("exec tk list --awaiting=ask\n# run `tk factory setup` to fix this\necho \"tk ask is not on path\"\n")},
		"image/preflight.sh":  &fstest.MapFile{Data: []byte("tk sandbox toolchain\n")},
	}
}

// stageFakePayload wires the fake bundle into the seams for one test and
// restores the real embedded payload afterwards, so payload-guarded tests
// still see the trees this build ships.
func stageFakePayload(t *testing.T) fstest.MapFS {
	t.Helper()
	fake := fakeBundle()
	setPayloadSeam(t, fake, fake)
	return fake
}

// setPayloadSeam wires the payload seams for one test and restores the real
// embedded payload afterwards, so the tests that follow see the trees this
// build actually ships.
//
// The lazy caches are reset through resetPayloadCaches, which drops the
// memoized lists as well as the sync.Onces: they memoize over whatever FS
// was wired when first read, and a walk seeded by one payload would
// otherwise poison every later test's paths.
func setPayloadSeam(t *testing.T, factory, sandbox fs.FS) {
	t.Helper()
	factoryFS = factory
	sandboxFS = sandbox
	resetPayloadCaches()
	t.Cleanup(wireEmbeddedPayload)
}

// No payload, no staging: the seams fail LOUDLY, naming what is missing —
// never a silent empty read that would let a deploy proceed on nothing.
// The test unwires the seams by hand first, because an ordinary run carries
// the real payload from payload.go's init; the cleanup wires it back.
func TestMissingPayloadIsALoudStop(t *testing.T) {
	factoryFS = nil
	sandboxFS = nil
	cloudProfilesFS = nil
	resetPayloadCaches()
	t.Cleanup(wireEmbeddedPayload)

	for name, err := range map[string]error{
		"Materialize":        Materialize(t.TempDir()),
		"MaterializeSandbox": MaterializeSandbox(t.TempDir()),
		"StageCloudProfiles": StageCloudProfiles(t.TempDir()),
		"ReadBundleFile":     func() error { _, err := ReadBundleFile("wrangler.toml"); return err }(),
		"ReadSandboxFile":    func() error { _, err := ReadSandboxFile("Dockerfile"); return err }(),
	} {
		if err == nil || !strings.Contains(err.Error(), "unwired") {
			t.Errorf("%s with no payload: %v, want the missing-payload stop", name, err)
		}
	}
	if paths := BundlePaths(); len(paths) != 0 {
		t.Errorf("BundlePaths() = %v with no payload, want empty", paths)
	}
}

func TestBundlePathsCoverWhatWranglerNeeds(t *testing.T) {
	requireEmbeddedPayload(t)
	paths := BundlePaths()
	want := []string{
		"wrangler.toml",
		"src/index.ts",
		"src/auth.ts",
		"src/run-room.ts",
		"src/run-workflow.ts",
		"src/sandbox.ts",
		"src/artifacts.ts",
		"src/env.d.ts",
		"migrations/0001_init.sql",
		"package.json",
	}
	have := make(map[string]bool, len(paths))
	for _, p := range paths {
		have[p] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("embedded factory bundle is missing %s (got %v)", w, paths)
		}
	}
}

// node_modules is never committed, but a developer who ran `pnpm install` in
// cloudflare must not end up embedding it into the binary.
func TestBundleExcludesDependenciesAndTests(t *testing.T) {
	requireEmbeddedPayload(t)
	for _, p := range BundlePaths() {
		if strings.HasPrefix(p, "node_modules/") || strings.HasPrefix(p, ".wrangler/") {
			t.Errorf("bundle contains build output: %s", p)
		}
	}
}

func TestMaterializeWritesTheBundle(t *testing.T) {
	requireEmbeddedPayload(t)
	dir := filepath.Join(t.TempDir(), "bundle")

	if err := Materialize(dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	for _, p := range []string{"wrangler.toml", "src/index.ts", "migrations/0001_init.sql"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Errorf("materialized bundle missing %s: %v", p, err)
		}
	}
	toml, err := os.ReadFile(filepath.Join(dir, "wrangler.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(toml), `name = "ticks-factory"`) {
		t.Errorf("wrangler.toml does not look like the factory config:\n%s", toml)
	}
}

// The materialization mechanics, proven against the fake payload: every
// file the (one day real) embed ships lands on disk under the same relative
// path, and the staging directory is the bundle root, not a prefixed copy.
func TestMaterializeWritesEveryShippedFileAtItsOwnPath(t *testing.T) {
	stageFakePayload(t)
	dir := t.TempDir()

	if err := Materialize(dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	for _, p := range []string{"wrangler.toml", "src/index.ts", "migrations/0001_init.sql", "package.json"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Errorf("materialized bundle missing %s: %v", p, err)
		}
	}
}

// Re-running a deploy must not leave a file from the previous tk version
// behind: the deployed bundle is exactly the embedded one, or the pin is a lie.
func TestMaterializeReplacesStaleFiles(t *testing.T) {
	stageFakePayload(t)
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := Materialize(dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	stale := filepath.Join(dir, "src", "left-over.ts")
	if err := os.WriteFile(stale, []byte("// from an older tk"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Materialize(dir); err != nil {
		t.Fatalf("re-Materialize: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale file survived re-materialization (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wrangler.toml")); err != nil {
		t.Errorf("re-materialization lost the bundle: %v", err)
	}
}

func TestBundleSHAIsStableAndContentAddressed(t *testing.T) {
	stageFakePayload(t)
	first := BundleSHA()
	if len(first) != 64 {
		t.Fatalf("BundleSHA() = %q, want 64 hex chars", first)
	}
	if second := BundleSHA(); second != first {
		t.Errorf("BundleSHA is not stable: %s then %s", first, second)
	}
	// That the hash also covers the orchestrator image bytes is by
	// construction — BundleSHA walks SandboxPaths in the same loop (carried
	// from ticks unchanged) — but it cannot be OBSERVED in-process against a
	// mutated fake, because the value is cached in a sync.Once for the
	// binary's lifetime. The real payload's own bytes re-derive a fresh hash
	// on every build, which is where the property has always mattered.
}

func TestSetDatabaseIDRewritesOnlyTheID(t *testing.T) {
	stageFakePayload(t)
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := Materialize(dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	const id = "11111111-2222-3333-4444-555555555555"
	if err := SetDatabaseID(dir, id); err != nil {
		t.Fatalf("SetDatabaseID: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "wrangler.toml"))
	if err != nil {
		t.Fatal(err)
	}
	toml := string(data)
	if !strings.Contains(toml, `database_id = "`+id+`"`) {
		t.Errorf("database_id not rewritten:\n%s", toml)
	}
	if strings.Contains(toml, placeholderDatabaseID) {
		t.Errorf("placeholder database_id survived:\n%s", toml)
	}
	if !strings.Contains(toml, `bucket_name = "ticks-factory-artifacts"`) {
		t.Errorf("SetDatabaseID disturbed the rest of the config:\n%s", toml)
	}
	// One assignment only ("database_id" also appears in a comment).
	if n := strings.Count(toml, "database_id = "); n != 1 {
		t.Errorf("database_id assigned %d times, want 1:\n%s", n, toml)
	}
}

func TestSetDatabaseIDRejectsAMalformedID(t *testing.T) {
	stageFakePayload(t)
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := Materialize(dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	if err := SetDatabaseID(dir, `oops" \n name = "not-the-factory`); err == nil {
		t.Error("a database id that would break out of the TOML string was accepted")
	}
}

func TestMaterializedFilesAreOwnerWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	stageFakePayload(t)
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := Materialize(dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	// SetDatabaseID rewrites wrangler.toml on every deploy; a read-only copy
	// would make the second run fail.
	info, err := os.Stat(filepath.Join(dir, "wrangler.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o200 == 0 {
		t.Errorf("wrangler.toml is not writable: %o", info.Mode().Perm())
	}
}

// The bundle has to DECLARE the container, or a deployment is a control plane
// that refuses every run with a message naming a remedy — `ticfac factory
// deploy` — that cannot supply what the bundle never contained. That is the
// shape the live factory shipped in, and it is what this guards.
func TestBundleDeclaresTheContainerBinding(t *testing.T) {
	requireEmbeddedPayload(t)
	data, err := ReadBundleFile(WranglerConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	toml := string(data)

	for _, want := range []string{
		`name = "SANDBOXES"`,
		`class_name = "Sandbox"`,
		"[[containers]]",
		`new_sqlite_classes = ["Sandbox"]`,
	} {
		if !strings.Contains(toml, want) {
			t.Errorf("wrangler.toml does not declare %s:\n%s", want, toml)
		}
	}
}

// The committed config's two declarations of the sandbox capacity —
// `[[containers]] max_instances`, the account-level ceiling Cloudflare enforces
// on concurrent containers, and `[vars] FACTORY_MAX_INSTANCES`, the mirror a
// cloud wave's dispatch width is bounded by because wrangler does not hand a
// container application's own config back to the Worker at runtime — must say
// one number (tick 7fl). Two numbers that must agree and are maintained
// separately drift, and the failure when they do is a wave that books more
// containers than the account can host, surfacing as sandbox creation
// failures attributed to whichever tick happened to be fourth, never as a
// capacity message. This is the content half of the check: the deploy's
// refusal to ship a disagreement is covered in deploy_test.go.
func TestCommittedCapacityNumbersAgree(t *testing.T) {
	requireEmbeddedPayload(t)
	data, err := ReadBundleFile(WranglerConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyContainerCapacity(data); err != nil {
		t.Errorf("the committed wrangler.toml's capacity numbers disagree: %v", err)
	}
}

// The mechanics of the check, on bytes written for the purpose: an agreeing
// pair passes, and a disagreement, a missing ceiling or a missing mirror is a
// stop that names both numbers — never a shrug that lets a deploy proceed on
// half a check.
func TestVerifyContainerCapacityNamesEveryStop(t *testing.T) {
	const pair = "[[containers]]\nmax_instances = %s\n[vars]\nFACTORY_MAX_INSTANCES = \"%s\"\n"

	agreeing := fmt.Sprintf(pair, "3", "3")
	if err := VerifyContainerCapacity([]byte(agreeing)); err != nil {
		t.Errorf("an agreeing pair was refused: %v", err)
	}

	disagreeing := fmt.Sprintf(pair, "3", "2")
	err := VerifyContainerCapacity([]byte(disagreeing))
	if err == nil {
		t.Fatalf("max_instances = 3 against FACTORY_MAX_INSTANCES = \"2\" was accepted")
	}
	for _, want := range []string{"max_instances = 3", `FACTORY_MAX_INSTANCES = "2"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the disagreement stop does not name %s: %v", want, err)
		}
	}

	for name, missing := range map[string]string{
		"no [[containers]] max_instances": "[vars]\nFACTORY_MAX_INSTANCES = \"3\"\n",
		"no [vars] FACTORY_MAX_INSTANCES": "[[containers]]\nmax_instances = 3\n",
	} {
		if err := VerifyContainerCapacity([]byte(missing)); err == nil {
			t.Errorf("wrangler.toml with %s was accepted:\n%s", name, missing)
		} else if !strings.Contains(err.Error(), name) {
			t.Errorf("the %s stop does not say so: %v", name, err)
		}
	}
}

// The image path in the committed config has to resolve to the tree the deploy
// stages — the same relative path, true in the repository and in the bundle.
func TestContainerImagePathResolvesToTheStagedContext(t *testing.T) {
	requireEmbeddedPayload(t)
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	if err := Materialize(bundleDir); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeSandbox(SandboxDir(bundleDir)); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(bundleDir, WranglerConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	image := containerImagePath(t, string(data))
	if _, err := os.Stat(filepath.Join(bundleDir, filepath.FromSlash(image))); err != nil {
		t.Errorf("the container image path %q does not resolve from the staged bundle: %v", image, err)
	}
}

// The same resolution, provable against the fake payload today: the image
// path is relative to the bundle directory and the staged context sits at
// exactly that path.
func TestContainerImagePathResolvesAgainstAFakeStagedContext(t *testing.T) {
	stageFakePayload(t)
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	if err := Materialize(bundleDir); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeSandbox(SandboxDir(bundleDir)); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(bundleDir, WranglerConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	image := containerImagePath(t, string(data))
	if _, err := os.Stat(filepath.Join(bundleDir, filepath.FromSlash(image))); err != nil {
		t.Errorf("the container image path %q does not resolve from the staged bundle: %v", image, err)
	}
}

// SPEC §12 Phase 4 item 1 moved the bundle to cloudflare and item 4 moved
// the image context to image, so the committed image path is only true in
// this repository if it names that cross. This is the half of "one relative path, true in both places" a
// move breaks SILENTLY: the staged half is covered by the two tests above
// (they stage through SandboxDir, so they agree with whatever SandboxDir
// does), and the repository half is this one. It also pins SandboxDir to
// the committed path: staging mirrors the repository layout, so the
// committed path and the staging derivation must say the same thing or the
// deploy resolves a path the repository does not (and a factory built
// in-repo deploys a different image than `wrangler dev` would boot).
func TestCommittedImagePathResolvesInTheRepository(t *testing.T) {
	requireEmbeddedPayload(t)
	data, err := ReadBundleFile(WranglerConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	image := containerImagePath(t, string(data))
	if path.IsAbs(image) {
		t.Fatalf("the container image path %q is absolute; it has to be relative to the bundle to be true in both places", image)
	}
	// The repository copy of the bundle sits at bundleRoot under the
	// repository root; internal/factory is two directories below that root.
	repoBundle := filepath.Join("..", "..", filepath.FromSlash(bundleRoot))
	if _, err := os.Stat(filepath.Join(repoBundle, filepath.FromSlash(image))); err != nil {
		t.Errorf("the container image path %q does not resolve from the repository's copy of the bundle (%s): %v", image, bundleRoot, err)
	}
	// And the staging derivation names the same place the committed path does.
	if committed := path.Dir(image); committed != sandboxRelativeToBundle {
		t.Errorf("wrangler.toml's image path puts the context at %q; SandboxDir stages it at %q — the staging mirrors the repository layout, so the two must agree", committed, sandboxRelativeToBundle)
	}
}

// containerImagePath reads the single `image = "..."` assignment out of the
// config. A regex rather than a TOML parser because the assertion is about one
// literal line, and a parser here would be a second grammar to keep honest.
func containerImagePath(t *testing.T, toml string) string {
	t.Helper()
	match := regexp.MustCompile(`(?m)^image\s*=\s*"([^"]+)"`).FindStringSubmatch(toml)
	if match == nil {
		t.Fatalf("wrangler.toml declares no container image:\n%s", toml)
	}
	return match[1]
}

// The Worker imports the Cloudflare Sandbox SDK, so the deploy installs the
// bundle before deploying it — with the lockfile, or the deployed dependency
// tree is whatever npm published today rather than what this build pins.
func TestBundleShipsTheLockfileTheInstallNeeds(t *testing.T) {
	requireEmbeddedPayload(t)
	have := make(map[string]bool)
	for _, p := range BundlePaths() {
		have[p] = true
	}
	for _, want := range []string{"package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml"} {
		if !have[want] {
			t.Errorf("the bundle does not ship %s, so `pnpm install --frozen-lockfile` cannot run", want)
		}
	}
}

func TestSandboxContextShipsTheImage(t *testing.T) {
	requireEmbeddedPayload(t)
	have := make(map[string]bool)
	for _, p := range SandboxPaths() {
		have[p] = true
	}
	// What the Dockerfile COPYs, plus the Dockerfile: the build context is
	// the image, and an image missing an entrypoint boots into nothing. Both
	// roles' entrypoints and the common half they source have to be here —
	// one image serves orchestrator and per-tick worker (tick x3v), and a
	// worker.sh that never shipped is a wave of containers with nothing to
	// run.
	for _, want := range []string{"Dockerfile", "entrypoint.sh", "worker.sh", "common.sh", "preflight.sh"} {
		if !have[want] {
			t.Errorf("the embedded image context is missing %s (got %v)", want, SandboxPaths())
		}
	}
}

func TestMaterializeSandboxStagesTheBuildContext(t *testing.T) {
	requireEmbeddedPayload(t)
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	dir := SandboxDir(bundleDir)

	if err := MaterializeSandbox(dir); err != nil {
		t.Fatalf("MaterializeSandbox: %v", err)
	}

	if want := SandboxDir(bundleDir); dir != want {
		t.Errorf("the image context is staged at %q, want SandboxDir (%s) of the bundle %q", dir, sandboxRelativeToBundle, bundleDir)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatalf("staged Dockerfile: %v", err)
	}
	if !strings.Contains(string(data), "FROM docker.io/cloudflare/sandbox") {
		t.Errorf("the staged Dockerfile is not the orchestrator image:\n%s", data)
	}
}

// The staging mechanics against the fake payload: the context lands at
// sandboxRelativeToBundle — mirroring the repository layout — every shipped
// file is there, and a file an older build wrote is pruned on re-stage.
func TestMaterializeSandboxMechanicsAgainstAFakePayload(t *testing.T) {
	stageFakePayload(t)
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	dir := SandboxDir(bundleDir)

	if err := MaterializeSandbox(dir); err != nil {
		t.Fatalf("MaterializeSandbox: %v", err)
	}
	if want := SandboxDir(bundleDir); dir != want {
		t.Errorf("the image context is staged at %q, want SandboxDir (%s) of the bundle %q", dir, sandboxRelativeToBundle, bundleDir)
	}
	for _, p := range []string{"Dockerfile", "entrypoint.sh", "common.sh"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("staged image context missing %s: %v", p, err)
		}
	}
	stale := filepath.Join(dir, "left-over.sh")
	if err := os.WriteFile(stale, []byte("# from an older build"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeSandbox(dir); err != nil {
		t.Fatalf("re-MaterializeSandbox: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a file an older build shipped survived re-staging (err=%v)", err)
	}
}
