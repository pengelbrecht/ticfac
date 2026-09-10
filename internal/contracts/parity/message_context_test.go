package parity

import (
	"strings"
	"testing"
)

// contracts/message-context.json — EXECUTABLE.
//
// The fixture pins the one-line context block an operator's message carries:
// the separator, the two prefixes, and the composed line for seven inputs. It
// is composable from the fixture alone, so this reader implements the rule and
// the fixture decides whether ticfac agrees. ticks' operator half and the
// factory's TypeScript half read the same file.
//
// The rule that carries the weight is the last case: an unknown context
// produces NO line, never an empty frame — a message that arrives with an
// empty header reads as though the run lost track of where it is.

const messageContextFile = "message-context.json"

type messageContext struct {
	Separator  string `json:"separator"`
	EpicPrefix string `json:"epic_prefix"`
	TickPrefix string `json:"tick_prefix"`
	Cases      []struct {
		Name    string `json:"name"`
		Context struct {
			Project string `json:"project"`
			Epic    string `json:"epic"`
			Tick    string `json:"tick"`
		} `json:"context"`
		Line string `json:"line"`
	} `json:"cases"`
	PrefixExamples []struct {
		Name    string `json:"name"`
		Context struct {
			Project string `json:"project"`
			Epic    string `json:"epic"`
			Tick    string `json:"tick"`
		} `json:"context"`
		Body     string `json:"body"`
		Prefixed string `json:"prefixed"`
	} `json:"prefix_examples"`
}

// composeLine is ticfac's implementation of the rule: the known parts, in
// order, each trimmed, joined by the separator. Nothing known produces the
// empty string.
func composeLine(c messageContext, project, epic, tick string) string {
	var parts []string
	if p := strings.TrimSpace(project); p != "" {
		parts = append(parts, p)
	}
	if e := strings.TrimSpace(epic); e != "" {
		parts = append(parts, c.EpicPrefix+e)
	}
	if k := strings.TrimSpace(tick); k != "" {
		parts = append(parts, c.TickPrefix+k)
	}
	return strings.Join(parts, c.Separator)
}

// prefixMessage puts the line above the body, separated by one newline. An
// unknown context leaves the message exactly as written.
func prefixMessage(line, body string) string {
	if line == "" {
		return body
	}
	return line + "\n" + body
}

func TestMessageContextComposesEveryCase(t *testing.T) {
	var c messageContext
	readContract(t, messageContextFile, &c)

	if c.Separator == "" || c.EpicPrefix == "" || c.TickPrefix == "" {
		t.Fatalf("the contract must pin a separator and both prefixes: %+v", c)
	}
	if len(c.Cases) == 0 {
		t.Fatal("the contract carries no cases")
	}

	empty := 0
	for _, tc := range c.Cases {
		got := composeLine(c, tc.Context.Project, tc.Context.Epic, tc.Context.Tick)
		if got != tc.Line {
			t.Errorf("%s: composed %q, contract says %q", tc.Name, got, tc.Line)
		}
		if tc.Line == "" {
			empty++
		}
	}
	if empty == 0 {
		t.Error("no case has an empty line — the rule that nothing known produces no frame is untested")
	}
}

func TestMessageContextPrefixesTheBody(t *testing.T) {
	var c messageContext
	readContract(t, messageContextFile, &c)

	if len(c.PrefixExamples) == 0 {
		t.Fatal("the contract carries no prefix examples")
	}
	for _, tc := range c.PrefixExamples {
		line := composeLine(c, tc.Context.Project, tc.Context.Epic, tc.Context.Tick)
		if got := prefixMessage(line, tc.Body); got != tc.Prefixed {
			t.Errorf("%s: prefixed %q, contract says %q", tc.Name, got, tc.Prefixed)
		}
	}
}

// The separator has to be visually distinct from anything an identifier may
// contain, or a project named with the separator inside it reads as two parts.
func TestMessageContextSeparatorIsNotInAnyIdentifier(t *testing.T) {
	var c messageContext
	readContract(t, messageContextFile, &c)

	for _, tc := range c.Cases {
		for _, id := range []string{tc.Context.Project, tc.Context.Epic, tc.Context.Tick} {
			if strings.TrimSpace(id) != "" && strings.Contains(id, strings.TrimSpace(c.Separator)) {
				t.Errorf("%s: identifier %q contains the separator %q", tc.Name, id, c.Separator)
			}
		}
	}
}
