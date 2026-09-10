package reconcile

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	ticfac "github.com/pengelbrecht/ticfac"
	"github.com/pengelbrecht/ticfac/internal/exec/subprocess"
	"github.com/pengelbrecht/ticfac/internal/schema"
)

// The role-result envelope, validated against contracts/job-protocol.json
// before the reconciler acts on it.
//
// A role job's ONLY deliverable is its answer: a review that merges nothing and
// a closeout that writes nothing still have to say something a controller can
// read. So the answer is held to the contract's own record — the envelope, not
// the role-specific payload inside it, which is the role contract's business —
// and a malformed one FAILS CLOSED: the process tick stays open, because a tick
// closed behind an answer nobody could parse is a close nothing stands behind.
//
// The schemas are the ones compiled into this binary, for the reason the pin
// is: a controller run outside this checkout has no contracts directory, and a
// validation that silently does not happen is exactly the hole the envelope
// exists to close.

var (
	roleResultOnce   sync.Once
	roleResultSchema *schema.Schema
	roleResultDefs   map[string]*schema.Schema
	roleResultErr    error
)

// loadRoleResultSchema parses the record once. The parse itself is a check: the
// bundle's schemas are written in a strict ten-keyword subset, and a schema
// carrying an eleventh would be one this validator cannot enforce — so it fails
// here rather than validating less than it appears to.
func loadRoleResultSchema() (*schema.Schema, map[string]*schema.Schema, error) {
	roleResultOnce.Do(func() {
		var protocol struct {
			Records map[string]struct {
				Schema json.RawMessage `json:"schema"`
			} `json:"records"`
			Defs map[string]json.RawMessage `json:"$defs"`
		}
		if err := json.Unmarshal(ticfac.JobProtocolJSON, &protocol); err != nil {
			roleResultErr = fmt.Errorf("the compiled-in job protocol is unreadable: %w", err)
			return
		}
		record, ok := protocol.Records["role_result"]
		if !ok {
			roleResultErr = fmt.Errorf("the compiled-in job protocol declares no role_result record")
			return
		}
		parsed, err := schema.ParseSchema(record.Schema)
		if err != nil {
			roleResultErr = fmt.Errorf("the role_result schema: %w", err)
			return
		}
		defs, err := schema.ParseDefs(protocol.Defs)
		if err != nil {
			roleResultErr = fmt.Errorf("the job protocol's $defs: %w", err)
			return
		}
		roleResultSchema, roleResultDefs = parsed, defs
	})
	return roleResultSchema, roleResultDefs, roleResultErr
}

// ValidateRoleResult holds one envelope to contracts/job-protocol.json's
// `ticfac.role-result.v1`, and to the question that was asked: the schema_id
// must be the output_schema the JobSpec named, and the role must be the role
// that was dispatched. Validating only the shape would admit a review's answer
// read as a closeout's.
func ValidateRoleResult(result *subprocess.RoleResult, outputSchema, role string) error {
	if result == nil {
		return fmt.Errorf("no role-result envelope: the job's only deliverable is its answer, and it did not give one")
	}
	record, defs, err := loadRoleResultSchema()
	if err != nil {
		return err
	}

	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("the role result cannot be encoded: %w", err)
	}
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("the role result cannot be read back: %w", err)
	}
	if problems := schema.Validate(record, defs, document); len(problems) > 0 {
		return fmt.Errorf("the role-result envelope does not satisfy %s: %s",
			subprocess.SchemaIDRoleResult, strings.Join(problems, "; "))
	}

	if outputSchema != "" && result.SchemaID != outputSchema {
		return fmt.Errorf("the role result cites %s and the job asked for %s: a result is validated against what was "+
			"ASKED for, never against what came back", result.SchemaID, outputSchema)
	}
	if role != "" && result.Role != role {
		return fmt.Errorf("the role result answers as %s and %s was dispatched", result.Role, role)
	}
	return nil
}
