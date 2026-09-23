package factory

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// The work-type to model table (tick mrn, epic wne): what a classified work
// type COSTS on this deployment — which model serves it.
//
// THE SPLIT this table is one half of (operator, 2026-09-22, on epic wne):
// the REPOSITORY declares what the work IS — the closed work-type vocabulary
// in internal/runconfig — and the FACTORY declares what a work type costs,
// here, in its own deployment config. That is consistent with where the
// factory already keeps this kind of fact: wrangler.toml binds
// SWEEP_MAX_TIER beside RUN_MAX_COST_USD, FACTORY_MAX_INSTANCES and the
// gateway credentials, and it gives the right failure direction — a
// repository cannot name a model the factory does not serve, because the
// factory owns the gateway.
//
// THE SEPARATION is the whole point of asking a classifier what the work IS
// rather than what it is worth. A classification records a WORK TYPE, never a
// model, so this table can be re-tuned — swap the model lineup, change your
// mind about flash — without invalidating anything recorded against it. The
// closed enum is what holds the two apart: a model id is not a work type, so
// a pair written backwards (a model name where a work type belongs) is
// refused at the key.
//
// Two rules this table obeys, both from the epic:
//
//   - A work type with no mapping falls back to the deployment's default
//     model AND SAYS SO. Failing a dispatch on a table someone forgot to
//     extend is the wrong failure.
//   - A work type that is not one of the closed enum's names is a bug, not a
//     fallback — refused here, at the table's edge, with the value and the
//     legal vocabulary in the message.

// VarName is the wrangler.toml [vars] key the table rides in, beside
// RUN_WORKER_MODEL: this deployment's standing answer to "which model serves
// this kind of work". The value's shape is one `worktype=model` pair per
// entry, comma-separated — a flat table an operator edits without redeploying
// TypeScript, the same purchase RUN_WORKER_MODEL made in tick 1cd.
//
// Unset is a legal state and means NO TABLE: every work type then falls back
// to the default and says so, which is the degrade the epic asks for rather
// than a dispatch that dies on a variable nobody set.
const VarName = "RUN_WORKER_MODEL_BY_WORK_TYPE"

// WorkTypeModelTable is the parsed [VarName] variable: which model this
// deployment serves each work type with. An empty table (the variable unset,
// or set to nothing) is legitimate — see VarName.
type WorkTypeModelTable struct {
	models map[runconfig.WorkType]string
}

// ParseWorkTypeModels parses a [VarName] value.
//
// It refuses, naming the pair and the legal vocabulary:
//
//   - a key that is not one of the closed work-type enum — an invented work
//     type, or a model name written where a work type belongs;
//   - the same work type mapped twice — an operator who wrote both meant one
//     thing, and the table has to say which rather than let the last line win;
//   - a pair with no model — declining a work type is done by leaving it out
//     (which falls back and says so), never by an empty pair.
//
// A value that is empty or whitespace is NO TABLE, not an error, for the
// reason VarName states.
func ParseWorkTypeModels(value string) (*WorkTypeModelTable, error) {
	table := &WorkTypeModelTable{models: map[runconfig.WorkType]string{}}
	if strings.TrimSpace(value) == "" {
		return table, nil
	}
	for _, pair := range strings.Split(value, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		name, model, _ := strings.Cut(pair, "=")
		name, model = strings.TrimSpace(name), strings.TrimSpace(model)
		if !runconfig.IsKnownWorkType(name) {
			return nil, fmt.Errorf("%s: %q is not a work type this vocabulary knows — a work type that is not one of %s is a bug, not a fallback (and a model name where a work type belongs is this refusal)", VarName, name, runconfig.WorkTypeList(false))
		}
		if model == "" {
			return nil, fmt.Errorf("%s: work type %s maps to no model — a type is declined by leaving it out of the table, never by an empty pair", VarName, name)
		}
		workType := runconfig.WorkType(name)
		if _, already := table.models[workType]; already {
			return nil, fmt.Errorf("%s: work type %s is mapped twice — two models for one work type is a config bug the table must name, not a precedence the last line wins", VarName, name)
		}
		table.models[workType] = model
	}
	return table, nil
}

// Model resolves one work type to the model this deployment serves it with.
//
// fallback is the deployment's default model — the same default the
// run-submission > deployment-var > built-in-default chain already resolves —
// served when the table has no mapping for the work type.
//
// The second return is the REPORT, and it exists because the epic states the
// rule in one line: "a work type with no mapping falls back to the default
// AND SAYS SO". It is empty when the table mapped the work type itself; when
// the work type had no mapping it is the sentence that says what happened,
// naming the unmapped work type and the default that was served, because a
// fallback nobody can see is a silent downgrade.
//
// An unknown work type is REFUSED, never defaulted: the enum is closed, so a
// value outside it did not reach the table through any honest path, and
// serving the default would hide the defect behind a working dispatch. The
// same goes for an unmapped work type with no default to fall back to — that
// is not a degrade, it is a dispatch about to run with no model at all.
func (t *WorkTypeModelTable) Model(workType runconfig.WorkType, fallback string) (model string, report string, err error) {
	if !workType.Valid() {
		return "", "", fmt.Errorf("work type %q is not one of %s — a work type that is not one of these is a bug, not a fallback, and the table refuses it rather than serving a default that hides it", string(workType), runconfig.WorkTypeList(false))
	}
	if t == nil {
		return fallback, fallbackReport(workType, fallback), nil
	}
	if model, mapped := t.models[workType]; mapped {
		return model, "", nil
	}
	if fallback == "" {
		return "", "", fmt.Errorf("work type %s has no mapping in %s and no default model was given to fall back to — extend the table or configure the default", string(workType), VarName)
	}
	return fallback, fallbackReport(workType, fallback), nil
}

// fallbackReport is the SAYING SO: one sentence a dispatch record can carry
// verbatim, naming the work type the table forgot and the default that was
// served in its place.
func fallbackReport(workType runconfig.WorkType, fallback string) string {
	return fmt.Sprintf("work type %s has no mapping in %s; serving the default model %s instead", string(workType), VarName, fallback)
}

// ReadWorkTypeModels reads the table out of a wrangler.toml's [vars] — the
// committed one (cloudflare/wrangler.toml) on the test path, a staged copy on
// the deploy path. A file whose [vars] does not set [VarName] yields an empty
// table, not an error: that is the unset-variable degrade, and the caller
// decides what no table means (every work type falls back and says so).
func ReadWorkTypeModels(path string) (*WorkTypeModelTable, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the factory config %s: %w", path, err)
	}
	var doc struct {
		Vars map[string]string `toml:"vars"`
	}
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return nil, fmt.Errorf("read the factory config %s: %w", path, err)
	}
	return ParseWorkTypeModels(doc.Vars[VarName])
}
