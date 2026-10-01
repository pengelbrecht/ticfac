package subprocess

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// The findings channel: how a worker reports something it discovered OUTSIDE
// its tick without writing the tracker and without prose an orchestrator has
// to read carefully (tick 7vn).
//
// The channel is a machine-readable block in the report the worker already
// writes — a fenced code block with the info string `findings` holding a
// JSON array of typed findings — lifted by collect into the role-result
// envelope's FIRST-CLASS Findings field, where the bundle's $defs.finding
// (since 4.0.0) validates the five pinned closed shapes rather than trusting
// the open result payload. Since tick nfo each finding also carries two
// OPTIONAL done-evidence fields (done_item, demonstrating_check): they ride
// this block and the draft the reconciler files, and join the ENVELOPE — the
// one surface the compiled-in schema validates at runtime — when the
// contract bundle adopts them (see FindingFieldNames and the parity reader
// that pins the pending set). Since tick ryv a key the record does not know
// is no longer a refusal: it is FOLDED into the finding's body as a labelled
// line and named in the attempt's records, so a finished tick's work is
// never thrown away over an annotation — while a missing required field or
// an invalid value still refuses, because those are the cases where the
// record can mean nothing by what it read.
//
// The reconciler turns each finding into a DRAFT tick proposal; the worker
// never writes `.tick/`, which is a protected prefix, and the reconciler
// never opens a tick on a worker's word — the draft is triaged by a person,
// which is what keeps the scope decision human.
//
// The block is deliberately INSIDE the report rather than a second file: the
// report is the one deliverable collect already reads durably (off the branch
// when the worktree is gone), and a second channel is a second thing a worker
// can forget and a second reader the vocabulary drifts in.

// The closed vocabulary of finding kinds. A kind says what the finding IS, not
// where it goes — `target` says where it goes.
//
//	proposed-tick  a tick this repository should carry (the v3i shape: a
//	               blocked worker proposing the split it could not make)
//	upstream-tick  a tick ANOTHER repository should carry (the 604 shape: an
//	               upstream finding the orchestrator had to read prose to
//	               file) — requires a target repository
//	contract       a pinned contract bundle change (the 8ug shape)
//	defect         a defect outside this tick's scope that nobody dispatched
//	               work for
const (
	FindingKindProposedTick = "proposed-tick"
	FindingKindUpstreamTick = "upstream-tick"
	FindingKindContract     = "contract"
	FindingKindDefect       = "defect"
)

// FindingKinds is the closed kind vocabulary.
var FindingKinds = []string{
	FindingKindProposedTick, FindingKindUpstreamTick, FindingKindContract, FindingKindDefect,
}

// The closed severity vocabulary. A severity is a worker's claim about how much
// the finding matters; triage is where it is acted on, and a severity nobody
// validates is a severity nobody can sort by.
const (
	FindingSeverityLow    = "low"
	FindingSeverityMedium = "medium"
	FindingSeverityHigh   = "high"
)

// FindingSeverities is the closed severity vocabulary.
var FindingSeverities = []string{
	FindingSeverityLow, FindingSeverityMedium, FindingSeverityHigh,
}

// FindingDoneItemNone is the reporter's answer that the finding breaks no
// item of the epic's definition of done (tick nfo): a CLAIM, deliberately
// distinct from reporting no done_item at all. The one is an answer the
// run can score; the other is a finding nobody linked, and the two must not
// look identical.
const FindingDoneItemNone = "none"

// targetRepositoryPattern is the shape of a finding's target: an owner/name
// GitHub repository, e.g. pengelbrecht/ticks. An upstream finding belongs on a
// DIFFERENT tracker, so the target names which one; an empty target means the
// repository the run is working on.
var targetRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Finding is one thing a worker discovered outside its tick. The five pinned
// fields are REQUIRED in the report block — kind, title, body, severity and
// the target repository — with body and target allowed to be empty, so "no
// target" and "target forgotten" cannot look identical.
//
// Two more fields carry the finding's EVIDENCE against the epic's definition
// of done (tick nfo), and they are OPTIONAL — a finding missing them is
// accepted and reads as UNLINKED, because refusing findings nobody thought
// to link is how a channel loses them. The reporter's claim is never the
// verdict on whether the finding gates its epic: the named check is what
// the run runs where the item is runnable (the done check is the
// authoritative verdict), one input to a prediction where it is not yet, and
// the claim is what the reporter is later scored against — evidence, not
// judgement.
type Finding struct {
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Severity string `json:"severity"`
	Target   string `json:"target"`
	// DoneItem is the acceptance item of the EPIC being worked — an [A<n>]
	// id as the epic's acceptance criteria mark its items — that the
	// reporter believes this finding breaks, or "none" when the reporter
	// believes it breaks none. Empty means no claim was made: UNLINKED.
	DoneItem string `json:"done_item,omitempty"`
	// DemonstratingCheck is the command or test the reporter says would
	// demonstrate the breakage — the id of one of the repository's declared
	// testing commands where one fits, else the test's name. Empty means no
	// claim was made.
	DemonstratingCheck string `json:"demonstrating_check,omitempty"`
	// TrackerEdit is the exact tracker change that IS the finding's fix,
	// when its fix is purely tracker-side (a v2 finding's `tracker_edit`,
	// hn6 yjq): the run applies it itself rather than filing a tick no worker
	// could do. It rides beside the record, never in it — the pinned
	// $defs.finding and the draft are unchanged — so it is not serialised.
	TrackerEdit *TrackerEdit `json:"-"`
}

// findingFields is every REQUIRED field of the record, for the closed-key
// check: a findings block carrying a field this record does not have is
// refused rather than read as if it were smaller.
var findingFields = []string{"kind", "title", "body", "severity", "target"}

// knownFindingField is the closed key set of the finding record: the five
// required fields plus the two optional evidence fields.
var knownFindingField = map[string]bool{
	"kind": true, "title": true, "body": true, "severity": true, "target": true,
	"done_item": true, "demonstrating_check": true,
}

// FindingFieldNames is every field the report-block record answers to, in
// alphabetical order. The parity reader pins this set against the bundle's
// $defs.finding: a bundle field ticfac cannot read is a refusal nobody
// issued, and a field ticfac reads that the bundle has not pinned yet is a
// pending bump this repository has to name rather than drift behind.
func FindingFieldNames() []string {
	names := make([]string, 0, len(knownFindingField))
	for name := range knownFindingField {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Validate refuses a finding this channel will not carry. Every refusal names
// the field, because "the finding was invalid" is the message that sends the
// next worker at the wrong problem.
func (f Finding) Validate() error {
	if !oneOf(FindingKinds, f.Kind) {
		return fmt.Errorf("finding.kind %q is not one of %s", f.Kind, strings.Join(FindingKinds, ", "))
	}
	if strings.TrimSpace(f.Title) == "" {
		return fmt.Errorf("finding.title is empty: a finding nobody can name is one nobody can triage")
	}
	if !oneOf(FindingSeverities, f.Severity) {
		return fmt.Errorf("finding.severity %q is not one of %s", f.Severity, strings.Join(FindingSeverities, ", "))
	}
	if f.Target != "" && !targetRepositoryPattern.MatchString(f.Target) {
		return fmt.Errorf("finding.target %q is not an owner/name repository", f.Target)
	}
	if f.Kind == FindingKindUpstreamTick && f.Target == "" {
		return fmt.Errorf("finding.target is empty: an upstream tick belongs on another repository's tracker, " +
			"and a finding that names none is routed nowhere")
	}
	return ValidateDoneItem(f.DoneItem)
}

// ValidateDoneItem refuses a done_item the channel will not carry: an
// acceptance item id — the epic's [A<n>] marks, the same shape
// [evidence.acceptance] keys on — the reporter's "none", or empty (no claim
// made). runstate reuses this for the draft half of the channel, so the two
// records cannot disagree about what a claim looks like.
func ValidateDoneItem(doneItem string) error {
	if doneItem == "" || doneItem == FindingDoneItemNone || ValidFindingDoneItem(doneItem) {
		return nil
	}
	return fmt.Errorf("finding.done_item %q is neither an acceptance item id (%s) nor %q: a done item a "+
		"reader cannot key to the epic's items is a link nobody can follow", doneItem,
		runconfig.AcceptanceItemPattern.String(), FindingDoneItemNone)
}

// ValidFindingDoneItem reports whether doneItem is an acceptance item id —
// the reporter's link to an item, neither empty (no claim made) nor the
// reporter's "none". The draft's linkage mark reads it, so linked, none
// and unlinked stay three states rather than two spellings of two.
func ValidFindingDoneItem(doneItem string) bool {
	return doneItem != "" && doneItem != FindingDoneItemNone &&
		runconfig.AcceptanceItemPattern.MatchString(doneItem)
}

// findingsFence is the opening line of the findings block: a code fence with
// the info string `findings`, optionally followed by the block's format
// version (`findings v2`), and nothing else on the line.
var findingsFence = regexp.MustCompile("^```findings(?:[ \\t]+(v1|v2))?[ \\t]*$")

// anyFence is any code-fence line, which is what CLOSES the findings block.
var anyFence = regexp.MustCompile("^```")

// The two formats of the findings block (tick 4m6). v1 is the format every
// run before 4m6 wrote and every archived report carries, so it is read
// forever; v2 is what the prompts ask for now. See findings_v2.go for the v2
// shape and why it is shaped that way.
const (
	FindingsV1 = 1
	FindingsV2 = 2
)

// FindingsBlock is everything the ONE reader of the findings block learned
// about it — the collect's view (the typed list, or the problem that refuses
// it) and the linter's (where the block is, which format it is, and every
// repair the reader made to an item). ParseFindings and ParseReport are both
// views of this; the linter reads it directly, so the checker and collect
// can never disagree about what a block says (tick 4m6).
type FindingsBlock struct {
	// Present is whether the report carries a complete findings block at all.
	// A block holding `[]` is Present with no findings — an explicit "I found
	// nothing" — while a report with no block is not Present: the two are
	// distinguishable, which is what the empty block is for.
	Present bool
	// Version is FindingsV1 or FindingsV2: the fence's own version when it
	// names one, else the shape of the items (a v2-only key or kind makes an
	// untagged block v2), else v1.
	Version int
	// Line is the 1-based line of the block's opening fence.
	Line int
	// Findings is the typed list, nil when the block is empty or refused.
	Findings []Finding
	// Problem is why the block is refused, empty when it is not.
	Problem string
	// Notes are the repairs the reader made rather than refusing: every key
	// folded into a body and every value normalised, per item.
	Notes []FindingNote
	// Titles are the items' titles as written, for checks the record itself
	// does not refuse (the title length rule).
	Titles []string
}

// FindingNote is one repair the reader made to one item rather than refusing
// the block: an unknown key folded into the body, or a value outside its
// vocabulary normalised (the original value folded into the body).
type FindingNote struct {
	Index    int
	Key      string
	Original string
	// Normalised is true for a value repaired in place, false for a key the
	// record does not know that was folded.
	Normalised bool
	// Why is the reader's explanation, for the linter to show the worker.
	Why string
}

// folded renders a note the way the collection has always named folds
// (tick ryv): `findings[<i>] "<key>"`, and for a normalisation
// `findings[<i>] "<key>" normalised from "<original>"`.
func (n FindingNote) folded() string {
	if n.Normalised {
		return fmt.Sprintf("findings[%d] %q normalised from %q", n.Index, n.Key, n.Original)
	}
	return fmt.Sprintf("findings[%d] %q", n.Index, n.Key)
}

// ParseFindings reads the findings block out of a report body.
//
// The FINAL complete block wins, for the same reason the FINAL status line
// does: a report may quote the template above its own answer. A block that
// opens and never closes is a problem rather than nothing — silently dropping
// a findings block is the exact failure (findings lost) this channel exists
// to remove, so a truncated one is reported as unparseable.
//
// The return is the typed list and, when the report carries a block this
// reader cannot accept, the problem with it. (nil, "", nil) means the report
// carries no findings, whether it has no block or an empty one;
// ReadFindingsBlock says which.
//
// The third return names every finding key the block carried that the
// finding record does not know, one per key, as `findings[<i>] "<key>"`,
// and every value the reader normalised. Such a key is not a problem (tick
// ryv): the 3h0 worker finished its tick and wrote a finding with an extra
// "title_note", and refusing the whole attempt as finding_report_invalid
// threw away work that was fine. The key and its value are FOLDED into the
// finding's body as a labelled line. What still refuses is everything the
// record can mean nothing by: a missing required field, and a value of the
// wrong type.
func ParseFindings(body string) (findings []Finding, problem string, folded []string) {
	block := ReadFindingsBlock(body)
	if block.Problem != "" {
		return nil, block.Problem, nil
	}
	if len(block.Findings) == 0 {
		return nil, "", nil
	}
	for _, note := range block.Notes {
		folded = append(folded, note.folded())
	}
	return block.Findings, "", folded
}

// ReadFindingsBlock is the one reader of the findings block (see
// FindingsBlock).
func ReadFindingsBlock(body string) FindingsBlock {
	var last []string
	var block []string
	lastLine, lastVersion := 0, 0
	openLine, openVersion := 0, 0
	open := false
	for i, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case !open:
			if m := findingsFence.FindStringSubmatch(line); m != nil {
				open, block, openLine = true, nil, i+1
				openVersion = 0
				switch m[1] {
				case "v1":
					openVersion = FindingsV1
				case "v2":
					openVersion = FindingsV2
				}
			}
		case anyFence.MatchString(line):
			// Keep scanning: the LAST complete block is the one that counts.
			open = false
			last, lastLine, lastVersion = block, openLine, openVersion
			if last == nil {
				last = []string{}
			}
		default:
			block = append(block, line)
		}
	}
	if open {
		return FindingsBlock{Line: openLine, Version: versionOr(openVersion),
			Problem: fmt.Sprintf("the report opens a findings block (line %d) and never closes it", openLine)}
	}
	if last == nil {
		return FindingsBlock{}
	}
	out := FindingsBlock{Present: true, Line: lastLine, Version: versionOr(lastVersion)}

	// Strict decode: the array itself is refused on anything but a JSON
	// array, because a findings list a reader half-understands is one that
	// silently loses the half it did not.
	text := strings.Join(last, "\n")
	var raw []json.RawMessage
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	if err := dec.Decode(&raw); err != nil {
		out.Problem = fmt.Sprintf("the findings block%s is not a JSON array: %v", blockLineAt(text, lastLine, err), err)
		return out
	}
	if dec.More() {
		out.Problem = "the findings block carries trailing content after the array"
		return out
	}
	objects := make([]map[string]json.RawMessage, 0, len(raw))
	for i, item := range raw {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item, &fields); err != nil || fields == nil {
			if err == nil {
				err = fmt.Errorf("it is null")
			}
			out.Problem = fmt.Sprintf("findings[%d] is not an object: %v", i, err)
			return out
		}
		objects = append(objects, fields)
	}
	if lastVersion == 0 && looksLikeV2(objects) {
		out.Version = FindingsV2
	}

	findings := []Finding{}
	for i, fields := range objects {
		var (
			finding Finding
			notes   []FindingNote
			problem string
		)
		if out.Version == FindingsV2 {
			finding, notes, problem = readFindingV2(i, fields)
		} else {
			finding, notes, problem = readFindingV1(i, fields)
		}
		if problem != "" {
			return FindingsBlock{Present: true, Line: out.Line, Version: out.Version, Problem: problem}
		}
		if err := finding.Validate(); err != nil {
			return FindingsBlock{Present: true, Line: out.Line, Version: out.Version,
				Problem: fmt.Sprintf("findings[%d]: %v", i, err)}
		}
		var title string
		_ = json.Unmarshal(fields["title"], &title)
		out.Titles = append(out.Titles, title)
		out.Notes = append(out.Notes, notes...)
		findings = append(findings, finding)
	}
	if len(findings) > 0 {
		out.Findings = findings
	}
	return out
}

func versionOr(v int) int {
	if v == 0 {
		return FindingsV1
	}
	return v
}

// blockLineAt names the report line a JSON syntax error in the block points
// at, so the worker is told where to look rather than a byte offset.
func blockLineAt(text string, fenceLine int, err error) string {
	var offset int64 = -1
	switch e := err.(type) {
	case *json.SyntaxError:
		offset = e.Offset
	case *json.UnmarshalTypeError:
		offset = e.Offset
	default:
		if err == io.ErrUnexpectedEOF {
			// The JSON is cut short: the block's closing fence is where it
			// ends, so that is the line to look above.
			return fmt.Sprintf(" (it ends at report line %d before the JSON does)", fenceLine+1+strings.Count(text, "\n")+1)
		}
	}
	if offset < 0 || offset > int64(len(text)) {
		return ""
	}
	return fmt.Sprintf(" (report line %d)", fenceLine+1+strings.Count(text[:offset], "\n"))
}

// readFindingV1 reads one item of a v1 block: the five required fields, the
// two optional evidence fields, unknown keys folded, closed-vocabulary values
// normalised.
func readFindingV1(i int, fields map[string]json.RawMessage) (Finding, []FindingNote, string) {
	for _, name := range findingFields {
		if _, ok := fields[name]; !ok {
			return Finding{}, nil, fmt.Sprintf("findings[%d] omits %q; every field is required, empty included", i, name)
		}
	}
	// The fold (tick ryv): keys the finding record does not know are KEPT
	// — appended to the finding's body as labelled lines, in key order —
	// rather than refusing the attempt over an annotation. The strict
	// decode still runs, over the KNOWN keys only, so what the record does
	// not know is labelled as not known, never guessed at, and what it
	// half-understands still cannot pass for what it reads.
	unknown := make([]string, 0)
	known := make(map[string]json.RawMessage, len(fields))
	for name, value := range fields {
		if knownFindingField[name] {
			known[name] = value
			continue
		}
		unknown = append(unknown, name)
	}
	sort.Strings(unknown)
	filtered, err := json.Marshal(known)
	if err != nil {
		return Finding{}, nil, fmt.Sprintf("findings[%d] could not be read: %v", i, err)
	}
	var finding Finding
	dec := json.NewDecoder(bytes.NewReader(filtered))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&finding); err != nil {
		return Finding{}, nil, fmt.Sprintf("findings[%d]: %v", i, err)
	}
	var notes []FindingNote
	for _, name := range unknown {
		finding.Body = foldIntoBody(finding.Body, name, fields[name])
		notes = append(notes, FindingNote{Index: i, Key: name, Original: foldValue(fields[name]),
			Why: "the v1 finding record has no such field (known: " + strings.Join(FindingFieldNames(), ", ") + ")"})
	}
	notes = append(notes, normalizeFinding(&finding, i)...)
	return finding, notes, ""
}

// targetPrefixPattern finds an owner/name repository at the START of a
// target a worker decorated ("pengelbrecht/ticks (contracts bundle)").
var targetPrefixPattern = regexp.MustCompile(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)(\s.*)?$`)

// normalizeFinding repairs a finding whose closed-vocabulary values a worker
// got slightly wrong, instead of refusing the whole report (and with it the
// tick's work): epic-2jn stopped on 'finding.target "pengelbrecht/ticks
// (contracts bundle)" is not an owner/name repository', a report whose work
// was fine. The original value always rides in the body as a folded line, so
// nothing is lost and a triager sees what the worker wrote:
//   - a target carrying an owner/name prefix keeps the prefix; a target with
//     none becomes this repository (empty);
//   - a kind or severity outside its vocabulary becomes "defect" / "medium".
//
// An empty title is still a refusal: a finding nobody can name is one nobody
// can triage, and no default names it.
func normalizeFinding(f *Finding, index int) []FindingNote {
	var notes []FindingNote
	fold := func(key, value, why string) {
		raw, _ := json.Marshal(value)
		f.Body = foldIntoBody(f.Body, key, raw)
		notes = append(notes, FindingNote{Index: index, Key: key, Original: value, Normalised: true, Why: why})
	}
	if f.Target != "" && !targetRepositoryPattern.MatchString(f.Target) {
		original := f.Target
		if m := targetPrefixPattern.FindStringSubmatch(strings.TrimSpace(original)); m != nil {
			f.Target = m[1]
		} else {
			f.Target = ""
			// An upstream tick names ANOTHER repository by definition; with
			// none recoverable it is a proposal for this one, and says so.
			if f.Kind == FindingKindUpstreamTick {
				f.Kind = FindingKindProposedTick
				fold("kind", FindingKindUpstreamTick, "an upstream tick needs an owner/name target")
			}
		}
		fold("target", original, "target is an owner/name repository, e.g. pengelbrecht/ticks, or empty for this one")
	}
	if !oneOf(FindingKinds, f.Kind) {
		original := f.Kind
		f.Kind = FindingKindDefect
		fold("kind", original, "kind is one of "+strings.Join(FindingKinds, ", "))
	}
	if !oneOf(FindingSeverities, f.Severity) {
		original := f.Severity
		f.Severity = FindingSeverityMedium
		fold("severity", original, "severity is one of "+strings.Join(FindingSeverities, ", "))
	}
	return notes
}

// foldIntoBody appends one unknown key and its value to the finding's body
// as a labelled line (tick ryv): `folded <key>: <value>`. A JSON string rides
// unquoted — the way a person triaging the finding reads it — and anything
// else as compact JSON, so a number, an array or an object the worker
// attached keeps its shape rather than being flattened into prose.
func foldIntoBody(body, key string, value json.RawMessage) string {
	line := "folded " + key + ": " + foldValue(value)
	if body == "" {
		return line
	}
	return body + "\n" + line
}

// foldValue renders one folded value: the string itself when the value is a
// JSON string, compact JSON otherwise.
func foldValue(value json.RawMessage) string {
	var asString string
	if err := json.Unmarshal(value, &asString); err == nil {
		return asString
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return string(value)
	}
	return compact.String()
}
