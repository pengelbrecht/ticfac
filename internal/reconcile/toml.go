package reconcile

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// A minimal reader for ONE table of `.tick/runners.toml`: `[testing.commands]`.
//
// It is deliberately not a TOML parser. The reconciler is allowed to run
// exactly one thing — the integrated gate the target repository declares — and
// a general parser would be a general permission: every other table in that
// file configures somebody else's process, and reading them here would invite
// a future caller to run one. So this reads the one table, refuses everything
// it does not understand, and says which line it refused on.
//
// The two spellings the file actually uses are both accepted, because they are
// the same document:
//
//	[testing.commands]
//	go = { command = "go test ./...", description = "Go" }
//
//	[testing.commands.go]
//	command = "go test ./..."
//	description = "Go"

// GateCommand is one declared check: the name it is filed under, the command
// line to run, and what a person should call it.
type GateCommand struct {
	Name        string
	Command     string
	Description string
}

// GateCommands are the checks of `[testing.commands]`, in name order so that a
// gate's evidence key and its config digest do not depend on map iteration.
type GateCommands []GateCommand

// ReadGateCommands reads `[testing.commands]` from a runners.toml file.
//
// A missing file is not an error the reader invents a default for: a
// repository that declares no gate has declared no gate, and the caller
// decides whether that is fatal. It is reported as an empty set with no error
// only when the file exists and the table is absent; a file that is not there
// at all is an error, because the reconciler was pointed at it.
func ReadGateCommands(path string) (GateCommands, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the gate configuration: %w", err)
	}
	return parseGateCommands(string(raw))
}

func parseGateCommands(document string) (GateCommands, error) {
	const table = "testing.commands"
	byName := map[string]*GateCommand{}
	section := ""

	for number, line := range strings.Split(document, "\n") {
		text := strings.TrimSpace(stripComment(line))
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "[") {
			if !strings.HasSuffix(text, "]") {
				return nil, fmt.Errorf("runners.toml:%d: %q is not a table header this reader understands", number+1, text)
			}
			section = strings.TrimSpace(strings.Trim(text, "[]"))
			continue
		}
		switch {
		case section == table:
			name, value, ok := splitKeyValue(text)
			if !ok {
				return nil, fmt.Errorf("runners.toml:%d: %q is not a key/value in [%s]", number+1, text, table)
			}
			command, err := parseInlineCommand(number+1, value)
			if err != nil {
				return nil, err
			}
			command.Name = name
			byName[name] = command
		case strings.HasPrefix(section, table+"."):
			name := strings.TrimPrefix(section, table+".")
			key, value, ok := splitKeyValue(text)
			if !ok {
				return nil, fmt.Errorf("runners.toml:%d: %q is not a key/value in [%s]", number+1, text, section)
			}
			entry := byName[name]
			if entry == nil {
				entry = &GateCommand{Name: name}
				byName[name] = entry
			}
			if err := assign(number+1, entry, key, value); err != nil {
				return nil, err
			}
		}
	}

	out := make(GateCommands, 0, len(byName))
	for _, entry := range byName {
		if entry.Command == "" {
			return nil, fmt.Errorf("[%s.%s] declares no command: a named gate that runs nothing is a gate that always passes",
				table, entry.Name)
		}
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// parseInlineCommand reads `{ command = "...", description = "..." }`.
func parseInlineCommand(line int, value string) (*GateCommand, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "{") || !strings.HasSuffix(value, "}") {
		return nil, fmt.Errorf("runners.toml:%d: %q is not an inline table; [testing.commands] entries are tables, "+
			"and a bare string would be a command line nobody named", line, value)
	}
	entry := &GateCommand{}
	for _, field := range splitInlineFields(strings.TrimSuffix(strings.TrimPrefix(value, "{"), "}")) {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		key, raw, ok := splitKeyValue(field)
		if !ok {
			return nil, fmt.Errorf("runners.toml:%d: %q is not a key/value inside the inline table", line, field)
		}
		if err := assign(line, entry, key, raw); err != nil {
			return nil, err
		}
	}
	return entry, nil
}

func assign(line int, entry *GateCommand, key, raw string) error {
	text, err := parseString(raw)
	if err != nil {
		return fmt.Errorf("runners.toml:%d: %s: %w", line, key, err)
	}
	switch key {
	case "command":
		entry.Command = text
	case "description":
		entry.Description = text
	default:
		// Ignored rather than refused: this reader owns two keys of a table
		// whose schema belongs to ticks, and refusing a key ticks adds later
		// would make an unrelated upgrade break the gate.
	}
	return nil
}

// splitInlineFields splits on commas that are not inside a string.
func splitInlineFields(value string) []string {
	var out []string
	depth, quoted, escaped, start := 0, false, false, 0
	for i, r := range value {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && quoted:
			escaped = true
		case r == '"':
			quoted = !quoted
		case quoted:
		case r == '{':
			depth++
		case r == '}':
			depth--
		case r == ',' && depth == 0:
			out = append(out, value[start:i])
			start = i + 1
		}
	}
	return append(out, value[start:])
}

// splitKeyValue splits on the first `=` outside a string.
func splitKeyValue(text string) (key, value string, ok bool) {
	quoted, escaped := false, false
	for i, r := range text {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && quoted:
			escaped = true
		case r == '"':
			quoted = !quoted
		case quoted:
		case r == '=':
			return strings.TrimSpace(strings.Trim(text[:i], `"`)), strings.TrimSpace(text[i+1:]), true
		}
	}
	return "", "", false
}

// parseString reads a basic TOML string. A value that is not one is refused:
// the only values this table carries are strings, and a number where a command
// belongs is a configuration error worth naming.
func parseString(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || !strings.HasPrefix(raw, `"`) || !strings.HasSuffix(raw, `"`) {
		return "", fmt.Errorf("%s is not a quoted string", raw)
	}
	text, err := strconv.Unquote(raw)
	if err != nil {
		return "", fmt.Errorf("%s is not a string this reader can unquote: %w", raw, err)
	}
	return text, nil
}

// stripComment removes a `#` comment that is not inside a string.
func stripComment(line string) string {
	quoted, escaped := false, false
	for i, r := range line {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && quoted:
			escaped = true
		case r == '"':
			quoted = !quoted
		case quoted:
		case r == '#':
			return line[:i]
		}
	}
	return line
}
