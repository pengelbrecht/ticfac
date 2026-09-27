package subprocess

import (
	"encoding/json"
	"fmt"
)

// The SIGTERM flush's snapshot (tick ppt, repaired after epic-2jn).
//
// The flush used to COMMIT an in-flight attempt's uncommitted work in the
// attempt's own worktree and push it to the attempt's own branch. That is
// right only when the worker dies with the process that took the snapshot — a
// whole-container eviction — and wrong in the ordinary local case, where the
// worker is a separate process that survives the orchestrator's SIGTERM (and
// in the cloud whenever only the orchestrator is evicted). On epic-2jn the
// worker did survive: it committed its real result beside the snapshot rather
// than on it, and the resumed run's collected head could not be put on
// origin, which held the snapshot instead. The run was held for a person over
// two identical trees.
//
// So the flush's snapshot is taken the way the wall-clock stop's is — into a
// PRIVATE index, never touching the worktree's index, HEAD or branch — onto
// the attempt's wip ref, and that ref, never the attempt branch, is what the
// flush pushes for the uncommitted part. A worker that lives on never sees it
// and its branch never diverges; a worker that died with its container leaves
// the ref as material a later incarnation points the attempt's prompt at,
// exactly as a successor is pointed at a stopped predecessor's preserved work.

// FileEvacuationSnapshot is where the flush records the snapshot it took,
// beside the attempt record. It is deliberately NOT FileWIPSnapshot: that
// record is the teardown's "already preserved", and a flush taken while the
// worker was still writing must not stop a later teardown from preserving
// what the worker wrote after it.
const FileEvacuationSnapshot = "evacuation-snapshot.json"

// EvacuationSnapshotMessage is the subject of the flush's snapshot commit.
const EvacuationSnapshotMessage = "ticfac: evacuation snapshot of uncommitted work — on a wip ref, not evidence, never merged"

// LegacyEvacuationSnapshotSubject is the subject older builds' flush committed
// ON the attempt branch. The reconciler recognises it on origin, because a
// worker's final commit supersedes its own snapshot (integrate.go).
const LegacyEvacuationSnapshotSubject = "ticfac: evacuation snapshot of uncommitted work — the container was stopped mid-attempt"

// EvacuationSnapshot preserves the worktree's uncommitted work on ref without
// touching the worktree's index, HEAD or branch: the worker may still be alive
// and is never disturbed. It answers ok=false, with no ref written, for a
// worktree that holds nothing a snapshot exists to carry.
func EvacuationSnapshot(worktree, ref, artifactPrefix string) (commit string, ok bool, err error) {
	dirty, err := UncommittedWork(worktree, artifactPrefix)
	if err != nil {
		return "", false, fmt.Errorf("read the worktree's uncommitted work: %w", err)
	}
	if !dirty {
		return "", false, nil
	}
	commit, err = snapshotWorktree(worktree, ref, artifactPrefix, EvacuationSnapshotMessage)
	if err != nil {
		return "", false, err
	}
	return commit, true, nil
}

// RecordEvacuationSnapshot writes the flush's snapshot record beside the
// attempt record in stateDir. A later record replaces an earlier one: each
// flush's snapshot is the newer state of the same worktree.
func RecordEvacuationSnapshot(stateDir string, snap WIPSnapshot) error {
	snap.SchemaVersion = WIPSnapshotSchemaVersion
	if err := newStore(stateDir).writeJSON(FileEvacuationSnapshot, snap); err != nil {
		return fmt.Errorf("write the evacuation-snapshot record: %w", err)
	}
	return nil
}

// artifactPrefixOf reads the job's artifact prefix out of a raw attempt
// record: the minimal decode, for the same reason ReadAttemptWork is one.
func artifactPrefixOf(raw []byte) string {
	var record struct {
		Spec *struct {
			ArtifactPrefix string `json:"artifact_prefix"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &record) != nil || record.Spec == nil {
		return ""
	}
	return record.Spec.ArtifactPrefix
}
