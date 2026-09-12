package factory

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

// The embedded payload (cloud/factory, cloud/sandbox) is not in this
// repository yet — it lands with ticks tick b3a ("Factory move B"), which is
// also when the seams at the top of bundle.go get wired to the module-root
// embeds. The tests here split in two, and the split is deliberate:
//
//   - The MECHANICS (materialization, pruning, hashing, the in-place config
//     rewrites) run today against a fake payload staged through the same
//     seams, so tick b3a inherits staging code that has been executed, not
//     merely compiled.
//   - The CONTENT assertions (the bundle really declares the container, the
//     image context really ships the entrypoints, the image path really
//     resolves) carry requireEmbeddedPayload below: they skip — loudly —
//     until the real trees arrive, then turn themselves back on. Nothing is
//     deleted, and the failure mode of a drifted payload returns exactly as
//     it was.

// requireEmbeddedPayload skips a content assertion while this build carries
// no payload. It is a SKIP and not a failure because the files it names are
// pending, not missing: the same test asserts the same things the moment
// tick b3a lands the trees.
func requireEmbeddedPayload(t *testing.T) {
	t.Helper()
	if factoryFS == nil || sandboxFS == nil {
		t.Skip("the embedded payload (cloud/factory, cloud/sandbox) lands with ticks tick b3a (Factory move B); this assertion turns itself back on when it does")
	}
}

// fakeBundle is a minimal factory bundle and orchestrator image context,
// shaped to exercise every rewrite this package performs on them:
// wrangler.toml carries the placeholder database_id, one container binding
// and a bucket; the Dockerfile carries both tk ARGs to pin.
//
// The bytes are fixtures, not copies: the REAL files' content is what the
// payload-guarded tests assert once the payload lands.
func fakeBundle() fstest.MapFS {
	return fstest.MapFS{
		"cloud/factory/wrangler.toml": &fstest.MapFile{Data: []byte(
			`name = "ticks-factory"
main = "src/index.ts"
[[d1_databases]]
binding = "DB"
database_id = "` + placeholderDatabaseID + `"
[[containers]]
class_name = "Sandbox"
new_sqlite_classes = ["Sandbox"]
image = "../sandbox/Dockerfile"
[[r2_buckets]]
binding = "ARTIFACTS"
bucket_name = "ticks-factory-artifacts"
`)},
		"cloud/factory/src/index.ts":             &fstest.MapFile{Data: []byte("export {};")},
		"cloud/factory/src/auth.ts":              &fstest.MapFile{Data: []byte("export {};")},
		"cloud/factory/migrations/0001_init.sql": &fstest.MapFile{Data: []byte("-- fake migration")},
		"cloud/factory/package.json":             &fstest.MapFile{Data: []byte("{}")},
		"cloud/sandbox/Dockerfile": &fstest.MapFile{Data: []byte(
			"FROM docker.io/cloudflare/sandbox:fake\n" +
				"ARG TK_VERSION=0.31.0\n" +
				"ARG TK_SOURCE_REF=v0.31.0\n" +
				"ARG TK_MODULE=github.com/pengelbrecht/ticks/cmd/tk\n")},
		"cloud/sandbox/entrypoint.sh": &fstest.MapFile{Data: []byte("# fake entrypoint\ntk version\n")},
		"cloud/sandbox/worker.sh":     &fstest.MapFile{Data: []byte("tk sandbox worker-prompt\ntk sandbox environment\n")},
		"cloud/sandbox/common.sh":     &fstest.MapFile{Data: []byte("exec tk list --awaiting=ask\n# run `tk factory setup` to fix this\necho \"tk ask is not on path\"\n")},
		"cloud/sandbox/preflight.sh":  &fstest.MapFile{Data: []byte("tk sandbox toolchain\n")},
	}
}

// stageFakePayload wires the fake bundle into the seams for one test and
// unwires it afterwards, so payload-guarded tests still see the honest nil.
//
// The lazy caches (pathsOnce, sandboxPathsOnce, shaOnce) have to be reset
// with the seams: they memoize over whatever FS was wired when first read,
// and a nil-seeded walk in one test would poison every later test's paths.
// A fresh sync.Once is the zero value, which is the reset.
func stageFakePayload(t *testing.T) fstest.MapFS {
	t.Helper()
	fake := fakeBundle()
	setPayloadSeam(t, fake, fake)
	return fake
}

// setPayloadSeam wires the payload seams for one test and restores the honest
// nil afterwards.
func setPayloadSeam(t *testing.T, factory, sandbox fs.FS) {
	t.Helper()
	factoryFS = factory
	sandboxFS = sandbox
	pathsOnce = sync.Once{}
	sandboxPathsOnce = sync.Once{}
	shaOnce = sync.Once{}
	t.Cleanup(func() {
		factoryFS = nil
		sandboxFS = nil
		pathsOnce = sync.Once{}
		sandboxPathsOnce = sync.Once{}
		shaOnce = sync.Once{}
	})
}

// No payload, no staging: the seams fail LOUDLY, naming what is missing —
// never a silent empty read that would let a deploy proceed on nothing.
func TestMissingPayloadIsALoudStop(t *testing.T) {
	for name, err := range map[string]error{
		"Materialize":        Materialize(t.TempDir()),
		"MaterializeSandbox": MaterializeSandbox(t.TempDir()),
		"ReadBundleFile":     func() error { _, err := ReadBundleFile("wrangler.toml"); return err }(),
		"ReadSandboxFile":    func() error { _, err := ReadSandboxFile("Dockerfile"); return err }(),
	} {
		if err == nil || !strings.Contains(err.Error(), "b3a") {
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
// cloud/factory must not end up embedding it into the binary.
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

	if filepath.Dir(dir) != filepath.Dir(bundleDir) {
		t.Errorf("the image context %q is not a sibling of the bundle %q", dir, bundleDir)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatalf("staged Dockerfile: %v", err)
	}
	if !strings.Contains(string(data), "FROM docker.io/cloudflare/sandbox") {
		t.Errorf("the staged Dockerfile is not the orchestrator image:\n%s", data)
	}
}

// The staging mechanics against the fake payload: the context lands as the
// bundle's SIBLING (SandboxDirName), every shipped file is there, and a file
// an older build wrote is pruned on re-stage.
func TestMaterializeSandboxMechanicsAgainstAFakePayload(t *testing.T) {
	stageFakePayload(t)
	bundleDir := filepath.Join(t.TempDir(), "bundle")
	dir := SandboxDir(bundleDir)

	if err := MaterializeSandbox(dir); err != nil {
		t.Fatalf("MaterializeSandbox: %v", err)
	}
	if filepath.Base(dir) != SandboxDirName || filepath.Dir(dir) != filepath.Dir(bundleDir) {
		t.Errorf("the image context %q is not the sibling %s of the bundle %q", dir, SandboxDirName, bundleDir)
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
