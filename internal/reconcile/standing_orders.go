package reconcile

import (
	"os"
	"regexp"
	"strings"
)

// The repository's standing orders, as the run reads them (tick tyd).
//
// `.tick/config.md` → *Standing orders* is where a person pre-delegates
// decision classes (the ticks skill's "Decide and log"): each class carries a
// default — "decide and log" or "always ask". A worker that stops to ask is
// re-dispatched up the tier ladder; at the ceiling these orders decide who
// answers. A question in a decide-and-log class goes back to a worker told to
// decide it and log the decision; only a question in an always-ask class holds
// for a person.
//
// The orders are PROSE a model reads, so the run's own reading is deliberately
// narrow and fails toward the person: a question is always-ask when it names
// the subject of any class the orders mark always-ask, and everything else is
// decide-and-log. A question that names both is always-ask. The worker still
// reads the orders themselves — they are quoted into its prompt — and may
// answer BLOCKED again naming an always-ask class, which then holds.

// standingOrders is the repository's standing-orders section, read.
type standingOrders struct {
	// Text is the section's own words, quoted into the decide-and-log prompt.
	Text string
	// AlwaysAsk and DecideAndLog are the class phrases the section marks with
	// each default, in the order they appear.
	AlwaysAsk    []string
	DecideAndLog []string
	// Declared says the section names at least one always-ask class. When it
	// does not, the ticks skill's own defaults stand in for it.
	Declared bool
}

// defaultAlwaysAsk are the always-ask classes when a repository declares none:
// the ticks skill's defaults (data deletion, force-pushes, external side
// effects, roadmap changes) plus the two every repository's operator is asked
// about whatever it wrote down — money and credentials.
var defaultAlwaysAsk = []string{
	"spending money", "credentials", "data deletion", "force-pushes",
	"touching a live external system", "removing scope", "roadmap changes",
}

// readStandingOrders reads the standing-orders section of the config at path.
// A missing file or section reads as no orders declared.
func readStandingOrders(path string) standingOrders {
	raw, err := os.ReadFile(path)
	if err != nil {
		return standingOrders{AlwaysAsk: defaultAlwaysAsk}
	}
	return parseStandingOrders(string(raw))
}

var ordersMarker = regexp.MustCompile(`(?i):\s*(?:\*\*|__)?\s*(decide[- ]and[- ]log|always[- ]ask)\s*(?:\*\*|__)?`)

func parseStandingOrders(document string) standingOrders {
	section := markdownSection(document, "standing orders")
	orders := standingOrders{Text: strings.TrimSpace(section)}
	plain := strings.NewReplacer("**", "", "__", "", "`", "").Replace(section)
	last := 0
	for _, m := range ordersMarker.FindAllStringSubmatchIndex(plain, -1) {
		segment := plain[last:m[0]]
		last = m[1]
		// The classes are what follows the last boundary before the marker: a
		// sentence end, a semicolon, an opening parenthesis, a list bullet, or
		// the colon that introduced a list ("Same as ticks (…): library …").
		cut := -1
		for _, sep := range []string{". ", "; ", "(", ": ", "\n"} {
			if i := strings.LastIndex(segment, sep); i+len(sep) > cut && i >= 0 {
				cut = i + len(sep)
			}
		}
		if cut > 0 {
			segment = segment[cut:]
		}
		classes := splitClasses(segment)
		if strings.HasPrefix(strings.ToLower(strings.ReplaceAll(m2(plain, m), "-", " ")), "always") {
			orders.AlwaysAsk = append(orders.AlwaysAsk, classes...)
		} else {
			orders.DecideAndLog = append(orders.DecideAndLog, classes...)
		}
	}
	orders.Declared = len(orders.AlwaysAsk) > 0
	if !orders.Declared {
		orders.AlwaysAsk = defaultAlwaysAsk
	}
	return orders
}

// m2 is the marker's captured default word.
func m2(s string, m []int) string { return s[m[2]:m[3]] }

func splitClasses(segment string) []string {
	var out []string
	for _, part := range strings.Split(segment, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "and ")
		part = strings.TrimPrefix(part, "- ")
		part = strings.Trim(part, " .;:")
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// markdownSection is the body of the first heading whose text contains name
// (case-insensitive), up to the next heading of the same or a higher level.
func markdownSection(document, name string) string {
	lines := strings.Split(document, "\n")
	start, level := -1, 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		depth := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
		if start >= 0 && depth <= level {
			return strings.Join(lines[start:i], "\n")
		}
		if start < 0 && strings.Contains(strings.ToLower(trimmed), name) {
			start, level = i+1, depth
		}
	}
	if start < 0 {
		return ""
	}
	return strings.Join(lines[start:], "\n")
}

// classSubjects maps the words a class phrase is recognised by to the words a
// QUESTION about that class uses. A class phrase none of these recognise is
// matched on its own significant words.
var classSubjects = []struct {
	names    []string
	subjects []string
}{
	{[]string{"money", "spend", "cost", "budget", "billing", "purchase", "paid"},
		[]string{"money", "spend", "spending", "pay", "paid", "payment", "purchase", "buy", "billing", "invoice", "subscription", "cost", "costs", "budget", "$"}},
	{[]string{"credential", "secret", "token", "password"},
		[]string{"credential", "credentials", "secret", "secrets", "token", "tokens", "password", "api key", "private key", "ssh key", "access key"}},
	{[]string{"live", "external", "production", "side effect", "side-effect", "deploy"},
		[]string{"production", "prod", "live system", "live external", "live service", "external system", "external service", "third-party service", "deploy", "deployment", "send email", "send an email"}},
	{[]string{"scope"},
		[]string{"remove scope", "removing scope", "descope", "de-scope", "drop the requirement", "drop a requirement", "cut scope", "out of scope", "skip the acceptance", "drop the acceptance"}},
	{[]string{"roadmap"}, []string{"roadmap"}},
	{[]string{"force-push", "force push", "force-pushes"},
		[]string{"force-push", "force push", "push --force", "push -f", "rewrite history", "rewrite the history"}},
	{[]string{"delet"},
		[]string{"delete data", "deleting data", "data deletion", "drop table", "drop the table", "wipe", "purge", "delete the database", "delete production"}},
	{[]string{"architecture"}, []string{"architecture", "architectural"}},
}

// alwaysAskClass is the always-ask class a question falls in, or "" when it
// falls in none — which makes it decide-and-log.
func (o standingOrders) alwaysAskClass(question string) string {
	q := " " + strings.ToLower(question) + " "
	for _, class := range o.AlwaysAsk {
		if matchesClass(q, strings.ToLower(class)) {
			return class
		}
	}
	return ""
}

func matchesClass(question, class string) bool {
	recognised := false
	for _, entry := range classSubjects {
		named := false
		for _, name := range entry.names {
			if strings.Contains(class, name) {
				named = true
				break
			}
		}
		if !named {
			continue
		}
		recognised = true
		for _, subject := range entry.subjects {
			if containsWord(question, subject) {
				return true
			}
		}
	}
	if recognised {
		return false
	}
	// A class this table does not know is matched on its own words: every
	// word of five letters or more ("migrations", "licensing").
	for _, word := range strings.Fields(class) {
		word = strings.Trim(word, ".,;:()")
		if len(word) >= 5 && containsWord(question, word) {
			return true
		}
	}
	return false
}

// containsWord reports whether the phrase occurs in text on word boundaries
// ("live" must not match "deliver"). Punctuation-only phrases ("$") match
// anywhere.
func containsWord(text, phrase string) bool {
	if phrase == "" {
		return false
	}
	if strings.Trim(phrase, "$") == "" {
		return strings.Contains(text, phrase)
	}
	pattern := `(^|[^a-z0-9])` + regexp.QuoteMeta(phrase) + `($|[^a-z0-9])`
	matched, _ := regexp.MatchString(pattern, text)
	return matched
}
