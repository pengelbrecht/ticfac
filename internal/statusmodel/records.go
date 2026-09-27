package statusmodel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pengelbrecht/ticfac/internal/runstate"
)

// Records is every durable record kind the model reads, gathered into one
// struct so the two readers below — the store that fetches origin and the
// run directory a checkout holds — answer the same shape and the model
// never learns which it was handed.
type Records struct {
	Checkpoint  *runstate.Checkpoint
	Attempts    []runstate.Attempt
	Decisions   []runstate.Decision
	Evidence    []runstate.Evidence
	Absorptions []runstate.Absorption
	Findings    []runstate.Finding
}

// RecordReader reads one run's records. The store is the durable authority
// (records mean pushed on origin); the directory is what a checkout holds,
// which is what a run that never pushed leaves behind and what a test
// writes by hand. Both are honest about what they are: the reader names
// itself in the degraded note when it cannot answer.
type RecordReader interface {
	Records() (Records, error)
}

// RecordsFromStore reads one run's records through the run-state store, the
// same store the reconciler and `ticfac findings` read through: the caller
// has already fetched, and the records answer from origin's view — the
// durable authority, never a working tree's unpushed copy.
func RecordsFromStore(store *runstate.Store) (Records, error) {
	if store == nil {
		return Records{}, fmt.Errorf("no run-state store")
	}
	out := Records{}
	checkpoint, ok, err := store.Checkpoint()
	if err != nil {
		return Records{}, err
	}
	if ok {
		out.Checkpoint = checkpoint
	}
	if out.Attempts, err = store.Attempts(); err != nil {
		return Records{}, err
	}
	if out.Decisions, err = store.Decisions(); err != nil {
		return Records{}, err
	}
	for _, key := range store.EvidenceKeys() {
		evidence, ok, err := store.Evidence(key)
		if err != nil {
			return Records{}, err
		}
		if ok {
			out.Evidence = append(out.Evidence, *evidence)
		}
	}
	if out.Absorptions, err = store.Absorptions(); err != nil {
		return Records{}, err
	}
	if out.Findings, err = store.Findings(); err != nil {
		return Records{}, err
	}
	sort.Slice(out.Attempts, func(i, j int) bool { return out.Attempts[i].Attempt < out.Attempts[j].Attempt })
	return out, nil
}

// RecordsFromDir reads one run's records from the run directory a checkout
// holds — `.ticfac/runs/<run-id>/`. The directory is what the store's fetch
// reads too (on origin); reading it directly is the fallback of a checkout
// with no remote to fetch, and the reader every fixture uses. A record that
// cannot be decoded is refused rather than skipped: a record this reader
// silently forgives is a record whose drift nobody can see.
func RecordsFromDir(dir string) (Records, error) {
	out := Records{}
	if dir == "" {
		return out, fmt.Errorf("no run directory to read")
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		// A run directory that does not exist is a run that has written no
		// durable record — the honest empty answer, not a failure.
		return Records{}, nil
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "checkpoint.json")); err == nil {
		var checkpoint runstate.Checkpoint
		if err := decodeRecord(raw, &checkpoint); err != nil {
			return Records{}, err
		}
		out.Checkpoint = &checkpoint
	}
	var err error
	if out.Attempts, err = numberedRecords[runstate.Attempt](filepath.Join(dir, "attempts")); err != nil {
		return Records{}, err
	}
	if out.Decisions, err = numberedRecords[runstate.Decision](filepath.Join(dir, "decisions")); err != nil {
		return Records{}, err
	}
	if out.Evidence, err = keyedRecords[runstate.Evidence](filepath.Join(dir, "evidence")); err != nil {
		return Records{}, err
	}
	if out.Absorptions, err = keyedRecords[runstate.Absorption](filepath.Join(dir, "absorptions")); err != nil {
		return Records{}, err
	}
	if out.Findings, err = keyedRecords[runstate.Finding](filepath.Join(dir, "findings")); err != nil {
		return Records{}, err
	}
	sort.Slice(out.Attempts, func(i, j int) bool { return out.Attempts[i].Attempt < out.Attempts[j].Attempt })
	return out, nil
}

// numberedRecords reads every `<n>.json` record in dir, in number order.
func numberedRecords[T any](dir string) ([]T, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []T{}
	numbers := map[int]string{}
	var order []int
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(name, ".json"))
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not the <n>.json name this directory's records use: %w", dir, name, err)
		}
		if _, seen := numbers[n]; !seen {
			order = append(order, n)
		}
		numbers[n] = name
	}
	sort.Ints(order)
	for _, n := range order {
		var record T
		raw, err := os.ReadFile(filepath.Join(dir, numbers[n]))
		if err != nil {
			return nil, err
		}
		if err := decodeRecord(raw, &record); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

// keyedRecords reads every `<key>.json` record in dir, in key order.
func keyedRecords[T any](dir string) ([]T, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := []T{}
	for _, name := range names {
		var record T
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if err := decodeRecord(raw, &record); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

// decodeRecord reads one record strictly — an unknown field is a record this
// reader does not know, and refusing it is what keeps a drifted record
// visible instead of half-read.
func decodeRecord(raw []byte, into any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("decode a run record: %w", err)
	}
	if dec.More() {
		return fmt.Errorf("decode a run record: trailing content after the object")
	}
	return nil
}
