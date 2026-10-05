package gitbin

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The push queue (tick rlp): every git push a ticfac process makes goes
// through PushQueue, which for a push to a hosted repository does two things.
//
// It ALWAYS RECORDS the push, host-wide: one line per push start in the
// repository's push log, shared by every ticfac process on the host, and
// tells the caller how the push ended. A failed push is told with what the
// log says around it — how many pushes to the repository started from this
// host in the trailing minute, and whether another run's push started within
// five seconds of it. That is the measurement docs/analysis/github-failures.md
// had to reconstruct after the fact: with hn6 and 43y live on one repository,
// 7 of the 8 bare "! [remote rejected] <sha> -> <ref> (failed)" rejections
// came within five seconds of the other run's push, against 3% of all pushes
// — a correlation over eight failures, and no evidence yet that failures
// cluster in minutes above GitHub's six pushes a minute. The record is what
// confirms that hypothesis or kills it. Recording costs a flock and a small
// file write; it never delays a push.
//
// It PACES only when the operator turns it on (TICFAC_PUSH_QUEUE=1, off by
// default, until the record says pacing is worth its delay). Then a push
// takes two things, in order. First its TURN: the repository's in-flight
// flock, so one push to a repository is in flight from this host at a time.
// Then, holding the turn, its SLOT: the earliest time at least MinGap after
// the last push started that leaves fewer than PerWindow starts in any
// trailing Window. The wait is BOUNDED (MaxWait, or a caller's deadline) and
// VISIBLE: a wait long enough to notice is told as it happens, and one past
// the bound is told as its own event and the push goes then.
//
// Neither half ever stops a push. A remote it cannot resolve, a file it
// cannot read, a lock it cannot take: the push goes ahead as it did before
// this existed.
//
// The cloud has its own answer for its repository writes (ef7: a repository
// Durable Object with one serialized publisher). Nothing here changes what
// that publisher is asked or answers; a container's pushes are recorded like
// any other.

// PushPolicy is how the queue records and, when Pace is set, paces one
// repository.
type PushPolicy struct {
	// Pace turns the pacing on; without it pushes are recorded and never
	// delayed.
	Pace bool
	// PerWindow pushes at most, in any trailing Window.
	PerWindow int
	Window    time.Duration
	// MinGap is the pacing slot: no two pushes to one repository start
	// closer together than this.
	MinGap time.Duration
	// MaxWait bounds one push's wait. A reservation further out is told as
	// PushOverdue and pushed at the bound.
	MaxWait time.Duration
	// NoticeAfter is the shortest wait told as PushWaiting before it starts.
	NoticeAfter time.Duration
	// FoldJitter bounds the jittered delay PushJitter adds before a push
	// every run on the host is likely to make at the same moment — the
	// base-refresh fold after a merge to main.
	FoldJitter time.Duration
	// Dir holds the queue's state files; empty is the user cache directory's
	// ticfac/push-queue, so every checkout of a repository on the host shares
	// one queue.
	Dir string
	// PaceLocal paces a remote that is a path on this machine as well. Only a
	// test wants it: a bare repository on disk has no rate to respect.
	PaceLocal bool
}

// PushQueueEnv turns the pacing on: "1" (or "on", "true"). Off by default.
const PushQueueEnv = "TICFAC_PUSH_QUEUE"

// DefaultPushPolicy, when paced, stays under GitHub's six pushes a minute per
// repository with room for a push the queue does not see (an operator's own, a
// worker's in its sandbox): five a minute, never two inside three seconds, a
// wait of at most two minutes. The fold jitter applies whether or not it
// paces.
var DefaultPushPolicy = PushPolicy{
	Pace:        envOn(os.Getenv(PushQueueEnv)),
	PerWindow:   5,
	Window:      time.Minute,
	MinGap:      3 * time.Second,
	MaxWait:     2 * time.Minute,
	NoticeAfter: 5 * time.Second,
	FoldJitter:  10 * time.Second,
}

func envOn(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}

var pushQueue = struct {
	sync.Mutex
	policy  PushPolicy
	enabled bool
	owner   string
}{
	policy: DefaultPushPolicy,
	// A test binary's gits push to bare repositories on disk and to
	// unreachable stand-in hosts; none of them is a repository whose pushes
	// are worth recording, and a test that recorded them would write into the
	// user's cache. A test that is ABOUT the queue switches it on with
	// SetPushPolicy.
	enabled: !testing.Testing(),
	owner:   fmt.Sprintf("pid:%d", os.Getpid()),
}

// SetPushPolicy switches the queue on with p, and answers the function that
// puts the previous state back. It exists for tests.
func SetPushPolicy(p PushPolicy) (restore func()) {
	pushQueue.Lock()
	defer pushQueue.Unlock()
	previous, wasEnabled := pushQueue.policy, pushQueue.enabled
	pushQueue.policy, pushQueue.enabled = p, true
	return func() {
		pushQueue.Lock()
		defer pushQueue.Unlock()
		pushQueue.policy, pushQueue.enabled = previous, wasEnabled
	}
}

// SetPushOwner names whose pushes this process makes in the host's push log:
// the run's id (a worker's supervisor names the run it works for), so a
// failure can be told whether ANOTHER run pushed beside it. Unset, a process
// is named by its pid.
func SetPushOwner(owner string) {
	if owner = strings.TrimSpace(owner); owner == "" {
		return
	}
	pushQueue.Lock()
	defer pushQueue.Unlock()
	pushQueue.owner = owner
}

func currentPushPolicy() (PushPolicy, bool, string) {
	pushQueue.Lock()
	defer pushQueue.Unlock()
	return pushQueue.policy, pushQueue.enabled, pushQueue.owner
}

// PushEventKind is what a PushEvent says.
type PushEventKind int

const (
	// PushWaiting is a push about to wait NoticeAfter or longer in the queue.
	PushWaiting PushEventKind = iota + 1
	// PushOverdue is a push whose slot was past MaxWait: it waits the bound
	// and pushes then.
	PushOverdue
	// PushDone is one recorded push finished, landed or not.
	PushDone
)

// PushEvent is the queue telling the caller what one push did in it.
type PushEvent struct {
	Kind PushEventKind
	// Repo is the queue's key: host/owner/name for a hosted remote.
	Repo string
	// At is when the push started (PushDone) or when its wait began.
	At time.Time
	// Wait is the time this push spent, or will spend, in the queue; zero
	// when the queue does not pace.
	Wait time.Duration
	// Bound is the policy's MaxWait.
	Bound time.Duration
	// Err is the push's own error, on PushDone; nil when it landed.
	Err error
	// Context is what the host's push log says around a FAILED push; nil
	// for one that landed, or when the log could not be read.
	Context *PushContext
}

// PushContext is the host's push log around one push: the evidence that
// confirms or kills "a (failed) is another run's push landing beside ours"
// and "a (failed) is the repository's rate".
type PushContext struct {
	// TrailingMinute is the pushes to the repository that started from this
	// host in the sixty seconds up to this one, this one included.
	TrailingMinute int
	// Other is the nearest push by another owner (another run) that started
	// within OtherWindow of this one, before or after; "" when none did.
	Other string
	// OtherGap is how far from this push's start the other one started:
	// negative before it, positive after.
	OtherGap time.Duration
}

// OtherWindow is how near another run's push must start to count beside a
// failure: the analysis's five seconds.
const OtherWindow = 5 * time.Second

// PushNotify is told about a push's queue wait and its outcome. Nil is valid.
type PushNotify func(PushEvent)

// PushQueue is the one function every git push a ticfac process makes goes
// through, called by the runner that starts git with the argv it is about to
// run. For anything but a push to a hosted repository it does nothing and
// answers a done that does nothing. For a push it records the push in the
// host's push log — and, when the queue paces, first waits for the push's
// turn and slot — and answers the done the runner calls with the push's
// error once git returns.
//
// dir is the directory the git runs in ("" is the process's), where a remote
// NAME is resolved to its URL.
func PushQueue(dir string, args []string, notify PushNotify) (done func(error)) {
	return PushQueueUntil(time.Time{}, dir, args, notify)
}

// PushQueueUntil is PushQueue for a push that must have started by deadline —
// the shutdown's evacuation, whose whole bound is seconds: a paced wait is cut
// at the deadline, and the push goes then. A zero deadline is no deadline.
func PushQueueUntil(deadline time.Time, dir string, args []string, notify PushNotify) (done func(error)) {
	policy, enabled, owner := currentPushPolicy()
	if !enabled || !isPush(args) {
		return func(error) {}
	}
	return queuePush(policy, owner, deadline, dir, args, notify)
}

func queuePush(policy PushPolicy, owner string, deadline time.Time, dir string, args []string,
	notify PushNotify) func(error) {
	repo, ok := pushRepository(dir, pushRemote(args), policy.PaceLocal)
	if !ok {
		return func(error) {}
	}
	if notify == nil {
		notify = func(PushEvent) {}
	}
	queued := time.Now()
	release := func() {}
	if policy.Pace {
		release = pace(policy, repo, queued, deadline, notify)
	}
	started := time.Now()
	logPush(policy, repo, owner, started)
	return func(err error) {
		release()
		event := PushEvent{Kind: PushDone, Repo: repo, At: started, Wait: started.Sub(queued), Bound: policy.MaxWait,
			Err: err}
		if err != nil {
			event.Context = pushContext(policy, repo, owner, started)
		}
		notify(event)
	}
}

// pace waits for a push's turn and slot, and answers the release of the turn.
func pace(policy PushPolicy, repo string, queued, deadline time.Time, notify PushNotify) func() {
	by := queued.Add(policy.MaxWait)
	if !deadline.IsZero() && deadline.Before(by) {
		by = deadline
	}
	// First the turn: one push to the repository in flight from this host at
	// a time, because a push to GitHub can outlast any slot.
	told := false
	release, held := holdInflight(policy, repo, by, func(waited time.Duration) {
		if !told && waited >= policy.NoticeAfter {
			told = true
			notify(PushEvent{Kind: PushWaiting, Repo: repo, At: queued, Wait: waited, Bound: policy.MaxWait})
		}
	})
	if !held {
		// The bound passed with another push still in flight (or the lock
		// could not be taken at all): this one goes now, unpaced, and says so.
		notify(PushEvent{Kind: PushOverdue, Repo: repo, At: queued, Wait: time.Since(queued), Bound: policy.MaxWait})
		return release
	}
	// Then the slot, read and written while this push holds the turn, so the
	// history the next push paces against is the starts that happened.
	now := time.Now()
	if slot, err := reservePush(policy, repo, now); err == nil && slot.After(now) {
		wait := slot.Sub(now)
		switch {
		case slot.After(by):
			notify(PushEvent{Kind: PushOverdue, Repo: repo, At: queued, Wait: slot.Sub(queued), Bound: policy.MaxWait})
			wait = time.Until(by)
		case !told && wait >= policy.NoticeAfter:
			notify(PushEvent{Kind: PushWaiting, Repo: repo, At: queued, Wait: slot.Sub(queued), Bound: policy.MaxWait})
		}
		if wait > 0 {
			time.Sleep(wait)
		}
	}
	return release
}

// pushLogEntry is one push start in the host's push log.
type pushLogEntry struct {
	At    int64  `json:"at"`
	Owner string `json:"owner"`
}

// pushLogKeep is how far back the log keeps starts: past the trailing minute
// and the five seconds after a push a later failure looks at.
const pushLogKeep = 2 * time.Minute

// logPush appends one start to the repository's push log, best effort.
func logPush(policy PushPolicy, repo, owner string, at time.Time) {
	_ = withPushLog(policy, repo, func(entries []pushLogEntry) []pushLogEntry {
		kept := entries[:0]
		for _, e := range entries {
			if at.Sub(time.Unix(0, e.At)) < pushLogKeep {
				kept = append(kept, e)
			}
		}
		return append(kept, pushLogEntry{At: at.UnixNano(), Owner: owner})
	})
}

// pushContext reads the log around a push that started at `at`.
func pushContext(policy PushPolicy, repo, owner string, at time.Time) *PushContext {
	var ctx *PushContext
	err := withPushLog(policy, repo, func(entries []pushLogEntry) []pushLogEntry {
		ctx = contextOf(entries, owner, at)
		return entries
	})
	if err != nil {
		return nil
	}
	return ctx
}

// contextOf is the reading itself, apart from the file.
func contextOf(entries []pushLogEntry, owner string, at time.Time) *PushContext {
	ctx := &PushContext{}
	for _, e := range entries {
		started := time.Unix(0, e.At)
		gap := started.Sub(at)
		if gap <= 0 && gap > -time.Minute {
			ctx.TrailingMinute++
		}
		if e.Owner == owner || gap < -OtherWindow || gap > OtherWindow {
			continue
		}
		if ctx.Other == "" || abs(gap) < abs(ctx.OtherGap) {
			ctx.Other, ctx.OtherGap = e.Owner, gap
		}
	}
	return ctx
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// withPushLog runs change over the repository's push log under its lock and
// writes back what it answers.
func withPushLog(policy PushPolicy, repo string, change func([]pushLogEntry) []pushLogEntry) error {
	dir, err := queueDir(policy)
	if err != nil {
		return err
	}
	base := filepath.Join(dir, queueFileName(repo)+".pushes")
	unlock, err := lockQueue(base + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	var entries []pushLogEntry
	if raw, err := os.ReadFile(base + ".json"); err == nil {
		_ = json.Unmarshal(raw, &entries)
	}
	return writeJSON(base+".json", change(entries))
}

func writeJSON(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// holdInflight takes the repository's in-flight lock, waiting for it no later
// than by and telling waiting how long it has waited as it does, and answers
// the function that lets go of it. A lock that cannot be taken by then — or
// at all — answers held false and a release that does nothing.
func holdInflight(policy PushPolicy, repo string, by time.Time, waiting func(time.Duration)) (func(), bool) {
	dir, err := queueDir(policy)
	if err != nil {
		return func() {}, false
	}
	path := filepath.Join(dir, queueFileName(repo)+".inflight")
	began := time.Now()
	for {
		release, held, err := tryLockQueue(path)
		if err != nil {
			return func() {}, false
		}
		if held {
			return release, true
		}
		if !time.Now().Before(by) {
			return func() {}, false
		}
		waiting(time.Since(began))
		time.Sleep(20 * time.Millisecond)
	}
}

func queueDir(policy PushPolicy) (string, error) {
	dir := policy.Dir
	if dir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(cache, "ticfac", "push-queue")
	}
	return dir, os.MkdirAll(dir, 0o755)
}

// PushJitter sleeps a uniformly random time below the policy's FoldJitter
// before a push to remote that every run on the host is likely to make at the
// same moment — the base-refresh fold after a merge to main, which hn6 and 43y
// made 140ms apart, and one of the two pushes failed "(failed)". It applies
// whether or not the queue paces, and answers the time slept: zero for a
// remote that is not a hosted repository.
func PushJitter(dir, remote string) time.Duration {
	policy, enabled, _ := currentPushPolicy()
	if !enabled || policy.FoldJitter <= 0 {
		return 0
	}
	if _, ok := pushRepository(dir, remote, policy.PaceLocal); !ok {
		return 0
	}
	d := time.Duration(rand.Int64N(int64(policy.FoldJitter)))
	time.Sleep(d)
	return d
}

// FullJitter is a uniformly random duration in [0, d): AWS's "full jitter",
// for a retry that must not land in step with everybody else's.
func FullJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(d)))
}

// isPush reads the subcommand out of an argv the way runstate.RemoteSubcommand
// does: leading global flags, and the values of the ones that take one, are
// skipped.
func isPush(args []string) bool {
	_, rest := subcommand(args)
	return rest != nil
}

// subcommand answers the argv's subcommand and, for a push, the words after
// it (non-nil); nil for anything else.
func subcommand(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			if arg == "push" {
				return arg, append([]string{}, args[i+1:]...)
			}
			return arg, nil
		}
		switch arg {
		case "-c", "-C", "--git-dir", "--work-tree", "--namespace", "--exec-path":
			i++
		}
	}
	return "", nil
}

// pushRemote is the remote a push argv names: the first word after `push`
// that is not an option. A push that names none goes to origin.
func pushRemote(args []string) string {
	_, rest := subcommand(args)
	for i := 0; i < len(rest); i++ {
		word := rest[i]
		if word == "--" {
			if i+1 < len(rest) {
				return rest[i+1]
			}
			break
		}
		if strings.HasPrefix(word, "-") {
			switch word {
			case "-o", "--push-option", "--receive-pack", "--exec", "--repo":
				i++
			}
			continue
		}
		return word
	}
	return "origin"
}

// remoteURLs caches a remote name's URL per directory: a run pushes hundreds
// of times and the answer does not move under it.
var remoteURLs sync.Map

// pushRepository is the queue key for a push to remote from dir, and whether
// the queue paces it at all.
func pushRepository(dir, remote string, paceLocal bool) (string, bool) {
	url := remote
	if !looksLikeURL(remote) {
		key := dir + "\x00" + remote
		if cached, ok := remoteURLs.Load(key); ok {
			url = cached.(string)
		} else {
			cmd := exec.Command(Path(), "remote", "get-url", remote)
			cmd.Dir = dir
			out, err := cmd.Output()
			if err != nil {
				return "", false
			}
			url = strings.TrimSpace(string(out))
			remoteURLs.Store(key, url)
		}
	}
	return RepositoryKey(url, paceLocal)
}

func looksLikeURL(remote string) bool {
	return strings.Contains(remote, "://") || strings.Contains(remote, ":") ||
		strings.HasPrefix(remote, "/") || strings.HasPrefix(remote, ".")
}

// RepositoryKey is the queue's name for the repository at url: host/owner/name,
// lower-cased, with no scheme, credential, port or .git — so the https and the
// ssh spelling of one repository, and a URL carrying a token, are one queue.
// ok is false for a remote that is not a hosted repository: a path, file://,
// or a host that is this machine or one RFC 2606 reserves for testing; unless
// paceLocal, which keys a path by itself.
func RepositoryKey(url string, paceLocal bool) (string, bool) {
	url = strings.TrimSpace(url)
	local := func() (string, bool) {
		if !paceLocal || url == "" {
			return "", false
		}
		path := strings.TrimPrefix(url, "file://")
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		return "local" + filepath.ToSlash(filepath.Clean(path)), true
	}
	var host, path string
	switch {
	case strings.HasPrefix(url, "file://"):
		return local()
	case strings.Contains(url, "://"):
		_, rest, _ := strings.Cut(url, "://")
		host, path, _ = strings.Cut(rest, "/")
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		if colon := strings.LastIndex(host, ":"); colon >= 0 && !strings.Contains(host, "]") {
			host = host[:colon]
		}
	case strings.Contains(url, ":") && !strings.HasPrefix(url, "/"):
		// scp-like: [user@]host:path
		hostPart, rest, _ := strings.Cut(url, ":")
		if strings.Contains(hostPart, "/") {
			return local()
		}
		host, path = hostPart, rest
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
	default:
		return local()
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "" || !hostedHost(host) {
		return local()
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if path == "" {
		return "", false
	}
	return host + "/" + strings.ToLower(path), true
}

func hostedHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return false
	}
	for _, suffix := range []string{".invalid", ".test", ".example", ".localhost"} {
		if strings.HasSuffix(host, suffix) || host == strings.TrimPrefix(suffix, ".") {
			return false
		}
	}
	return !strings.HasPrefix(host, "127.")
}

// queueState is one repository's reserved slots, as unix nanoseconds, in the
// order they were reserved (which is also time order).
type queueState struct {
	Slots []int64 `json:"slots"`
}

// reservePush reserves repo's next slot at or after now and answers it.
func reservePush(policy PushPolicy, repo string, now time.Time) (time.Time, error) {
	dir, err := queueDir(policy)
	if err != nil {
		return now, err
	}
	base := filepath.Join(dir, queueFileName(repo))
	unlock, err := lockQueue(base + ".lock")
	if err != nil {
		return now, err
	}
	defer unlock()

	var state queueState
	if raw, err := os.ReadFile(base + ".json"); err == nil {
		// A torn or foreign file is an empty queue, not a failed push.
		_ = json.Unmarshal(raw, &state)
	}
	slot, slots := nextSlot(policy, state.Slots, now)
	if err := writeJSON(base+".json", queueState{Slots: slots}); err != nil {
		return now, err
	}
	return slot, nil
}

// nextSlot is the reservation arithmetic, apart from the file: the earliest
// time at or after now that is MinGap after the last reserved slot and has
// fewer than PerWindow slots in the Window before it. It answers that slot
// and the slots to keep, the new one appended and the ones no future slot
// can count dropped.
func nextSlot(policy PushPolicy, slots []int64, now time.Time) (time.Time, []int64) {
	window := policy.Window
	if window <= 0 {
		window = time.Minute
	}
	limit := policy.PerWindow
	if limit <= 0 {
		limit = 1
	}
	kept := make([]int64, 0, len(slots)+1)
	for _, s := range slots {
		if s > now.Add(-window).UnixNano() {
			kept = append(kept, s)
		}
	}
	slot := now.UnixNano()
	if n := len(kept); n > 0 && kept[n-1]+int64(policy.MinGap) > slot {
		slot = kept[n-1] + int64(policy.MinGap)
	}
	for {
		inWindow := 0
		first := -1
		for i, s := range kept {
			if s > slot-int64(window) {
				if first < 0 {
					first = i
				}
				inWindow++
			}
		}
		if inWindow < limit {
			break
		}
		// The slot that has to leave the window before this one fits.
		slot = kept[first+inWindow-limit] + int64(window)
	}
	kept = append(kept, slot)
	return time.Unix(0, slot), kept
}

// queueFileName makes a repository key a file name.
func queueFileName(repo string) string {
	var b strings.Builder
	for _, r := range repo {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
