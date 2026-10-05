package gitbin

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// checkPaced is the queue's own promise, read off a list of push start times
// for one repository: no two closer than the slot, never more than the limit
// in any trailing window.
func checkPaced(t *testing.T, policy PushPolicy, starts []time.Time) {
	t.Helper()
	sorted := append([]time.Time{}, starts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Before(sorted[j]) })
	for i := 1; i < len(sorted); i++ {
		if gap := sorted[i].Sub(sorted[i-1]); gap < policy.MinGap {
			t.Errorf("pushes %d and %d started %s apart, inside one pacing slot (%s)", i-1, i, gap, policy.MinGap)
		}
	}
	for i := range sorted {
		n := 0
		for j := i; j < len(sorted) && sorted[j].Sub(sorted[i]) < policy.Window; j++ {
			n++
		}
		if n > policy.PerWindow {
			t.Errorf("%d pushes started inside one %s window from push %d, want at most %d", n, policy.Window, i,
				policy.PerWindow)
		}
	}
}

// A burst of reservations at one instant — every run on the host folding the
// same main commit — is spread into slots that respect both the gap and the
// window.
//
// short: arithmetic only
func TestABurstOfPushesIsSpreadIntoPacedSlots(t *testing.T) {
	t.Parallel()
	policy := PushPolicy{PerWindow: 5, Window: time.Minute, MinGap: 3 * time.Second}
	now := time.Unix(1_000_000, 0)
	var slots []int64
	var starts []time.Time
	for i := 0; i < 12; i++ {
		var slot time.Time
		slot, slots = nextSlot(policy, slots, now)
		starts = append(starts, slot)
	}
	checkPaced(t, policy, starts)
	if starts[0] != now {
		t.Errorf("the first push of an idle queue waited until %s, want now", starts[0].Sub(now))
	}
	// Five fit the first minute; the sixth waits for the first to leave it.
	if got, want := starts[5].Sub(now), time.Minute; got != want {
		t.Errorf("the sixth push of a burst is at +%s, want +%s", got, want)
	}

	// A queue gone quiet for a window is idle again.
	later := starts[len(starts)-1].Add(2 * time.Minute)
	slot, kept := nextSlot(policy, slots, later)
	if slot != later {
		t.Errorf("a push after a quiet window waited %s", slot.Sub(later))
	}
	if len(kept) != 1 {
		t.Errorf("the queue kept %d slots no future push can count", len(kept)-1)
	}
}

// Two ticfac processes reserving against one repository's queue at once
// never share a slot: each reservation is taken under the queue file's flock,
// and each simulated run opens the file for itself as a separate process does.
//
// short: a few dozen reservations in a temp directory; no git, no sleeping
func TestTwoRunsReservingAtOnceNeverShareASlot(t *testing.T) {
	t.Parallel()
	policy := PushPolicy{PerWindow: 5, Window: time.Minute, MinGap: 3 * time.Second, Dir: t.TempDir()}
	now := time.Now()
	var mu sync.Mutex
	var starts []time.Time
	var wg sync.WaitGroup
	for run := 0; run < 2; run++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 8; i++ {
				slot, err := reservePush(policy, "github.com/o/r", now)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				starts = append(starts, slot)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(starts) != 16 {
		t.Fatalf("reserved %d slots, want 16", len(starts))
	}
	checkPaced(t, policy, starts)
	// A different repository is a different queue.
	if slot, err := reservePush(policy, "github.com/o/other", now); err != nil || !slot.Equal(time.Unix(0, now.UnixNano())) {
		t.Errorf("another repository's first push waited until %s (%v), want now", slot.Sub(now), err)
	}
}

// Off by default, the queue delays nothing — and still records every push in
// the host's log, so a failed push is told how many pushes to the repository
// started from this host in its trailing minute and whether another run's
// push started within five seconds of it (tick rlp).
//
// short: three recorded pushes in a temp directory; no git, no network
func TestUnpacedPushesAreRecordedAndAFailureToldWhatWasAroundIt(t *testing.T) {
	t.Parallel()
	if DefaultPushPolicy.Pace && os.Getenv(PushQueueEnv) == "" {
		t.Fatal("the push queue paces by default; it is off until the operator turns it on")
	}
	policy := PushPolicy{Dir: t.TempDir(), MaxWait: time.Minute, NoticeAfter: time.Hour}
	args := []string{"push", "https://github.com/o/r.git", "x:refs/heads/epic/43y"}
	var events []PushEvent
	notify := func(e PushEvent) { events = append(events, e) }

	began := time.Now()
	queuePush(policy, "epic-hn6", time.Time{}, "", args, nil)(nil)
	queuePush(policy, "epic-43y", time.Time{}, "", args, nil)(nil)
	done := queuePush(policy, "epic-43y", time.Time{}, "", args, notify)
	if waited := time.Since(began); waited > 2*time.Second {
		t.Errorf("three unpaced pushes took %s to start: the queue delayed them", waited)
	}
	done(errors.New("! [remote rejected] abc -> epic/43y (failed)"))

	if len(events) != 1 || events[0].Kind != PushDone || events[0].Err == nil {
		t.Fatalf("the failed push was told as %+v, want one PushDone carrying its error", events)
	}
	c := events[0].Context
	if c == nil {
		t.Fatal("the failed push carries no context from the host's push log")
	}
	if c.TrailingMinute != 3 {
		t.Errorf("the log counts %d pushes in the trailing minute, want 3", c.TrailingMinute)
	}
	if c.Other != "epic-hn6" || c.OtherGap > 0 || c.OtherGap < -OtherWindow {
		t.Errorf("the nearest other run's push is %q at %s, want epic-hn6 just before", c.Other, c.OtherGap)
	}
}

// short: arithmetic only
func TestTheContextAroundAPushCountsTheMinuteAndOnlyOtherRunsNearIt(t *testing.T) {
	t.Parallel()
	at := time.Unix(1_000_000, 0)
	entry := func(d time.Duration, owner string) pushLogEntry {
		return pushLogEntry{At: at.Add(d).UnixNano(), Owner: owner}
	}
	c := contextOf([]pushLogEntry{
		entry(-90*time.Second, "b"), // outside the minute
		entry(-30*time.Second, "b"), // in the minute, not near
		entry(-2*time.Second, "a"),  // our own, near: not another run
		entry(-4*time.Second, "b"),  // near, before
		entry(0, "a"),               // this push
		entry(3*time.Second, "c"),   // near, after, and nearer
	}, "a", at)
	if c.TrailingMinute != 4 {
		t.Errorf("counted %d in the trailing minute, want 4", c.TrailingMinute)
	}
	if c.Other != "c" || c.OtherGap != 3*time.Second {
		t.Errorf("the nearest other run is %q at %s, want c at +3s", c.Other, c.OtherGap)
	}
	if c := contextOf([]pushLogEntry{entry(0, "a"), entry(-6*time.Second, "b")}, "a", at); c.Other != "" {
		t.Errorf("a push 6s away counted as beside this one: %+v", c)
	}
}

// short: string parsing only
func TestTheQueueKeysAHostedRepositoryByItsName(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		url  string
		want string
		ok   bool
	}{
		{"https://github.com/pengelbrecht/ticfac.git", "github.com/pengelbrecht/ticfac", true},
		{"https://x-access-token:secret@github.com/pengelbrecht/TicFac/", "github.com/pengelbrecht/ticfac", true},
		{"git@github.com:pengelbrecht/ticfac.git", "github.com/pengelbrecht/ticfac", true},
		{"ssh://git@github.com:22/pengelbrecht/ticfac", "github.com/pengelbrecht/ticfac", true},
		{"/tmp/origin.git", "", false},
		{"file:///tmp/origin.git", "", false},
		{"../origin", "", false},
		{"ssh://remote.invalid/tmp/origin.git", "", false},
		{"http://127.0.0.1:8080/o/r.git", "", false},
		{"https://localhost/o/r.git", "", false},
	} {
		got, ok := RepositoryKey(c.url, false)
		if got != c.want || ok != c.ok {
			t.Errorf("RepositoryKey(%q) = %q, %v; want %q, %v", c.url, got, ok, c.want, c.ok)
		}
	}
	if got, ok := RepositoryKey("/tmp/origin.git", true); !ok || got != "local/tmp/origin.git" {
		t.Errorf("RepositoryKey of a path, pacing local remotes, = %q, %v", got, ok)
	}
}

// short: string parsing only
func TestThePushRemoteIsReadOffTheArgv(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args []string
		want string
		push bool
	}{
		{[]string{"push", "origin", "a:b"}, "origin", true},
		{[]string{"-c", "user.name=x", "push", "--quiet", "--force-with-lease=refs/heads/a:b", "up", "x:y"}, "up", true},
		{[]string{"push", "-o", "ci.skip", "https://github.com/o/r", "x:y"}, "https://github.com/o/r", true},
		{[]string{"push"}, "origin", true},
		{[]string{"fetch", "origin"}, "", false},
		{[]string{"-C", "push", "status"}, "", false},
	} {
		if got := isPush(c.args); got != c.push {
			t.Errorf("isPush(%q) = %v, want %v", c.args, got, c.push)
		}
		if c.push {
			if got := pushRemote(c.args); got != c.want {
				t.Errorf("pushRemote(%q) = %q, want %q", c.args, got, c.want)
			}
		}
	}
}

// Every git this repository starts whose argv may be a push goes through
// PushQueue (tick rlp). The queue is only as good as its coverage: the
// failures it exists for came from pushes nobody paced — tracker records,
// integrations, folds, start refs, wip refs, worker attempt branches — and a
// runner written next month that skips it is the same gap again.
//
// Like the transport guard beside it, this reads every production Go file
// for the calls that start git and requires the function making the call to
// mention PushQueue, unless the call's own argv spells a subcommand other than
// push in literals the reader can see. A runner that takes its argv from its
// caller can be handed a push, so it goes through the queue whatever its
// callers do today; for anything but a push PushQueue costs nothing.
//
// short: parses the repository's Go files; no git, no processes
func TestEveryGitThatCanPushGoesThroughThePushQueue(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", "testdata", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		found, err := unqueuedPushes(filepath.ToSlash(rel), src)
		if err != nil {
			return err
		}
		offenders = append(offenders, found...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	offenders = slices.DeleteFunc(offenders, func(offender string) bool {
		file, rest, _ := strings.Cut(offender, ":")
		_, fn, _ := strings.Cut(rest, " in ")
		_, ok := neverPushes[file+" "+fn]
		return ok
	})
	if len(offenders) > 0 {
		t.Fatalf("git started where it can push, outside the push queue:\n  %s\n"+
			"Call gitbin.PushQueue(dir, args, notify) before the command runs and the done it answers after, in "+
			"the function that starts it: an unpaced push is how two runs on one repository collided "+
			"(docs/analysis/github-failures.md, tick rlp).", strings.Join(offenders, "\n  "))
	}
}

// neverPushes names the generic runners ("file func") whose argv this reader
// cannot see through but which never push, each with why.
var neverPushes = map[string]string{
	"internal/runstate/batch.go start":          "`cat-file --batch` after the safeArgs spread",
	"internal/runstate/batch.go catFileProcess": "`cat-file blob <sha>` after the safeArgs spread",
}

// The guard's negative control.
//
// short: parses one small source in memory
func TestThePushQueueGuardSeesAnUnqueuedPush(t *testing.T) {
	t.Parallel()
	const src = `package p

import (
	osexec "os/exec"
	"github.com/pengelbrecht/ticfac/internal/gitbin"
)

func push() { osexec.Command("git", "-C", "/r", "push", "origin", "main").Run() }

func runner(args ...string) { osexec.Command(gitbin.Path(), args...).Run() }

func queued(args ...string) {
	done := gitbin.PushQueue("", args, nil)
	done(osexec.Command(gitbin.Path(), args...).Run())
}

func fetch() { osexec.Command("git", "fetch", "origin").Run() }
`
	got, err := unqueuedPushes("p.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"p.go:8 in push", "p.go:10 in runner"}
	if !slices.Equal(got, want) {
		t.Fatalf("the guard reported %q, want %q", got, want)
	}
}

// unqueuedPushes is the guard over one file.
func unqueuedPushes(name string, src []byte) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	execName := ""
	for _, imp := range file.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "os/exec" {
			execName = "exec"
			if imp.Name != nil {
				execName = imp.Name.Name
			}
		}
	}
	if execName == "" {
		return nil, nil
	}
	var offenders []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		queued := mentions(fn, "PushQueue") || mentions(fn, "PushQueueUntil")
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != execName {
				return true
			}
			var args []ast.Expr
			switch sel.Sel.Name {
			case "Command":
				args = call.Args
			case "CommandContext":
				if len(call.Args) == 0 {
					return true
				}
				args = call.Args[1:]
			default:
				return true
			}
			if len(args) == 0 || !isGit(args[0]) {
				return true
			}
			if !queued && mayPush(args[1:], call.Ellipsis.IsValid()) {
				offenders = append(offenders, name+":"+strconv.Itoa(fset.Position(call.Pos()).Line)+" in "+fn.Name.Name)
			}
			return true
		})
	}
	return offenders, nil
}

// mayPush reads an argv as far as its literals go: the first word that is not
// a global flag is the subcommand, and only a literal other than push lets
// the call off.
func mayPush(args []ast.Expr, spread bool) bool {
	words, known := flatten(args, spread)
	for i := 0; i < len(words); i++ {
		if !known[i] {
			return true
		}
		word := words[i]
		if strings.HasPrefix(word, "-") {
			switch word {
			case "-c", "-C", "--git-dir", "--work-tree", "--namespace", "--exec-path":
				i++
			}
			continue
		}
		return word == "push"
	}
	return false
}
