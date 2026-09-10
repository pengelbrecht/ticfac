package profile

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// A narrow reader for ONE family of tables in `.tick/runners.toml`:
// `[roles.<name>]` and `[roles.<name>.tiers.<tier>]`.
//
// It is deliberately a SECOND reader rather than an extension of the
// reconciler's gate reader (internal/reconcile/toml.go). That one parses
// `[testing.commands]` because the integrated gate is the only thing the
// reconciler is allowed to RUN, and it refuses every other table on purpose: a
// reader that also understood roles would be one edit away from running
// something a role declared. Routing is a different question — which runner and
// model a role is dispatched with — so it gets a reader of its own that can run
// nothing at all.
//
// It is not a TOML parser either. It reads two keys of one table family,
// ignores every other table, and refuses a line inside a roles table that it
// cannot read, naming the line.

// Role is one `[roles.<name>]` declaration, and the tier overlays under it.
// `kind` is ticks' word for the runner; the two keys this reader owns are the
// two a profile has anywhere to put.
type Role struct {
	Name  string
	Kind  string
	Model string
	Tiers map[string]Role
}

// ReadRoles reads `[roles.*]` from a runners.toml file. A missing file is NOT
// an error: a repository that declares no routing has declared none, and the
// caller decides what that means — here it means the profiles ship as written.
func ReadRoles(path string) (map[string]Role, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Role{}, nil
		}
		return nil, fmt.Errorf("read the runner routing: %w", err)
	}
	return ParseRoles(string(raw))
}

// ParseRoles reads `[roles.*]` out of a runners.toml document.
func ParseRoles(document string) (map[string]Role, error) {
	roles := map[string]Role{}
	section := ""

	for number, line := range strings.Split(document, "\n") {
		text := strings.TrimSpace(stripComment(line))
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "[") {
			if !strings.HasSuffix(text, "]") {
				if !strings.HasPrefix(text, "[roles") {
					// Not this reader's table, and not this reader's business
					// to have an opinion about: the gate reader owns the
					// document's well-formedness where it reads it.
					section = ""
					continue
				}
				return nil, fmt.Errorf("runners.toml:%d: %q is not a table header this reader understands", number+1, text)
			}
			section = strings.TrimSpace(strings.Trim(text, "[]"))
			continue
		}
		name, tier, ok := rolePath(section)
		if !ok {
			continue
		}
		key, value, ok := splitKeyValue(text)
		if !ok {
			return nil, fmt.Errorf("runners.toml:%d: %q is not a key/value in [%s]", number+1, text, section)
		}
		if key != "kind" && key != "model" {
			// A key this reader does not own: the schema of that table belongs
			// to ticks, and refusing a key ticks adds later would break every
			// run's routing on an unrelated upgrade.
			continue
		}
		text, err := parseString(value)
		if err != nil {
			return nil, fmt.Errorf("runners.toml:%d: %s: %w", number+1, key, err)
		}
		role := roles[name]
		role.Name = name
		if tier == "" {
			role.assign(key, text)
		} else {
			if role.Tiers == nil {
				role.Tiers = map[string]Role{}
			}
			overlay := role.Tiers[tier]
			overlay.Name = tier
			overlay.assign(key, text)
			role.Tiers[tier] = overlay
		}
		roles[name] = role
	}
	return roles, nil
}

func (r *Role) assign(key, value string) {
	switch key {
	case "kind":
		r.Kind = value
	case "model":
		r.Model = value
	}
}

// rolePath splits a table header into the role it configures and the tier it
// overlays, or reports that the header is not this reader's.
func rolePath(section string) (role, tier string, ok bool) {
	const prefix = "roles."
	if !strings.HasPrefix(section, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(section, prefix)
	if rest == "" {
		return "", "", false
	}
	parts := strings.Split(rest, ".")
	switch {
	case len(parts) == 1:
		return parts[0], "", true
	case len(parts) == 3 && parts[1] == "tiers":
		return parts[0], parts[2], true
	default:
		// A roles sub-table this reader does not model — read nothing rather
		// than guess what it configures.
		return "", "", false
	}
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

// parseString reads a basic TOML string. The two keys this reader owns are both
// strings, so a value that is not one is a configuration error worth naming
// rather than a value to coerce.
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
