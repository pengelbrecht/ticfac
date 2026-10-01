package subprocess

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/acceptance"
	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// The report checker (tick 4m6): one linter over the worker's report, built
// on the SAME reader collect uses — ParseReport and ReadFindingsBlock — so
// the worker's check and the collect's verdict can never read the format two
// ways.
//
// It is used three times:
//
//   - by the WORKER, before it stops: every prompt tells it to run
//     `ticfac-exec-subprocess lint-report <path>` and fix what it says;
//   - by the executors, after the worker stops: a report that fails the check
//     is pushed back to the SAME session with the errors, at most
//     MaxLintPushbacks times (the subprocess supervisor re-prompts the
//     runner's session, the herdr executor types into the agent's pane);
//   - by collect, after the pushbacks are spent: a report whose FATAL errors
//     remain settles the attempt failed as missing-result — retried like any
//     attempt that never said what it did, never a hold for a person.
//
// Errors come in two strengths. A FATAL error is one collect cannot read past
// — no STATUS line, a findings block the reader refuses, a review with no
// REVIEW-VERDICT — and it fails the attempt once the pushbacks are spent. Every
// other error is something the reader REPAIRED (a folded key, a normalised
// value) or a rule the record does not enforce (a long title, a claim that
// names no item of the epic): it is pushed back, because the worker can fix it
// in one turn and triage is better for it, but after the bound the report is
// accepted as read. The work is never thrown away over an annotation — the
// lesson of ticks ryv and #61. Warnings are printed and never pushed back.
//
// The invariant the property test pins: any report the linter accepts
// (no errors), collect accepts (no fatal problem) — ReportRefusal's problems
// are, by construction, a subset of LintReport's errors.

// MaxLintPushbacks bounds how often one attempt's report is pushed back.
const MaxLintPushbacks = 2

// EnvLintPushback tells a re-prompted runner which pushback this is (1, 2).
const EnvLintPushback = "TICFAC_LINT_PUSHBACK"

// LintProblem is one thing wrong with a report: where, and what is allowed.
type LintProblem struct {
	Where   string
	Message string
	// Fatal marks a problem collect refuses the report over once the
	// pushbacks are spent.
	Fatal bool
}

func (p LintProblem) String() string {
	if p.Where == "" {
		return p.Message
	}
	return p.Where + ": " + p.Message
}

// LintResult is the checker's answer.
type LintResult struct {
	Errors   []LintProblem
	Warnings []LintProblem
}

// Clean is whether the report has no errors (warnings are allowed).
func (r LintResult) Clean() bool { return len(r.Errors) == 0 }

// Text renders the result as the short list a worker acts on.
func (r LintResult) Text() string {
	var b strings.Builder
	for _, p := range r.Errors {
		b.WriteString("error: " + p.String() + "\n")
	}
	for _, p := range r.Warnings {
		b.WriteString("warning: " + p.String() + "\n")
	}
	return b.String()
}

// LintContext is what the checker knows beyond the report: the role the job
// was dispatched as, and — when they can be read — the epic's acceptance item
// ids and the repository's declared command ids. A nil list means unknown, and
// the checks that need it are skipped rather than guessed.
type LintContext struct {
	Role            string
	AcceptanceItems []string
	CommandIDs      []string
	// Repo is the checkout a relative command path is resolved in.
	Repo string
}

// ------------------------------------------------------------ role contract ---

// roleContract is what each role's prompt asks its report to carry beyond the
// STATUS line and the findings block, and what collect reads: the review's
// REVIEW-VERDICT line (tick b50) — and, for a NOT READY, the detail and the
// blocking findings that make it one the run can act on (notReadyProblems,
// epic-6in). The close-out's contract asks for prose (the
// retro, what was absorbed, what is left open) and no typed evidence line, so
// it has nothing typed to check here; a typed line added to a role's contract
// is added to this table, and the checker, the pushback and collect's refusal
// all pick it up.
type roleContract struct {
	reviewVerdict bool
}

var roleContracts = map[string]roleContract{
	"review-epic": {reviewVerdict: true},
}

// ReportRefusal is collect's half of the checker: the FATAL problems of a
// report, context-free, as sentences. Empty means collect can read the report.
// Every collect implementation settles an attempt whose report still has any
// of these after the pushbacks as missing-result.
func ReportRefusal(role string, report Report) []string {
	var out []string
	for _, p := range fatalProblems(role, report, nil) {
		out = append(out, p.String())
	}
	return out
}

// fatalProblems is the one definition of what collect cannot read past. lines
// is the report's lines, for near-miss hints; nil skips them.
func fatalProblems(role string, report Report, lines []string) []LintProblem {
	var out []LintProblem
	if report.Status == "" {
		msg := "the report has no STATUS line; it must end with exactly one of `STATUS: DONE`, " +
			"`STATUS: DONE_WITH_CONCERNS — <what to double-check>`, `STATUS: NEEDS_CONTEXT — <what you need>`, " +
			"`STATUS: BLOCKED — <why>`"
		if n, line := nearMiss(lines, "STATUS"); n > 0 {
			msg += fmt.Sprintf(" (line %d, %q, is not one of them)", n, line)
		}
		out = append(out, LintProblem{Where: "STATUS", Message: msg, Fatal: true})
	}
	if report.FindingsProblem != "" {
		out = append(out, LintProblem{Where: "findings block", Message: report.FindingsProblem +
			". The block is a JSON array of objects; see the v2 shape in your prompt", Fatal: true})
	}
	if roleContracts[role].reviewVerdict && report.ReviewVerdict == "" {
		msg := "a review-epic report states its judgement as its own line before the STATUS line: " +
			"`REVIEW-VERDICT: READY` or `REVIEW-VERDICT: NOT READY — <what would make it ready>`"
		if n, line := nearMiss(lines, "REVIEW-VERDICT"); n > 0 {
			msg += fmt.Sprintf(" (line %d, %q, is not one of them)", n, line)
		}
		out = append(out, LintProblem{Where: "REVIEW-VERDICT", Message: msg, Fatal: true})
	}
	if roleContracts[role].reviewVerdict && report.ReviewVerdict == ReviewVerdictNotReady {
		out = append(out, notReadyProblems(report)...)
	}
	return out
}

// notReadyProblems is what a NOT READY verdict must carry to be acted on
// (epic-6in, 2026-09-28): what would make the epic ready, on the verdict line,
// and the BLOCKING findings that say it in a form the run can act on. A
// review's NOT READY is the run's to act on — each blocking finding is
// absorbed into the epic and the epic is reviewed again (reconcile's
// review_rounds.go) — so a bare "NOT READY", or one whose reasons are prose
// with no blocking finding, is a verdict nothing can act on: 6in's review
// answered "DONE (NOT READY)", and all its hold could say was "merge it by
// hand or close it".
//
// Both are FATAL: a verdict the run cannot act on is not an answer, and a
// review is cheap to ask again, where a hold is a person.
func notReadyProblems(report Report) []LintProblem {
	var out []LintProblem
	if report.ReviewVerdictDetail == "" {
		out = append(out, LintProblem{Where: "REVIEW-VERDICT", Fatal: true, Message: "`REVIEW-VERDICT: NOT READY` " +
			"carries no detail; say what would make the epic ready after an em dash: " +
			"`REVIEW-VERDICT: NOT READY — <what would make it ready>`"})
	}
	if report.FindingsProblem == "" && !HasBlockingFinding(report.Findings) {
		out = append(out, LintProblem{Where: "REVIEW-VERDICT", Fatal: true, Message: "a NOT READY names its " +
			"blocking findings: every reason the epic is not ready is a finding of severity `high` in the findings " +
			"block — the run absorbs each blocking finding into the epic, fixes it and reviews the epic again, " +
			"and a NOT READY with no high-severity finding is a verdict nothing can act on. Lower severities are " +
			"not reasons: they are filed as backlog"})
	}
	return out
}

// BlockingFinding is whether a review's finding is one of the reasons for its
// NOT READY: a finding of severity high. The definition is the severity and
// nothing else — the one field every finding already carries, required and in
// a closed vocabulary — so the review's prompt, this checker and the run that
// absorbs the finding read "blocking" the same way.
func BlockingFinding(f Finding) bool {
	return f.Severity == FindingSeverityHigh
}

// HasBlockingFinding is whether any of the findings is blocking.
func HasBlockingFinding(findings []Finding) bool {
	for _, f := range findings {
		if BlockingFinding(f) {
			return true
		}
	}
	return false
}

// nearMiss finds the last line that starts like a typed line (any case) but
// did not parse as one, so the error can point at it.
func nearMiss(lines []string, prefix string) (int, string) {
	pattern := regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(prefix) + `\s*:`)
	found, text := 0, ""
	for i, raw := range lines {
		trimmed := strings.Trim(strings.TrimRight(raw, "\r"), decorationCutset)
		if pattern.MatchString(trimmed) {
			found, text = i+1, trimmed
		}
	}
	return found, text
}

// ------------------------------------------------------------------ linting ---

// LintReport checks one report body.
func LintReport(body string, ctx LintContext) LintResult {
	var out LintResult
	lines := strings.Split(body, "\n")
	report := ParseReport(body)
	out.Errors = append(out.Errors, fatalProblems(ctx.Role, report, lines)...)

	block := ReadFindingsBlock(body)
	if block.Problem == "" {
		for _, note := range block.Notes {
			where := fmt.Sprintf("findings[%d].%s", note.Index, note.Key)
			msg := note.Why
			if note.Original != "" {
				msg = fmt.Sprintf("%s (you wrote %q)", note.Why, note.Original)
			}
			out.Errors = append(out.Errors, LintProblem{Where: where, Message: msg})
		}
		for i, title := range block.Titles {
			if n := len([]rune(title)); n > MaxFindingTitle {
				out.Errors = append(out.Errors, LintProblem{Where: fmt.Sprintf("findings[%d].title", i),
					Message: fmt.Sprintf("is %d characters; keep it to %d — one line a person can triage from a "+
						"list, with the detail in body", n, MaxFindingTitle)})
			}
		}
		for i, f := range block.Findings {
			out.lintClaim(i, f, block.Version, ctx)
		}
		switch {
		case !block.Present:
			out.Warnings = append(out.Warnings, LintProblem{Where: "findings block",
				Message: "the report carries none; when you found nothing outside your tick, say so with an " +
					"empty block (```findings v2, then [], then ```), so \"found nothing\" reads differently from \"forgot\""})
		case block.Version == FindingsV1:
			out.Warnings = append(out.Warnings, LintProblem{Where: fmt.Sprintf("findings block (line %d)", block.Line),
				Message: "is the v1 format; it is still read, but write ```findings v2 as your prompt shows"})
		}
	}

	if report.Status != "" {
		if report.Status != StatusDone && report.Detail == "" {
			out.Warnings = append(out.Warnings, LintProblem{Where: "STATUS",
				Message: fmt.Sprintf("%s carries no detail; say what after an em dash: `STATUS: %s — <…>`",
					report.Status, report.Status)})
		}
		if n := lastNonBlank(lines); n > 0 && !statusLine.MatchString(strings.Trim(strings.TrimRight(lines[n-1], "\r"), decorationCutset)) {
			out.Warnings = append(out.Warnings, LintProblem{Where: fmt.Sprintf("line %d", n),
				Message: "the report continues after its final STATUS line; the STATUS line is meant to be the last line"})
		}
	}
	if report.ReviewVerdict != "" && !roleContracts[ctx.Role].reviewVerdict && ctx.Role != "" {
		out.Warnings = append(out.Warnings, LintProblem{Where: "REVIEW-VERDICT",
			Message: fmt.Sprintf("only a review-epic report states a verdict; a %s report's is ignored", ctx.Role)})
	}
	return out
}

// lintClaim checks a finding's done-evidence claim against the epic and the
// repository, where they are known.
func (out *LintResult) lintClaim(i int, f Finding, version int, ctx LintContext) {
	itemField, checkField := "breaks.item", "breaks.check"
	if version == FindingsV1 {
		itemField, checkField = "done_item", "demonstrating_check"
	}
	add := func(where, msg string) {
		p := LintProblem{Where: fmt.Sprintf("findings[%d].%s", i, where), Message: msg}
		if version == FindingsV1 {
			// v1's claim fields were never checked against the epic; the
			// checker says what is wrong without pushing an old-format
			// report back over it.
			out.Warnings = append(out.Warnings, p)
			return
		}
		out.Errors = append(out.Errors, p)
	}
	if ValidFindingDoneItem(f.DoneItem) && ctx.AcceptanceItems != nil && !oneOf(ctx.AcceptanceItems, f.DoneItem) {
		allowed := "the epic marks no [A<n>] items, so omit the claim"
		if len(ctx.AcceptanceItems) > 0 {
			allowed = "the epic's items are " + strings.Join(ctx.AcceptanceItems, ", ")
		}
		add(itemField, fmt.Sprintf("%q is not an acceptance item of the epic; %s", f.DoneItem, allowed))
	}
	if f.DemonstratingCheck == "" || ctx.CommandIDs == nil {
		return
	}
	if oneOf(ctx.CommandIDs, f.DemonstratingCheck) || runnable(f.DemonstratingCheck, ctx.Repo) {
		return
	}
	ids := "the repository declares none"
	if len(ctx.CommandIDs) > 0 {
		ids = "the declared ids are " + strings.Join(ctx.CommandIDs, ", ")
	}
	add(checkField, fmt.Sprintf("%q is neither a declared command id ([testing.commands] or [evidence.commands]; %s) "+
		"nor a runnable command line; name the id, or the exact command that fails", f.DemonstratingCheck, ids))
}

// runnable is whether a check reads as a command line: more than one word,
// and a first word that is an executable on PATH or a path in the checkout.
// Prose ("the unit tests") and bare words fail it.
func runnable(check, repo string) bool {
	words := strings.Fields(check)
	if len(words) < 2 {
		return false
	}
	head := words[0]
	if strings.Contains(head, "/") {
		path := head
		if !filepath.IsAbs(path) && repo != "" {
			path = filepath.Join(repo, path)
		}
		_, err := os.Stat(path)
		return err == nil
	}
	_, err := exec.LookPath(head)
	return err == nil
}

func lastNonBlank(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return i + 1
		}
	}
	return 0
}

// ------------------------------------------------------------------ context ---

// LoadLintContext reads what the checker can know about a job from its
// checkout: the epic's acceptance item ids (from the tick record's parent, or
// the tick itself when it carries the acceptance) and the repository's
// declared command ids (.tick/runners.toml). Anything that cannot be read is
// left unknown (nil) — the checks that need it are skipped, never guessed.
func LoadLintContext(repo, role, tickID string) LintContext {
	ctx := LintContext{Role: role, Repo: repo}
	if repo == "" {
		return ctx
	}
	if items, ok := epicItems(repo, tickID); ok {
		ctx.AcceptanceItems = items
	}
	if cfg, err := runconfig.LoadRepo(repo); err == nil && cfg != nil {
		ids := []string{}
		if cfg.Testing != nil {
			for id := range cfg.Testing.Commands {
				ids = append(ids, id)
			}
		}
		if cfg.Evidence != nil {
			for id := range cfg.Evidence.Commands {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		ctx.CommandIDs = ids
	}
	return ctx
}

type tickRecord struct {
	Parent             string `json:"parent"`
	Type               string `json:"type"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
}

func readTickRecord(repo, id string) (tickRecord, bool) {
	var rec tickRecord
	if id == "" || strings.ContainsAny(id, "/\\") {
		return rec, false
	}
	raw, err := os.ReadFile(filepath.Join(repo, ".tick", "issues", id+".json"))
	if err != nil || json.Unmarshal(raw, &rec) != nil {
		return rec, false
	}
	return rec, true
}

// epicItems is the acceptance item ids of the epic a tick belongs to.
func epicItems(repo, tickID string) ([]string, bool) {
	rec, ok := readTickRecord(repo, tickID)
	if !ok {
		return nil, false
	}
	epic := rec
	if rec.Type != "epic" {
		if rec.Parent == "" {
			return nil, false
		}
		if epic, ok = readTickRecord(repo, rec.Parent); !ok {
			return nil, false
		}
	}
	items, err := acceptance.Parse(epic.AcceptanceCriteria)
	if err != nil {
		return nil, false
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids, true
}

// ----------------------------------------------------------------- prompts ---

// LintCommandName is the checker's binary. It ships beside ticfac and in the
// sandbox image (internal/factory's staged binaries).
const LintCommandName = "ticfac-exec-subprocess"

// LintCommand is the command line a worker runs to check its report.
func LintCommand(bin, resultPath, role, tickID, repo string) string {
	if bin == "" {
		bin = LintCommandName
	}
	cmd := fmt.Sprintf("%s lint-report %s", lintQuote(bin), lintQuote(resultPath))
	if role != "" {
		cmd += " --role " + lintQuote(role)
	}
	if tickID != "" && tickID != "job" {
		cmd += " --tick " + lintQuote(tickID)
	}
	if repo != "" {
		cmd += " --repo " + lintQuote(repo)
	}
	return cmd
}

// LintSection is the prompt section that tells a worker to check its report
// before it stops, the same words on every executor.
func LintSection(command string) string {
	return "## Check your report before you stop\n\n" +
		"When your report is written, run the report checker on it:\n\n" +
		"    " + command + "\n\n" +
		"It is the same reader the run collects your report with. Fix every error it prints and run it again " +
		"until it exits 0; a report that fails it is sent back to you, and one that still fails is treated as " +
		"no report at all. It checks the STATUS line, the findings block field by field, and anything else " +
		"your role must state.\n\n" +
		FindingsV2Section
}

// FindingsV2Section is the findings format as every prompt states it.
const FindingsV2Section = "A discovery outside your job that deserves its own tick goes in a typed findings " +
	"block in the report — never prose, never a tracker write — and when you found nothing, say so with an empty " +
	"block:\n\n" +
	"```findings v2\n" +
	"[\n" +
	"  {\n" +
	"    \"kind\": \"defect\",\n" +
	"    \"title\": \"One line, at most 80 characters\",\n" +
	"    \"severity\": \"medium\",\n" +
	"    \"body\": \"What you found and why it matters.\",\n" +
	"    \"target\": \"owner/name\",\n" +
	"    \"breaks\": {\"item\": \"A3\", \"check\": \"go\"},\n" +
	"    \"evidence\": \"internal/x/y.go:42, or a command and the line it fails with\"\n" +
	"  }\n" +
	"]\n" +
	"```\n\n" +
	"`kind` is what it IS: `defect`, `proposal` (work worth a tick) or `contract-change` (a pinned " +
	"contract-bundle change). `severity` is `low`, `medium` or `high`. Only kind, title and severity are " +
	"required. `target` is the repository it belongs on as owner/name; omit it for this repository. `breaks` " +
	"is optional: the epic's acceptance item this finding breaks, and the check that shows it — a " +
	"[testing.commands] id or a runnable command; omit `breaks` when it breaks no item (never write \"none\"). " +
	"`evidence` is optional: where to look. No findings: an empty array, `[]`.\n\n"

// LintPushbackPrompt is what a worker whose report failed the check is told,
// in its own session.
func LintPushbackPrompt(resultPath, command, problems string) string {
	return "Your report at " + resultPath + " does not pass the report check. The run read it with the same " +
		"checker you can run yourself:\n\n    " + command + "\n\nwhich says:\n\n" + problems + "\n" +
		"Fix each error in the report — rewrite the file at that exact path, keeping its final STATUS line — then " +
		"run the checker again until it exits 0. Do not redo the work itself; only the report needs fixing. " +
		HeadlessLine + "\n"
}

// LintPushbackDetail is the observation a pushback is recorded as: a
// `started` observation (a runner turn did start), recognisable by IsNudge's
// prefix so the feed surfaces it beside the report nudges.
func LintPushbackDetail(n int, what, resultPath, how string, errors int) string {
	return fmt.Sprintf("%spushback %d of %d: %s, and its report at %s failed the report check with %d error(s); %s",
		nudgeDetailPrefix, n, MaxLintPushbacks, what, resultPath, errors, how)
}

func lintQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"$`\\|&;<>()*?[]{}!#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ---------------------------------------------------------------------- CLI ---

// LintMain is `ticfac-exec-subprocess lint-report <path> [--role r] [--tick
// id] [--repo dir] [--pushback]`. The flags default to the job's own
// environment (TICFAC_ROLE, TICFAC_TICK, TICFAC_WORKTREE, else the working
// directory), so a worker inside a job can run it with the path alone. Exit 0
// when the report has no errors, 1 when it has (the list is on stdout), 2 on a
// usage error.
//
// --pushback prints, for a failing report, the whole prompt the run pushes the
// report back with (LintPushbackPrompt) instead of the bare list: the cloud
// worker's entrypoint (image/worker.sh) has no Go supervisor to render it and
// hands this text to the harness's own session as it is, so the local and the
// cloud pushback say the same words (hn6 run_d51a, u5n).
func LintMain(args []string, stdout, stderr io.Writer) int {
	role, tick, repo := os.Getenv("TICFAC_ROLE"), os.Getenv("TICFAC_TICK"), os.Getenv("TICFAC_WORKTREE")
	var path string
	pushback := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if !strings.HasPrefix(arg, "-") {
			if path != "" {
				fmt.Fprintf(stderr, "lint-report: one report path, not %q and %q\n", path, arg)
				return ExitUsage
			}
			path = arg
			continue
		}
		if name == "h" || name == "help" {
			fmt.Fprint(stdout, lintUsage)
			return ExitOK
		}
		if name == "pushback" && !hasValue {
			pushback = true
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				fmt.Fprintf(stderr, "lint-report: %s needs a value\n", arg)
				return ExitUsage
			}
			i++
			value = args[i]
		}
		switch name {
		case "role":
			role = value
		case "tick":
			tick = value
		case "repo":
			repo = value
		default:
			fmt.Fprintf(stderr, "lint-report: unknown flag %s\n", arg)
			return ExitUsage
		}
	}
	if path == "" {
		path = os.Getenv("TICFAC_RESULT_PATH")
	}
	if path == "" {
		fmt.Fprint(stderr, lintUsage)
		return ExitUsage
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stdout, "error: %s: the report cannot be read (%v); write it at that exact path\n", path, err)
		return ExitError
	}
	if repo == "" {
		repo, _ = os.Getwd()
	}
	result := LintReport(string(raw), LoadLintContext(repo, role, tick))
	if pushback && !result.Clean() {
		fmt.Fprint(stdout, LintPushbackPrompt(path, LintCommand(LintCommandName, path, role, tick, repo), result.Text()))
		return ExitError
	}
	fmt.Fprint(stdout, result.Text())
	if !result.Clean() {
		fmt.Fprintf(stdout, "%d error(s): fix them and run the check again\n", len(result.Errors))
		return ExitError
	}
	fmt.Fprintln(stdout, "ok: the report passes the check")
	return ExitOK
}

const lintUsage = "usage: ticfac-exec-subprocess lint-report <path> [--role <role>] [--tick <id>] [--repo <dir>] [--pushback]\n"

// ReasonReportInvalid is the message key every collect settles a report with
// FATAL check problems under: not a verdict — it collects as missing-result,
// the closed vocabulary's word for an attempt that never said what it did —
// but its own sentence, because "no report" and "a report nobody can read,
// and here is why" send a person looking at different things.
const ReasonReportInvalid = "report-invalid"

// ReportRefusalMessage is the sentence a report with fatal check problems is
// refused in, the same on every executor.
func ReportRefusalMessage(path string, problems []string) string {
	return fmt.Sprintf("the report at %s fails the report check, so collect cannot read it (it is retried like "+
		"a missing report, never held): %s", path, strings.Join(problems, "; "))
}
