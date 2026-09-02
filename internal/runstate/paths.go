package runstate

import (
	"fmt"
	"strings"
)

// Root is the run-state directory. It mirrors `.tick/` in location and in
// boundary rules, and differs from it in persistence policy.
const Root = ".ticfac"

// TagPrefix is the run tag's pattern, `ticfac/run-<run-id>`. The tag is placed
// at terminal state and before the run branch is deleted, on published and
// unpublished runs alike.
const TagPrefix = "ticfac/run-"

// RunDir is `.ticfac/runs/<run-id>`, the directory holding one run's records.
func RunDir(runID string) string { return Root + "/runs/" + runID }

// CheckpointPath is `.ticfac/runs/<run-id>/checkpoint.json` — exactly one per
// run, updated under a sha guard.
func CheckpointPath(runID string) string { return RunDir(runID) + "/checkpoint.json" }

// AttemptPath is `.ticfac/runs/<run-id>/attempts/<n>.json`, where <n> is the
// 1-based attempt number for the tick.
func AttemptPath(runID string, n int) string {
	return fmt.Sprintf("%s/attempts/%d.json", RunDir(runID), n)
}

// DecisionPath is `.ticfac/runs/<run-id>/decisions/<n>.json`, where <n> is the
// 1-based decision number within the run.
func DecisionPath(runID string, n int) string {
	return fmt.Sprintf("%s/decisions/%d.json", RunDir(runID), n)
}

// EvidencePath is `.ticfac/runs/<run-id>/evidence/<key>.json`, where <key> is
// the record's own `key` field — so a filename and a citation in a JobResult
// name the same thing.
func EvidencePath(runID, key string) string { return RunDir(runID) + "/evidence/" + key + ".json" }

// TagName is the run tag, `ticfac/run-<run-id>`.
func TagName(runID string) string { return TagPrefix + runID }

// checkSegment refuses an identifier that would escape the run directory or
// name a file the layout does not describe. A run id or an evidence key
// reaches this store from a tracker, a config file or a check definition, and
// one containing a slash would silently write outside `.ticfac/runs/<run-id>/`.
func checkSegment(kind, value string) error {
	switch {
	case value == "":
		return fmt.Errorf("%s is empty", kind)
	case strings.ContainsAny(value, "/\\"):
		return fmt.Errorf("%s %q contains a path separator", kind, value)
	case value == "." || value == "..":
		return fmt.Errorf("%s %q is a path traversal", kind, value)
	case strings.HasPrefix(value, "."):
		return fmt.Errorf("%s %q starts with a dot", kind, value)
	}
	return nil
}

// checkIndex refuses a non-positive record number: <n> is 1-based in both the
// attempts and the decisions directory.
func checkIndex(kind string, n int) error {
	if n < 1 {
		return fmt.Errorf("%s %d is not 1-based", kind, n)
	}
	return nil
}
