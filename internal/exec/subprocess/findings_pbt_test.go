package subprocess

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"hegel.dev/go/hegel"
)

// Properties of the findings parser (tick 89g): a block a worker wrote is
// either READ or REFUSED, never half-read. "Arbitrary JSON never silently
// loses a finding" is the whole of the channel's reason to exist (tick 7vn):
// a finding that arrives in a report and reaches neither the parsed list nor
// a problem is a discovery that vanished, and the only thing a triager can
// do about those is never learn they happened.
//
// The generator therefore draws REPORTS, not just findings:
//
//   - a preamble of arbitrary prose, sometimes quoting a DECOY findings block
//     of the shape the prompt template has — the last complete block is the
//     one that counts, and a template quoted above the answer is the live
//     shape of that mistake;
//   - one block whose fence tag is drawn (untagged, v1, v2) and whose items
//     are drawn CLEAN (guaranteed to parse), LOUD (guaranteed to refuse the
//     whole block) or arbitrary objects the reader may repair;
//   - sometimes a corruption of the block itself — an unclosed fence, content
//     after the array, an item that is not an object — each of which must be
//     a problem, never silence.
//
// The properties, one per direction of the mistake:
//
//   - NOTHING VANISHES: every item the generator drew with a title either
//     appears in the parsed list, or the block has a problem. Never neither.
//   - NO FALSE REFUSAL: a block drawn entirely from the clean pools parses,
//     its findings come back in the order the worker wrote them, and every
//     key an item carried is readable back out of the finding — in the
//     record, or folded into the body the way tick ryv promised.
//   - NO SILENT CORRUPTION: an unclosed fence, content after the array and a
//     non-object item are problems, never empty answers.
//   - THE LAST BLOCK WINS: the decoy the preamble quoted never shadows the
//     worker's answer.
//   - ONE READER: ParseFindings and ReadFindingsBlock say the same thing
//     about the same report — the checker and the collect can never disagree.

// pbtReport is one drawn report and what the generator knows about it.
type pbtReport struct {
	body   string
	titles []string // the titles the drawn items carried, in array order
	// carried is, per item, every key that item carried and the value as the
	// parser folds it — what a surviving finding must still be able to say.
	carried [][]pbtCarried
	// clean says every item came from the clean pools: the block must parse.
	clean bool
	// loud says the block was drawn to be refused.
	loud bool
	// unclosed says the fence never closes: the block carries no COMPLETE
	// block, so Present is legitimately false — the refusal is the Problem.
	unclosed bool
}

// pbtCarried is one key one item carried, rendered the way foldValue renders
// it: the string itself for a JSON string, compact JSON otherwise.
type pbtCarried struct{ key, value string }

// pbtTitleWords is where titles come from: real words, so a folded body a
// person reads is what the property is checked against.
var pbtTitleWords = []string{"a gate that refuses nothing", "the width drifts", "a lost ack", "an unaskable remote"}

func pbtGenTitle(tc hegel.TestCase, n int) string {
	parts := make([]string, 0, 2)
	for range 1 + hegel.Draw(tc, hegel.Integers(0, 1)) {
		parts = append(parts, hegel.Draw(tc, hegel.SampledFrom(pbtTitleWords)))
	}
	return fmt.Sprintf("%s %d", strings.Join(parts, " and "), n)
}

func pbtGenString(tc hegel.TestCase) string {
	return hegel.Draw(tc, hegel.Text().MinSize(0).MaxSize(80))
}

// pbtGenItem draws ONE v1 item. clean draws only from the pools the reader
// accepts or repairs; otherwise the item is one of the shapes the reader
// must refuse the whole block over. The returns are the marshalled item, the
// title it carries ("" when none), whether the block must be refused over it,
// and the key/value pairs a surviving finding must still carry.
func pbtGenItem(tc hegel.TestCase, clean bool, n int) (json.RawMessage, string, bool, []pbtCarried) {
	item := map[string]json.RawMessage{}
	marshal := func(v any) json.RawMessage {
		raw, _ := json.Marshal(v)
		return raw
	}
	var carried []pbtCarried
	remember := func(key string, value json.RawMessage) {
		item[key] = value
		carried = append(carried, pbtCarried{key: key, value: foldValue(value)})
	}
	if clean {
		title := pbtGenTitle(tc, n)
		remember("title", marshal(title))
		target := hegel.Draw(tc, hegel.SampledFrom([]string{"", "owner/example", "owner/example (the bundle)", "no repository at all"}))
		// An upstream tick with no target is the one value pair the record
		// refuses rather than repairs — the kind names another repository, so
		// a finding that names none is routed nowhere — so the clean pool
		// draws the kind from the kinds an empty target can carry.
		kinds := FindingKinds
		if target == "" {
			kinds = []string{FindingKindProposedTick, FindingKindContract, FindingKindDefect}
		}
		kind := hegel.Draw(tc, hegel.SampledFrom(kinds))
		if hegel.Draw(tc, hegel.WeightedBooleans(0.25)) { // repaired into defect, never refused
			kind = hegel.Draw(tc, hegel.SampledFrom([]string{"bug", "note", ""}))
		}
		remember("kind", marshal(kind))
		remember("body", marshal(pbtGenString(tc)))
		severity := hegel.Draw(tc, hegel.SampledFrom(FindingSeverities))
		if hegel.Draw(tc, hegel.WeightedBooleans(0.25)) { // repaired into medium, never refused
			severity = "urgent"
		}
		remember("severity", marshal(severity))
		remember("target", marshal(target))
		switch hegel.Draw(tc, hegel.Integers(0, 2)) {
		case 0: // a done claim the run can key
			remember("done_item", marshal(fmt.Sprintf("A%d", hegel.Draw(tc, hegel.Integers(1, 12)))))
			if hegel.Draw(tc, hegel.Booleans()) {
				remember("demonstrating_check", marshal("go"))
			}
		case 1: // the reporter's explicit none
			remember("done_item", marshal(FindingDoneItemNone))
		}
		if hegel.Draw(tc, hegel.WeightedBooleans(0.5)) { // a key the record does not know: kept, folded, noted
			var value json.RawMessage
			switch hegel.Draw(tc, hegel.Integers(0, 2)) {
			case 0:
				value = marshal(pbtGenString(tc))
			case 1:
				value = marshal(hegel.Draw(tc, hegel.Integers(-3, 99)))
			default:
				value = marshal(map[string]any{"file": "x.go", "line": hegel.Draw(tc, hegel.Integers(1, 400))})
			}
			remember(hegel.Draw(tc, hegel.SampledFrom([]string{"title_note", "where", "score"})), value)
		}
		return marshal(item), title, false, carried
	}
	switch hegel.Draw(tc, hegel.Integers(0, 3)) {
	case 0: // an empty title: the one thing no default names
		for _, name := range findingFields {
			remember(name, marshal(""))
		}
		return marshal(item), "", true, nil
	case 1: // a required key missing
		missing := hegel.Draw(tc, hegel.SampledFrom(findingFields))
		for _, name := range findingFields {
			if name == missing {
				continue
			}
			remember(name, marshal(""))
		}
		return marshal(item), "", true, nil
	case 2: // a done item the run cannot key to the epic's items
		for _, name := range findingFields {
			remember(name, marshal(""))
		}
		remember("done_item", marshal("item three"))
		return marshal(item), "", true, nil
	default: // a value of the wrong JSON type
		for _, name := range findingFields {
			remember(name, marshal(""))
		}
		typed := hegel.Draw(tc, hegel.SampledFrom(findingFields))
		item[typed] = marshal(7)
		return marshal(item), "", true, nil
	}
}

// pbtGenReport draws one report body: an optional preamble that may quote a
// decoy block, then the block under test, then optional prose after it.
func pbtGenReport(tc hegel.TestCase) pbtReport {
	report := pbtReport{clean: true}
	var block strings.Builder
	if hegel.Draw(tc, hegel.Booleans()) {
		fmt.Fprintf(&block, "## Report\n\n%s\n\n", pbtGenString(tc))
	}
	if hegel.Draw(tc, hegel.WeightedBooleans(0.4)) {
		// The prompt's own template, quoted above the answer: the shape the
		// "final block wins" rule exists for.
		block.WriteString("The prompt says:\n\n```findings\n[{\"kind\": \"defect\", \"title\": \"decoy zero\", " +
			"\"body\": \"\", \"severity\": \"low\", \"target\": \"\"}]\n```\n\n")
	}
	version := hegel.Draw(tc, hegel.SampledFrom([]string{"", " v1"}))
	fmt.Fprintf(&block, "```findings%s\n[", version)
	for i, n := 0, hegel.Draw(tc, hegel.Integers(0, 4)); i < n; i++ {
		if i > 0 {
			block.WriteString(",")
		}
		item, title, loud, carried := pbtGenItem(tc, !hegel.Draw(tc, hegel.WeightedBooleans(0.3)), i)
		block.Write(item)
		if title != "" {
			report.titles = append(report.titles, title)
		}
		report.carried = append(report.carried, carried)
		if loud {
			report.loud, report.clean = true, false
		}
	}
	block.WriteString("]")
	switch hegel.Draw(tc, hegel.Integers(0, 9)) {
	case 0: // the fence never closes: a truncated block is a problem
		report.body = block.String()
		report.loud, report.clean, report.unclosed = true, false, true
		report.titles, report.carried = nil, nil
		return report
	case 1: // content after the array: the strict decode refuses it
		block.WriteString("\nand then some\n```\n")
		report.loud, report.clean = true, false
	case 2: // an item that is not an object at all
		report.body = fmt.Sprintf("```findings%s\n[7, {\"kind\": \"defect\", \"title\": \"beside a number\", "+
			"\"body\": \"\", \"severity\": \"low\", \"target\": \"\"}]\n```\n", version)
		report.loud, report.clean, report.titles, report.carried = true, false, nil, nil
		return report
	default:
		block.WriteString("\n```\n")
	}
	if hegel.Draw(tc, hegel.Booleans()) {
		fmt.Fprintf(&block, "\n%s\n", pbtGenString(tc))
	}
	report.body = block.String()
	return report
}

func TestPBTTheFindingsBlockNeverSilentlyLosesAFinding(t *testing.T) {
	t.Parallel()
	var cases, parsed, problems int
	hegel.Test(t, func(ht *hegel.T) {
		report := pbtGenReport(ht)
		cases++
		block := ReadFindingsBlock(report.body)

		// NOTHING VANISHES: an item with a title either reaches the parsed
		// list or the block says why it did not.
		got := map[string]bool{}
		for _, finding := range block.Findings {
			got[finding.Title] = true
		}
		for _, title := range report.titles {
			if got[title] || block.Problem != "" {
				continue
			}
			ht.Fatalf("the report carried a finding titled %q that reached neither the parsed list nor a problem:\n%s\n"+
				"block: %+v", title, report.body, block)
		}

		// NO FALSE REFUSAL, and the preservation half of the ryv contract:
		// a block drawn from the clean pools parses, in the worker's order,
		// and every key an item carried is readable back out of the finding.
		if report.clean {
			if block.Problem != "" {
				ht.Fatalf("a well-formed findings block was refused: %q\n%s", block.Problem, report.body)
			}
			if len(block.Findings) != len(report.titles) {
				ht.Fatalf("%d findings came back for a block carrying %d:\n%s", len(block.Findings),
					len(report.titles), report.body)
			}
			for i, finding := range block.Findings {
				if finding.Title != report.titles[i] {
					ht.Fatalf("finding %d came back titled %q, want %q — the worker's order is lost:\n%s",
						i, finding.Title, report.titles[i], report.body)
				}
				if err := finding.Validate(); err != nil {
					ht.Fatalf("the parser produced a finding the record refuses: %v\n%s", err, report.body)
				}
				rendered := finding.Kind + "\n" + finding.Title + "\n" + finding.Severity + "\n" +
					finding.Target + "\n" + finding.DoneItem + "\n" + finding.DemonstratingCheck + "\n" + finding.Body
				for _, carried := range report.carried[i] {
					if carried.value == "" || strings.Contains(rendered, carried.value) {
						continue
					}
					ht.Fatalf("the value of %q (%q) reached neither the record nor the folded body:\n"+
						"finding: %+v\nreport:\n%s", carried.key, carried.value, finding, report.body)
				}
			}
		}

		// NO SILENT CORRUPTION: a truncated block, content after the array
		// and a non-object item are problems, never empty answers. A block
		// whose FENCE closed — the content refusals — is Present as well: the
		// reader must say the block WAS there. The truncated block is the one
		// case Present may deny: the report carries no complete block at all,
		// which is what the field is documented to mean, and the refusal rides
		// the Problem either way.
		if report.loud && block.Problem == "" {
			ht.Fatalf("a block the generator drew to be refused was read in silence:\n%s\nblock: %+v",
				report.body, block)
		}
		if report.loud && !report.unclosed && !block.Present {
			ht.Fatalf("a refused block reads as absent: Present must say it was there:\n%s", report.body)
		}

		// THE LAST BLOCK WINS: the decoy never shadows the answer.
		for _, finding := range block.Findings {
			if strings.HasPrefix(finding.Title, "decoy") {
				ht.Fatalf("a template quoted above the answer won over the worker's own block:\n%s", report.body)
			}
		}

		// ONE READER: the collect's view and the linter's are the same fact.
		list, problem, folded := ParseFindings(report.body)
		if problem != block.Problem {
			ht.Fatalf("ParseFindings and ReadFindingsBlock disagree about the problem: %q vs %q\n%s",
				problem, block.Problem, report.body)
		}
		if len(list) != len(block.Findings) {
			ht.Fatalf("ParseFindings returned %d findings where the block reads %d:\n%s",
				len(list), len(block.Findings), report.body)
		}
		for i := range list {
			if list[i] != block.Findings[i] {
				ht.Fatalf("finding %d differs between the two readers:\n%s\n%+v vs %+v",
					i, report.body, list[i], block.Findings[i])
			}
		}
		var wantFolded []string
		for _, note := range block.Notes {
			wantFolded = append(wantFolded, note.folded())
		}
		if strings.Join(folded, "\n") != strings.Join(wantFolded, "\n") {
			ht.Fatalf("the folded notes differ between the two readers:\n%q\n%q", folded, wantFolded)
		}

		if block.Problem == "" && len(block.Findings) > 0 {
			parsed++
		}
		if block.Problem != "" {
			problems++
		}
	}, hegel.WithTestCases(400))
	t.Logf("%d generated reports: %d parsed with findings, %d refused loudly", cases, parsed, problems)
	if cases == 0 {
		t.Fatal("the generator drew no cases: the property is vacuous")
	}
	if parsed == 0 || problems == 0 {
		t.Errorf("the generator only ever reached one side of the property (%d parsed, %d refused): "+
			"neither half of it is exercised", parsed, problems)
	}
}

// pbtGenV2Item draws one v2 item: only kind, title and severity are required,
// body, target, breaks and evidence are optional, and an unknown key is
// folded like v1's.
func pbtGenV2Item(tc hegel.TestCase, clean bool, n int) (json.RawMessage, string, bool) {
	item := map[string]json.RawMessage{}
	marshal := func(v any) json.RawMessage {
		raw, _ := json.Marshal(v)
		return raw
	}
	put := func(key string, v any) { item[key] = marshal(v) }
	title := pbtGenTitle(tc, n)
	if clean {
		put("kind", hegel.Draw(tc, hegel.SampledFrom(FindingV2Kinds)))
		if hegel.Draw(tc, hegel.WeightedBooleans(0.25)) { // repaired into defect, never refused
			put("kind", "task")
		}
		put("title", title)
		if hegel.Draw(tc, hegel.WeightedBooleans(0.25)) { // repaired into medium, never refused
			put("severity", "urgent")
		} else {
			put("severity", hegel.Draw(tc, hegel.SampledFrom(FindingSeverities)))
		}
		if hegel.Draw(tc, hegel.Booleans()) {
			put("body", pbtGenString(tc))
		}
		if hegel.Draw(tc, hegel.Booleans()) {
			put("target", hegel.Draw(tc, hegel.SampledFrom([]string{"owner/example", "no repository at all"})))
		}
		switch hegel.Draw(tc, hegel.Integers(0, 2)) {
		case 0: // one claim, both halves
			put("breaks", map[string]any{"item": "A3", "check": "go"})
		case 1: // a claim the run can key to nothing: kept, noted, unlinked
			put("breaks", map[string]any{"item": "item three"})
		}
		if hegel.Draw(tc, hegel.Booleans()) {
			put("evidence", "internal/x/y.go:42")
		}
		if hegel.Draw(tc, hegel.WeightedBooleans(0.5)) { // v2 has no such keys: folded, noted
			put(hegel.Draw(tc, hegel.SampledFrom([]string{"done_item", "notes"})), pbtGenString(tc))
		}
		return marshal(item), title, false
	}
	switch hegel.Draw(tc, hegel.Integers(0, 3)) {
	case 0: // a required key missing
		missing := hegel.Draw(tc, hegel.SampledFrom(v2Required))
		for _, name := range v2Required {
			if name != missing {
				put(name, "")
			}
		}
		return marshal(item), "", true
	case 1: // an empty title
		for _, name := range v2Required {
			put(name, "")
		}
		return marshal(item), "", true
	case 2: // a tracker edit the run cannot apply as written
		put("kind", "defect")
		put("title", title)
		put("severity", "low")
		put("tracker_edit", `"not an object"`)
		return marshal(item), title, true
	default: // arbitrary v2-shaped JSON the generator has no expectation about
		put("kind", pbtGenString(tc))
		put("title", title)
		put("severity", pbtGenString(tc))
		if hegel.Draw(tc, hegel.Booleans()) {
			put("breaks", "not an object")
		}
		return marshal(item), title, false
	}
}

func TestPBTAV2FindingsBlockNeverSilentlyLosesAFinding(t *testing.T) {
	t.Parallel()
	var cases, parsed, problems int
	hegel.Test(t, func(ht *hegel.T) {
		var b strings.Builder
		var titles []string
		clean, loud := true, false
		b.WriteString("```findings v2\n[")
		for i, n := 0, hegel.Draw(ht, hegel.Integers(0, 4)); i < n; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			item, title, refuse := pbtGenV2Item(ht, !hegel.Draw(ht, hegel.WeightedBooleans(0.3)), i)
			b.Write(item)
			if title != "" {
				titles = append(titles, title)
			}
			if refuse {
				loud, clean = true, false
			}
		}
		b.WriteString("]\n```\n")
		body := b.String()
		cases++
		block := ReadFindingsBlock(body)

		got := map[string]bool{}
		for _, finding := range block.Findings {
			got[finding.Title] = true
		}
		for _, title := range titles {
			if !got[title] && block.Problem == "" {
				ht.Fatalf("a v2 finding titled %q was lost in silence:\n%s\nblock: %+v", title, body, block)
			}
		}
		if clean && block.Problem != "" {
			ht.Fatalf("a well-formed v2 block was refused: %q\n%s", block.Problem, body)
		}
		if clean && block.Problem == "" && len(block.Findings) != len(titles) {
			ht.Fatalf("%d v2 findings came back for a block carrying %d:\n%s", len(block.Findings), len(titles), body)
		}
		if loud && block.Problem == "" {
			ht.Fatalf("a v2 block drawn to be refused was read in silence:\n%s\nblock: %+v", body, block)
		}
		if block.Version != FindingsV2 {
			ht.Fatalf("a fenced v2 block read as version %d:\n%s", block.Version, body)
		}
		if block.Problem == "" && len(block.Findings) > 0 {
			parsed++
		}
		if block.Problem != "" {
			problems++
		}
	}, hegel.WithTestCases(300))
	t.Logf("%d generated v2 reports: %d parsed with findings, %d refused loudly", cases, parsed, problems)
	if cases == 0 {
		t.Fatal("the generator drew no cases: the property is vacuous")
	}
	if parsed == 0 || problems == 0 {
		t.Errorf("the generator only ever reached one side of the property (%d parsed, %d refused): "+
			"neither half of it is exercised", parsed, problems)
	}
}
