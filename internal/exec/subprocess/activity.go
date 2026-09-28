package subprocess

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runprogress"
)

// Activity, not time, decides whether a worker is stuck (tick wv2).
//
// epic-6in (2026-09-28): two strong-tier pi workers were stopped by a fixed
// 3600s wall clock while they were working. dz1 attempt 5's transcript has
// events until 08:01:32 and it was stopped at 08:01:38. The two workers left
// +4166/-274 and +1946/-55 lines uncommitted. A wall clock cannot tell a
// worker that is still producing from one that has stopped. What can is what
// the worker is visibly doing:
//
//   - its harness's own session transcript: when the last event was written,
//     and what it was (model output, thinking, a tool call started, a tool
//     result);
//   - the CPU time of the tool processes under the harness, so a 25-minute
//     test suite that is busy is not "quiet";
//   - its worktree and branch: a file written or a commit made.
//
// A worker is STUCK when none of the three moved for StuckAfter. It is then
// nudged once, in its own session, with the evidence; if none of the three
// moves in the StuckAfter after the nudge, it is stopped and settled as
// failed, and the run's existing retry and tier escalation take it from
// there. Each step is an observation that names the evidence, and the
// reconciler puts it on the feed.
//
// The wall clock stays, as a generous runaway backstop only.
//
// Calibration, from the same run: the longest silences in four pi
// transcripts were 3-6 minutes, and one of 26m30s (dz1, a single model call
// that returned and carried on). With StuckAfter = 15m that one is nudged
// at 15m and would have been stopped at 30m only if it had still been
// silent; it answered at 26m30s.

// DefaultStuckAfter is how long a worker may show no activity at all before
// it is nudged, and again after the nudge before it is stopped.
const DefaultStuckAfter = 15 * time.Minute

// cpuFloor is the tool-process CPU that counts as progress within a window:
// 1/180 of the window (5s in 15 minutes), never under 50ms. It is a floor
// and not "any CPU at all" because idle helper processes (caffeinate, an idle
// MCP server) still tick a little.
func cpuFloor(window time.Duration) time.Duration {
	floor := window / 180
	switch {
	case floor < 50*time.Millisecond:
		return 50 * time.Millisecond
	case floor > 5*time.Second:
		return 5 * time.Second
	}
	return floor
}

// ---- process CPU ----------------------------------------------------------

// Proc is one row of the process table: pid, parent, CPU time consumed.
type Proc struct {
	PID  int
	PPID int
	CPU  time.Duration
}

// ProcTable reads the process table. Production reads `ps`; tests hand in a
// table of their own.
type ProcTable func() ([]Proc, error)

// SystemProcs is the process table as `ps -A -o pid=,ppid=,time=` reports it
// — the same columns on macOS and Linux.
func SystemProcs() ([]Proc, error) {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,time=").Output()
	if err != nil {
		return nil, fmt.Errorf("read the process table: %w", err)
	}
	return ParseProcs(out), nil
}

// ParseProcs reads ps's pid/ppid/time columns. A row that does not parse is
// skipped: one odd row must not blind the whole measurement.
func ParseProcs(out []byte) []Proc {
	var procs []Proc
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		cpu, err3 := parseCPUTime(fields[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		procs = append(procs, Proc{PID: pid, PPID: ppid, CPU: cpu})
	}
	return procs
}

// parseCPUTime reads ps's `time` column: [[dd-]hh:]mm:ss[.ff] (Linux prints
// hh:mm:ss, macOS m:ss.ff).
func parseCPUTime(s string) (time.Duration, error) {
	days := 0
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, err
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("cpu time %q", s)
	}
	secs, err := strconv.ParseFloat(parts[len(parts)-1], 64)
	if err != nil {
		return 0, err
	}
	total := time.Duration(days)*24*time.Hour + time.Duration(secs*float64(time.Second))
	unit := time.Minute
	for i := len(parts) - 2; i >= 0; i-- {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return 0, err
		}
		total += time.Duration(n) * unit
		unit *= 60
	}
	return total, nil
}

// TreeCPU sums the CPU time of the processes under root at depth minDepth
// or deeper (root itself is depth 0), and counts them. It walks by pid, from
// the one pid the caller owns — never by matching names.
func TreeCPU(procs []Proc, root, minDepth int) (time.Duration, int) {
	children := map[int][]Proc{}
	for _, p := range procs {
		if p.PID != p.PPID {
			children[p.PPID] = append(children[p.PPID], p)
		}
	}
	var total time.Duration
	count := 0
	var walk func(pid, depth int)
	seen := map[int]bool{}
	walk = func(pid, depth int) {
		for _, c := range children[pid] {
			if seen[c.PID] {
				continue
			}
			seen[c.PID] = true
			if depth+1 >= minDepth {
				total += c.CPU
				count++
			}
			walk(c.PID, depth+1)
		}
	}
	walk(root, 0)
	return total, count
}

// ---- transcripts ----------------------------------------------------------

// EnvTranscriptHome overrides the home directory transcripts are looked for
// under — the tests' fake transcripts, and nothing else.
const EnvTranscriptHome = "TICFAC_TRANSCRIPT_HOME"

// TranscriptEvent is the last event a harness wrote to its session
// transcript.
type TranscriptEvent struct {
	At   time.Time
	Kind string
	// ToolInFlight says the last event started a tool call that has no
	// result yet.
	ToolInFlight bool
	Path         string
}

// transcriptHome is the home directory the harness writes under.
func transcriptHome() string {
	if h := os.Getenv(EnvTranscriptHome); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

var claudeSlug = regexp.MustCompile(`[^A-Za-z0-9]`)

// TranscriptDir is where a harness of the given kind keeps the sessions of a
// working directory, or "" for a harness whose layout is not known.
//
//   - pi (@earendil-works/pi-coding-agent, dist/core/session-manager.js
//     getDefaultSessionDirPath): <agent dir>/sessions/--<cwd without its
//     leading slash, / \ : → ->--, agent dir $PI_CODING_AGENT_DIR or
//     ~/.pi/agent.
//   - claude: <config dir>/projects/<cwd with every non-alphanumeric → ->,
//     config dir $CLAUDE_CONFIG_DIR or ~/.claude.
//
// codex keeps its rollouts by date, not by directory, and is not read.
func TranscriptDir(kind, cwd string) string {
	if cwd == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}
	home := transcriptHome()
	switch kind {
	case "pi":
		agentDir := filepath.Join(home, ".pi", "agent")
		if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" && os.Getenv(EnvTranscriptHome) == "" {
			agentDir = d
		}
		slug := "--" + strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(strings.TrimLeft(cwd, "/\\")) + "--"
		return filepath.Join(agentDir, "sessions", slug)
	case "claude":
		configDir := filepath.Join(home, ".claude")
		if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" && os.Getenv(EnvTranscriptHome) == "" {
			configDir = d
		}
		return filepath.Join(configDir, "projects", claudeSlug.ReplaceAllString(cwd, "-"))
	}
	return ""
}

// LastTranscriptEvent reads the newest session transcript a harness kept for
// a working directory and answers its last event. False when there is none
// to read — a harness whose layout is not known, or one that has written
// nothing yet.
func LastTranscriptEvent(kind, cwd string) (TranscriptEvent, bool) {
	dir := TranscriptDir(kind, cwd)
	if dir == "" {
		return TranscriptEvent{}, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return TranscriptEvent{}, false
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	var files []candidate
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, candidate{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	if len(files) == 0 {
		return TranscriptEvent{}, false
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	event, ok := lastEventIn(files[0].path)
	if !ok {
		// A transcript with no dated event is still a file the harness
		// wrote: its mtime is the honest fallback.
		return TranscriptEvent{At: files[0].mod, Kind: "transcript written", Path: files[0].path}, true
	}
	event.Path = files[0].path
	return event, true
}

// transcriptLine is the union of the pi and claude line shapes this reads.
type transcriptLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// lastEventIn reads the tail of a transcript and answers its last dated
// event.
func lastEventIn(path string) (TranscriptEvent, bool) {
	f, err := os.Open(path)
	if err != nil {
		return TranscriptEvent{}, false
	}
	defer f.Close()
	const tail = 256 << 10
	if info, err := f.Stat(); err == nil && info.Size() > tail {
		_, _ = f.Seek(info.Size()-tail, io.SeekStart)
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return TranscriptEvent{}, false
	}
	var lines [][]byte
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64<<10), tail+1)
	for sc.Scan() {
		lines = append(lines, append([]byte(nil), sc.Bytes()...))
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var line transcriptLine
		if json.Unmarshal(lines[i], &line) != nil || line.Timestamp == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, line.Timestamp)
		if err != nil {
			continue
		}
		kind, inFlight := classify(line)
		return TranscriptEvent{At: at, Kind: kind, ToolInFlight: inFlight}, true
	}
	return TranscriptEvent{}, false
}

// classify names one transcript event.
func classify(line transcriptLine) (string, bool) {
	var blocks []struct {
		Type string `json:"type"`
	}
	role := line.Type
	if line.Message != nil {
		if line.Message.Role != "" {
			role = line.Message.Role
		}
		_ = json.Unmarshal(line.Message.Content, &blocks)
	}
	last := ""
	for _, b := range blocks {
		if b.Type != "" {
			last = b.Type
		}
	}
	switch {
	case role == "toolResult" || last == "tool_result":
		return "tool result", false
	case last == "toolCall" || last == "tool_use":
		return "tool call started", true
	case last == "thinking":
		return "thinking", false
	case role == "assistant":
		return "model output", false
	case role == "user":
		return "prompt", false
	}
	if line.Type != "" {
		return line.Type, false
	}
	return "event", false
}

// ---- the decision ---------------------------------------------------------

// ActivityState is what the watcher keeps between looks, durably, beside the
// attempt: the CPU mark (the tool-process CPU total when it last advanced by
// the floor, and when), when the watcher first looked, and the nudges.
type ActivityState struct {
	FirstSeenAt   time.Time     `json:"first_seen_at"`
	CPUMark       time.Duration `json:"cpu_mark"`
	CPUMarkAt     time.Time     `json:"cpu_mark_at"`
	StuckNudgedAt time.Time     `json:"stuck_nudged_at,omitempty"`
	StuckStopped  bool          `json:"stuck_stopped,omitempty"`
	WipNudgedAt   time.Time     `json:"wip_nudged_at,omitempty"`
	WipCheckedAt  time.Time     `json:"wip_checked_at,omitempty"`
	CheckedAt     time.Time     `json:"checked_at,omitempty"`
}

// Activity is one look at a worker.
type Activity struct {
	Transcript    TranscriptEvent
	HasTranscript bool
	// ToolCPU is the CPU time of the processes under the harness; ToolProcs
	// how many there are; CPUMeasured whether the table could be read.
	ToolCPU     time.Duration
	ToolProcs   int
	CPUMeasured bool
	CPUAt       time.Time // when the tool CPU last advanced by the floor
	BranchAt    time.Time
	WorktreeAt  time.Time
	FirstSeenAt time.Time
	// Status is what the substrate says about the worker, when it says
	// anything (a herdr agent_status); it is named in the evidence, never
	// decided on.
	Status string
}

// ObserveCPU folds one CPU sample into the state: the mark moves when the
// tool processes have used at least the floor since it last moved.
func (s *ActivityState) ObserveCPU(cpu time.Duration, now time.Time, window time.Duration) {
	switch {
	case s.CPUMarkAt.IsZero():
		// The first sample is a baseline, not activity: it says nothing
		// about when the CPU was used. It is dated at the watch's own
		// baseline, the moment the worker was issued or first seen.
		s.CPUMark, s.CPUMarkAt = cpu, s.FirstSeenAt
		if s.CPUMarkAt.IsZero() {
			s.CPUMarkAt = now
		}
	case cpu-s.CPUMark >= cpuFloor(window):
		s.CPUMark, s.CPUMarkAt = cpu, now
	case cpu < s.CPUMark:
		// Tool processes exited and took their CPU with them: the new total
		// is the baseline, and nothing is claimed about when.
		s.CPUMark = cpu
	}
}

// Last is the newest sign of activity, of any kind.
func (a Activity) Last() time.Time {
	last := a.FirstSeenAt
	for _, t := range []time.Time{a.Transcript.At, a.CPUAt, a.BranchAt, a.WorktreeAt} {
		if t.After(last) {
			last = t
		}
	}
	return last
}

// Evidence is the sentence a nudge or a stop carries: every signal and how
// long ago it last moved.
func (a Activity) Evidence(now time.Time) string {
	ago := func(t time.Time) string {
		if t.IsZero() {
			return "never seen"
		}
		return now.Sub(t).Round(time.Second).String() + " ago"
	}
	var parts []string
	if a.HasTranscript {
		what := a.Transcript.Kind
		if a.Transcript.ToolInFlight {
			what += ", with no result yet"
		}
		parts = append(parts, fmt.Sprintf("its transcript's last event was %s (%s)", ago(a.Transcript.At), what))
	} else {
		parts = append(parts, "no session transcript could be read")
	}
	if a.CPUMeasured {
		parts = append(parts, fmt.Sprintf("its %d tool process(es) last used CPU %s (%s in total)",
			a.ToolProcs, ago(a.CPUAt), a.ToolCPU.Round(100*time.Millisecond)))
	} else {
		parts = append(parts, "its process CPU could not be read")
	}
	parts = append(parts, fmt.Sprintf("its worktree last changed %s", ago(a.WorktreeAt)),
		fmt.Sprintf("its branch last moved %s", ago(a.BranchAt)))
	if a.Status != "" {
		parts = append(parts, "the substrate reports it "+a.Status)
	}
	return strings.Join(parts, "; ")
}

// StuckStep is what the watcher does about a look.
type StuckStep int

const (
	StuckNone StuckStep = iota
	StuckNudge
	StuckStop
)

// DecideStuck applies the rule: quiet for `after` → nudge once; still quiet
// `after` past the nudge → stop. Activity after a nudge clears it, so a
// later silence earns a nudge of its own.
func DecideStuck(s *ActivityState, a Activity, now time.Time, after time.Duration) StuckStep {
	last := a.Last()
	if !s.StuckNudgedAt.IsZero() && last.After(s.StuckNudgedAt) {
		s.StuckNudgedAt = time.Time{}
	}
	if now.Sub(last) < after {
		return StuckNone
	}
	if s.StuckNudgedAt.IsZero() {
		return StuckNudge
	}
	if now.Sub(s.StuckNudgedAt) >= after {
		return StuckStop
	}
	return StuckNone
}

// StuckPrompt is the in-session nudge.
func StuckPrompt(evidence string, after time.Duration) string {
	return fmt.Sprintf("You appear stuck: the run has seen no activity from you for %s — %s. "+
		"If a command is hung, stop it and do not start it again the same way. Commit your work in progress "+
		"now, then carry on, or write your report if you cannot. If nothing moves in the next %s you are "+
		"stopped and the tick is retried.", after.Round(time.Second), evidence, after.Round(time.Second))
}

// Observation prefixes: the kinds are the job protocol's closed vocabulary,
// so a stuck nudge is a `heartbeat` (the worker was looked at) and a stuck
// stop an `exited` that say what they are — as a nudge is a `started`.
const (
	stuckNudgePrefix = "appears stuck: "
	stuckStopPrefix  = "stopped as stuck: "
	wipNudgePrefix   = "asked to commit its work in progress: "
)

// StuckNudgeDetail, StuckStopDetail and WipNudgeDetail are the observations.
func StuckNudgeDetail(how, evidence string) string {
	return stuckNudgePrefix + how + " — " + evidence
}

func StuckStopDetail(how, evidence string) string {
	return stuckStopPrefix + how + " — " + evidence
}

func WipNudgeDetail(evidence string) string { return wipNudgePrefix + evidence }

// IsStuckNudge, IsStuckStop and IsWipNudge read them back, for the feed.
func IsStuckNudge(o Observation) bool {
	return o.Kind == ObsHeartbeat && strings.HasPrefix(o.Detail, stuckNudgePrefix)
}

func IsStuckStop(o Observation) bool {
	return o.Kind == ObsExited && strings.HasPrefix(o.Detail, stuckStopPrefix)
}

func IsWipNudge(o Observation) bool {
	return o.Kind == ObsHeartbeat && strings.HasPrefix(o.Detail, wipNudgePrefix)
}

// EnvStuckNudge tells a runner re-prompted by the stuck watch that it is one.
const EnvStuckNudge = "TICFAC_STUCK_NUDGE"

// activityWatch is the local supervisor's watch over its own runner. It
// lives in the supervisor process for the attempt's whole life, so its state
// is memory, not a file.
type activityWatch struct {
	state ActivityState
}

// look takes one look at the runner and answers what to do.
func (w *activityWatch) look(record *attemptRecord, runnerPID int, after time.Duration) (StuckStep, string) {
	now := time.Now()
	a := Activity{FirstSeenAt: w.state.FirstSeenAt}
	if ev, ok := LastTranscriptEvent(record.Runner, record.Worktree); ok {
		a.Transcript, a.HasTranscript = ev, true
	}
	if procs, err := SystemProcs(); err == nil {
		// depth 0 is the runner itself; its tools are below it.
		cpu, n := TreeCPU(procs, runnerPID, 1)
		w.state.ObserveCPU(cpu, now, after)
		a.ToolCPU, a.ToolProcs, a.CPUMeasured, a.CPUAt = cpu, n, true, w.state.CPUMarkAt
	}
	a.BranchAt, a.WorktreeAt = MeasureWork(record.Repo, record.Branch, record.Worktree, now)
	step := DecideStuck(&w.state, a, now, after)
	if step == StuckNone {
		return step, ""
	}
	return step, a.Evidence(now)
}

// ---- the commit-WIP nudge -------------------------------------------------

// The periodic commit-WIP nudge: a worker with a large uncommitted change and
// a branch that has not moved for WipNudgeEvery is asked to commit what it
// has — at most once per WipNudgeEvery. Uncommitted work is exactly what a
// stop throws away, or leaves only on a wip snapshot.
const (
	WipNudgeEvery = 30 * time.Minute
	WipNudgeFiles = 8
	WipNudgeLines = 300
)

// Uncommitted measures a worktree's uncommitted change: files touched
// (tracked and untracked) and lines changed in tracked files.
func Uncommitted(worktree string) (files, lines int, err error) {
	status := exec.Command("git", "status", "--porcelain")
	status.Dir = worktree
	out, err := status.Output()
	if err != nil {
		return 0, 0, err
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) != "" {
			files++
		}
	}
	diff := exec.Command("git", "diff", "--numstat", "HEAD")
	diff.Dir = worktree
	out, err = diff.Output()
	if err != nil {
		return files, 0, nil
	}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		a, _ := strconv.Atoi(f[0])
		d, _ := strconv.Atoi(f[1])
		lines += a + d
	}
	return files, lines, nil
}

// WipNudgeDue says whether the commit-WIP nudge is due, and the evidence.
// measure is called only when the time conditions hold, so a watcher that
// polls every few seconds runs git at most once a minute.
func WipNudgeDue(s *ActivityState, branchAt, now time.Time, measure func() (int, int, error)) (bool, string) {
	if branchAt.IsZero() || now.Sub(branchAt) < WipNudgeEvery {
		return false, ""
	}
	if !s.WipNudgedAt.IsZero() && now.Sub(s.WipNudgedAt) < WipNudgeEvery {
		return false, ""
	}
	if !s.WipCheckedAt.IsZero() && now.Sub(s.WipCheckedAt) < time.Minute {
		return false, ""
	}
	s.WipCheckedAt = now
	files, lines, err := measure()
	if err != nil || (files < WipNudgeFiles && lines < WipNudgeLines) {
		return false, ""
	}
	return true, fmt.Sprintf("%d file(s) and %d line(s) are uncommitted and its branch last moved %s ago",
		files, lines, now.Sub(branchAt).Round(time.Second))
}

// SmallCommitsLine is what every worker prompt says about committing.
const SmallCommitsLine = "Commit small and often as you go: every coherent step is a commit on your branch. " +
	"Uncommitted work is what a stop loses."

// WipPrompt is the commit-WIP nudge's text.
func WipPrompt(branch, evidence string) string {
	return fmt.Sprintf("Commit your work in progress on %s now (%s): small commits, even if unfinished, then "+
		"carry on with the task.", branch, evidence)
}

// MeasureWork reads when a worker's branch last moved and its worktree last
// changed — runprogress's measurement, the one the stall warning and
// `ticfac status` make. A fact that cannot be read is zero.
func MeasureWork(repo, branch, worktree string, now time.Time) (branchAt, worktreeAt time.Time) {
	a := runprogress.Of(repo, branch, worktree, now)
	if a.BranchMovedAt != nil {
		branchAt = *a.BranchMovedAt
	}
	if a.WorktreeChangedAt != nil {
		worktreeAt = *a.WorktreeChangedAt
	}
	return branchAt, worktreeAt
}

// CheckEvery is how often a watcher looks: a tenth of the window, between
// 100ms and 30s — a full look walks the worktree and the process table.
func CheckEvery(after time.Duration) time.Duration {
	every := after / 10
	switch {
	case every < 100*time.Millisecond:
		return 100 * time.Millisecond
	case every > 30*time.Second:
		return 30 * time.Second
	}
	return every
}
