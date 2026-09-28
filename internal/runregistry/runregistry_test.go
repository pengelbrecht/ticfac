package runregistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The registration itself (tick aj9): one write, one read, and every field
// the convention needs — the checkout the run works in, absolute; the
// machine that wrote it, named; the moment it claimed life there, parseable.
func TestWriteThenRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)

	if err := write(dir, "epic-2jn", "/a/checkout", now); err != nil {
		t.Fatal(err)
	}

	reg, ok, err := read(dir, "epic-2jn")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("a written registration could not be read back")
	}
	if reg.SchemaVersion != SchemaVersion {
		t.Errorf("schema version %d, want %d", reg.SchemaVersion, SchemaVersion)
	}
	if reg.RunID != "epic-2jn" {
		t.Errorf("run id %q, want epic-2jn", reg.RunID)
	}
	if reg.Repo != "/a/checkout" {
		t.Errorf("repo %q, want the checkout the run works in", reg.Repo)
	}
	if reg.Host == "" {
		t.Error("the registration does not name the machine that wrote it — a path is host-local, and the name is what says whose")
	}
	if at, err := time.Parse(time.RFC3339, reg.RegisteredAt); err != nil || !at.Equal(now) {
		t.Errorf("registered_at %q does not name the claim's moment: %v", reg.RegisteredAt, err)
	}
}

// A run with no registration is a fact, not an error: the caller falls back
// to the checkout it is in, and the second return says which answer it got.
func TestReadWithoutRegistration(t *testing.T) {
	t.Parallel()
	if reg, ok, err := read(t.TempDir(), "epic-none"); err != nil {
		t.Fatalf("an absent registration is an error: %v", err)
	} else if ok {
		t.Fatalf("an absent registration read as present: %+v", reg)
	}
}

// The registration is an atomic overwrite, and last writer wins by design:
// a run resumed in another checkout is driven from there, and the last claim
// is the claim that matters. A reader mid-write sees the whole registration
// or none of it, and no half-written file survives the write.
func TestWriteIsAnAtomicOverwrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := write(dir, "epic-x", "/first/checkout", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := write(dir, "epic-x", "/second/checkout", time.Now()); err != nil {
		t.Fatal(err)
	}

	reg, ok, err := read(dir, "epic-x")
	if err != nil || !ok {
		t.Fatalf("the overwritten registration could not be read: ok=%v err=%v", ok, err)
	}
	if reg.Repo != "/second/checkout" {
		t.Errorf("after a resume in another checkout the registration names %q, want /second/checkout", reg.Repo)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "epic-x.json" {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the registry holds %v after an overwrite, want exactly epic-x.json — a temp file left behind is a registration half-written", names)
	}
}

// A run id reaches this package from the command line, so one carrying a
// separator — or a dot of traversal — is refused at the door, the same rule
// runstate holds for its record names: it would write outside the registry.
func TestWriteRefusesRunIDsThatEscapeTheRegistry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, id := range []string{"", "a/b", "a\\b", ".", "..", ".hidden"} {
		if err := write(dir, id, "/a/checkout", time.Now()); err == nil {
			t.Errorf("run id %q was accepted", id)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a refused run id still wrote: %v", entries)
	}
}

// The registered repo is stored ABSOLUTE. A relative path is a coordinate of
// the process that wrote it, and the registration exists precisely to be read
// from OTHER checkouts, where a relative path names nothing.
//
// Serial: filepath.Abs resolves against the process's working directory, and
// the honest test does not move that directory under parallel neighbours.
func TestWriteStoresAnAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	if err := write(dir, "epic-rel", "a/checkout", time.Now()); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	reg, ok, err := read(dir, "epic-rel")
	if err != nil || !ok {
		t.Fatalf("the registration could not be read: ok=%v err=%v", ok, err)
	}
	if reg.Repo != filepath.Join(wd, "a", "checkout") {
		t.Errorf("a relative repo stored as %q, want %q", reg.Repo, filepath.Join(wd, "a", "checkout"))
	}
}

// The convention as one call: the registered repo when the machine holds one,
// else the fallback — which is the per-checkout answer `ticfac status` gives
// today, unchanged for a run this machine never claimed.
func TestWorkingRepoPrefersTheRegistration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := write(dir, "epic-2jn", "/the/working/checkout", time.Now()); err != nil {
		t.Fatal(err)
	}
	repo, registered := workingRepo(dir, "epic-2jn", "/where/the/caller/is")
	if !registered {
		t.Error("a registered run read as unregistered")
	}
	if repo != "/the/working/checkout" {
		t.Errorf("probing repo %q, want the registered working repo", repo)
	}
}

func TestWorkingRepoFallsBackToTheCallersCheckout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if repo, registered := workingRepo(dir, "epic-unknown", "/where/the/caller/is"); registered || repo != "/where/the/caller/is" {
		t.Errorf("no registration: repo=%q registered=%v, want the fallback checkout and no claim it was registered", repo, registered)
	}
	// And an unreadable registration falls back too: a corrupt file is a
	// fact the caller degrades on, never a run claimed for a path nobody read.
	if err := os.WriteFile(filepath.Join(dir, "epic-corrupt.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if repo, registered := workingRepo(dir, "epic-corrupt", "/where/the/caller/is"); registered || repo != "/where/the/caller/is" {
		t.Errorf("a corrupt registration: repo=%q registered=%v, want the fallback and no claim it was registered", repo, registered)
	}
}

// List is the enumeration a listing surface starts from: every registration
// this machine holds, by run id so two reads differ only in the numbers. An
// empty registry is an empty list, not an error — "none" is an answer — and a
// registration that cannot be decoded is an error, not a skip: a listing that
// quietly drops a run is the silence this package exists to end.
func TestListSortsAndRefusesCorruption(t *testing.T) {
	t.Parallel()
	if regs, err := list(filepath.Join(t.TempDir(), "absent")); err != nil || len(regs) != 0 {
		t.Fatalf("an absent registry: regs=%v err=%v, want an empty list and no error", regs, err)
	}

	dir := t.TempDir()
	for _, id := range []string{"epic-zz", "epic-aa", "epic-mm"} {
		if err := write(dir, id, "/a/checkout", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	regs, err := list(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 3 {
		t.Fatalf("listed %d registrations, want 3", len(regs))
	}
	for i, want := range []string{"epic-aa", "epic-mm", "epic-zz"} {
		if regs[i].RunID != want {
			t.Errorf("list position %d is %s, want %s", i, regs[i].RunID, want)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "epic-broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := list(dir); err == nil {
		t.Error("a corrupt registration was skipped rather than refused")
	}
}

// The exported surface answers from Dir(): the override names a directory
// (tests, containers), and the default is the machine's own ticfac state,
// beside the executor state that already lives there — never inside a
// checkout, which is the very thing a registration names.
//
// Serial: t.Setenv cannot be used with t.Parallel.
func TestTheExportedSurfaceReadsDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(RegistryDirEnv, dir)
	// A checkout that stands: the exported surface answers only for
	// registrations whose checkout still exists (see
	// TestARegistrationWhoseCheckoutIsGoneIsNoRegistration).
	working := t.TempDir()

	if err := Register("epic-env", working); err != nil {
		t.Fatal(err)
	}
	reg, ok, err := Lookup("epic-env")
	if err != nil || !ok {
		t.Fatalf("Lookup after Register: ok=%v err=%v", ok, err)
	}
	if reg.Repo != working {
		t.Errorf("registration repo %q, want %s", reg.Repo, working)
	}

	repo, registered := WorkingRepo("epic-env", "/fallback")
	if !registered || repo != working {
		t.Errorf("WorkingRepo through Dir(): repo=%q registered=%v", repo, registered)
	}

	regs, err := List()
	if err != nil || len(regs) != 1 || regs[0].RunID != "epic-env" {
		t.Fatalf("List through Dir(): regs=%v err=%v", regs, err)
	}

	// The file is one JSON object a person can read, versioned like every
	// other record this tree writes.
	raw, err := os.ReadFile(filepath.Join(dir, "epic-env.json"))
	if err != nil {
		t.Fatal(err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("the registration is not one JSON object: %v\n%s", err, raw)
	}
	if asMap["schema_version"] != float64(SchemaVersion) {
		t.Errorf("schema_version %v, want %d", asMap["schema_version"], SchemaVersion)
	}
}

// The default is the machine's own ticfac state. It is read through
// OperatorDir, the one spelling of it that never refuses: Dir() in a test
// binary with no redirect refuses (see
// TestATestBinaryWithNoRedirectRefusesTheOperatorsRegistry), and HOME is
// moved so no assertion here could touch the real directory.
//
// Serial: t.Setenv cannot be used with t.Parallel.
func TestDirDefaultsToTheMachineStateDir(t *testing.T) {
	t.Setenv(RegistryDirEnv, "")
	t.Setenv("HOME", t.TempDir())
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory on this host")
	}
	if got := OperatorDir(); got != filepath.Join(home, ".ticfac", "registry") {
		t.Errorf("OperatorDir() = %q, want %q", got, filepath.Join(home, ".ticfac", "registry"))
	}
}

// Tick 7ag's guard was opt-in: a package whose TestMain did not call
// registrytest.GuardMain -- the 9sz branch's own dispatch-only TestMain on
// 2026-09-27 was one -- ran every claim in its suite against the operator's
// real ~/.ticfac/registry, and eight phantom runs pointing at deleted temp
// dirs surfaced in the bare `ticfac` overview. So the refusal lives where
// every writer and reader passes: a TEST BINARY (testing.Testing(), which is
// also true of every child a test re-execs from its own binary) whose
// environment names no redirect is refused the operator's registry
// outright -- loudly, by a panic that fails the package, because Claim
// treats a failed registration as best effort and would log an error
// nobody reads.
//
// HOME is moved first, so a regression that writes anyway writes into this
// test's temp dir and never onto the operator's machine.
//
// Serial: t.Setenv cannot be used with t.Parallel.
func TestATestBinaryWithNoRedirectRefusesTheOperatorsRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(RegistryDirEnv, "")
	operator := filepath.Join(home, ".ticfac", "registry")

	mustPanic := func(what string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s in a test binary with no %s redirect did not refuse", what, RegistryDirEnv)
			}
		}()
		fn()
	}
	mustPanic("Register", func() { _ = Register("epic-leak", t.TempDir()) })
	mustPanic("Lookup", func() { _, _, _ = Lookup("epic-leak") })
	mustPanic("List", func() { _, _ = List() })
	mustPanic("WorkingRepo", func() { _, _ = WorkingRepo("epic-leak", "/fallback") })

	if _, err := os.Stat(filepath.Join(operator, "epic-leak.json")); err == nil {
		t.Errorf("the refused registration was written anyway, into %s", operator)
	}

	// A redirect is all a test needs: the refusal is about WHERE, never
	// about whether a test may register at all.
	t.Setenv(RegistryDirEnv, t.TempDir())
	if err := Register("epic-ok", t.TempDir()); err != nil {
		t.Errorf("a redirected registration was refused: %v", err)
	}
}

// A registration names the checkout a run works in, and one whose checkout
// no longer exists names nothing a probe, a listing or a person can act on:
// the eight test strays of 2026-09-27 each pointed at a deleted go-test temp
// dir and read as "held for a person" in the bare `ticfac`. Every exported
// reader treats it as no registration at all and removes it, so the
// machine's registry cleans itself the first time anything reads it: List
// drops it, Lookup and WorkingRepo answer as though it were absent. A
// registration whose checkout stands is untouched.
//
// Serial: t.Setenv cannot be used with t.Parallel.
func TestARegistrationWhoseCheckoutIsGoneIsNoRegistration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(RegistryDirEnv, dir)
	standing := t.TempDir()
	gone := filepath.Join(t.TempDir(), "deleted-checkout")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	for id, repo := range map[string]string{"epic-here": standing, "epic-gone": gone, "epic-gone2": gone} {
		if err := Register(id, repo); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := Lookup("epic-gone2"); ok || err != nil {
		t.Errorf("Lookup of a registration naming a deleted checkout: ok=%v err=%v, want absent", ok, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "epic-gone2.json")); !os.IsNotExist(err) {
		t.Errorf("Lookup left the registration naming a deleted checkout in place (stat: %v)", err)
	}
	if repo, registered := WorkingRepo("epic-gone", "/fallback"); registered || repo != "/fallback" {
		t.Errorf("WorkingRepo of a registration naming a deleted checkout = %q (registered %v), want the fallback", repo, registered)
	}
	if err := Register("epic-gone", gone+"-never"); err != nil {
		t.Fatal(err)
	}

	regs, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 || regs[0].RunID != "epic-here" {
		t.Errorf("List() = %v, want only the registration whose checkout stands", regs)
	}
	if _, err := os.Stat(filepath.Join(dir, "epic-gone.json")); !os.IsNotExist(err) {
		t.Errorf("List left the registration naming a deleted checkout in place (stat: %v)", err)
	}
	if repo, registered := WorkingRepo("epic-here", "/fallback"); !registered || repo != standing {
		t.Errorf("WorkingRepo of a standing registration = %q (registered %v), want %s", repo, registered, standing)
	}
}
