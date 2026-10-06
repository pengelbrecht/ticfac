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
	return RecordsFromStoreRun(store, store.RunID())
}

// RecordsFromStoreRun reads ONE run's records from a store's fetched view —
// the store's own run via RecordsFromStore, or any sibling run's the same
// fetched branch carries (the epic's every-run records, ticfac tick gmo).
// The paths are the store's view of `.ticfac/runs/<run-id>/`, exactly the
// layout RecordsFromDir reads from disk.
func RecordsFromStoreRun(store *runstate.Store, runID string) (Records, error) {
	if store == nil {
		return Records{}, fmt.Errorf("no run-state store")
	}
	dir := runstate.RunDir(runID)
	source := recordSource{
		read: func(path string) ([]byte, bool, error) { return store.Read(path) },
		list: func(sub string) ([]string, error) {
			names := []string{}
			for _, path := range store.List(sub) {
				names = append(names, filepath.Base(path))
			}
			return names, nil
		},
	}
	return source.records(dir)
}

// RecordsFromDir reads one run's records from the run directory a checkout
// holds — `.ticfac/runs/<run-id>/`. The directory is what the store's fetch
// reads too (on origin); reading it directly is the fallback of a checkout
// with no remote to fetch, and the reader every fixture uses. A record that
// cannot be decoded is refused rather than skipped: a record this reader
// silently forgives is a record whose drift nobody can see.
func RecordsFromDir(dir string) (Records, error) {
	if dir == "" {
		return Records{}, fmt.Errorf("no run directory to read")
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		// A run directory that does not exist is a run that has written no
		// durable record — the honest empty answer, not a failure.
		return Records{}, nil
	}
	source := recordSource{
		read: func(path string) ([]byte, bool, error) {
			raw, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				return nil, false, nil
			}
			if err != nil {
				return nil, false, err
			}
			return raw, true, nil
		},
		list: func(sub string) ([]string, error) {
			entries, err := os.ReadDir(sub)
			if os.IsNotExist(err) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			names := []string{}
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
					names = append(names, entry.Name())
				}
			}
			return names, nil
		},
	}
	return source.records(dir)
}

// recordSource is one run's record store the way both readers see it: a way
// to read one path, and a way to list the record files of one record store
// directory (the `attempts`, `evidence`, … subdirectories). The two readers
// — the store's fetched view and the checkout's own directory — are the same
// walk over different accessors, so the record set they answer cannot drift
// apart.
type recordSource struct {
	read func(path string) ([]byte, bool, error)
	list func(dir string) ([]string, error)
}

// records walks one run's records: the checkpoint, then every numbered and
// keyed store. A record that cannot be decoded is refused rather than
// skipped, whichever accessor the walk reads through.
func (s recordSource) records(root string) (Records, error) {
	out := Records{}
	if raw, ok, err := s.read(filepath.Join(root, "checkpoint.json")); err != nil {
		return Records{}, err
	} else if ok {
		var checkpoint runstate.Checkpoint
		if err := decodeRecord(raw, &checkpoint); err != nil {
			return Records{}, err
		}
		out.Checkpoint = &checkpoint
	}
	var err error
	if out.Attempts, err = numberedRecords[runstate.Attempt](s, root, "attempts"); err != nil {
		return Records{}, err
	}
	if out.Decisions, err = numberedRecords[runstate.Decision](s, root, "decisions"); err != nil {
		return Records{}, err
	}
	if out.Evidence, err = keyedRecords[runstate.Evidence](s, root, "evidence"); err != nil {
		return Records{}, err
	}
	if out.Absorptions, err = keyedRecords[runstate.Absorption](s, root, "absorptions"); err != nil {
		return Records{}, err
	}
	if out.Findings, err = keyedRecords[runstate.Finding](s, root, "findings"); err != nil {
		return Records{}, err
	}
	sort.Slice(out.Attempts, func(i, j int) bool { return out.Attempts[i].Attempt < out.Attempts[j].Attempt })
	return out, nil
}

// numberedRecords reads every `<n>.json` record in one store, in number order.
func numberedRecords[T any](source recordSource, root, what string) ([]T, error) {
	names, err := source.list(filepath.Join(root, what))
	if err != nil {
		return nil, err
	}
	out := []T{}
	numbers := map[int]string{}
	var order []int
	for _, name := range names {
		n, err := strconv.Atoi(strings.TrimSuffix(name, ".json"))
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not the <n>.json name this directory's records use: %w", what, name, err)
		}
		if _, seen := numbers[n]; !seen {
			order = append(order, n)
		}
		numbers[n] = name
	}
	sort.Ints(order)
	for _, n := range order {
		var record T
		raw, ok, err := source.read(filepath.Join(root, what, numbers[n]))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if err := decodeRecord(raw, &record); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

// keyedRecords reads every `<key>.json` record in one store, in key order.
func keyedRecords[T any](source recordSource, root, what string) ([]T, error) {
	names, err := source.list(filepath.Join(root, what))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := []T{}
	for _, name := range names {
		var record T
		raw, ok, err := source.read(filepath.Join(root, what, name))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
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
