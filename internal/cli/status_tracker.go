package cli

// The epic's tracker, as the integration branch carries it (hn6, tick gmo).
//
// A dashboard that answers for the epic must read the tracker where the
// epic's work actually is: the integration branch on origin — where the
// run's own durable tracker writes land (internal/reconcile/tracker.go:
// durable means pushed) — and not the checkout's working tree, which an
// operator watching a cloud run has on main. From main, every tick the
// integration branch closed still reads open, and the closed half of the
// epic's table is invisible.
//
// The read is read-only and cheap: the branch's `.tick/` subtree is
// materialized through `git archive` into a temporary directory carrying a
// minimal git repository, and tk reads there. No worktree of the operator's
// checkout is registered, and the only thing written under its .git is the
// private, per-process peek ref the fetch targets (never FETCH_HEAD, never
// a ref another process fetches into). The result is the same `.tick/`
// bytes tk would read if the branch were checked out, and nothing else.
//
// Every failure is a fallback, never an error: the tracker on the checkout
// is what `epicGraph` has always read, and an origin this checkout cannot
// fetch from — or a branch it does not have yet — costs nothing but the
// fallback. The degraded list names an unread tracker only when BOTH are
// unreadable, which is the honest claim.

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/pengelbrecht/ticfac/internal/gitbin"
	"github.com/pengelbrecht/ticfac/internal/tempdir"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// statusTrackerFetchSeq distinguishes stores within one process, the same
// counter rule the run-state store holds (two reads in one process must not
// share a ref to fetch into).
var statusTrackerFetchSeq atomic.Uint64

// statusTrackerPeekRef is the private, per-process ref one read fetches the
// integration branch into — never FETCH_HEAD, and never a ref another
// process fetches into (the store's own rule, learned the hard way).
func statusTrackerPeekRef(epicID string) string {
	id := fmt.Sprintf("%d-%d", os.Getpid(), statusTrackerFetchSeq.Add(1))
	return "refs/ticfac/peek/status/" + id + "/" + epicID
}

// trackerGitOut runs one git command in repo and answers its stdout. The
// environment carries the transport bound every git that can reach a remote
// must have (gitbin.TransportEnv — tick pul): a fetch to a silent remote
// otherwise holds the status read forever.
func trackerGitOut(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Env = append(os.Environ(), gitbin.TransportEnv()...)
	// It fetches and reads, never pushes; the queue costs nothing for
	// anything else (tick rlp).
	done := gitbin.PushQueue(repo, args, nil)
	err := cmd.Run()
	done(err)
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err,
			strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// graphAtIntegrationBranch reads the epic's graph from the integration
// branch as origin has it, closed tasks included, with the record fields the
// graph endpoint omits enriched from `tk list --all --json` of the same
// branch. Nil — with the checkout fallback left to the caller — when any
// step of the read fails: no remote, no branch on it, an archive that will
// not extract, a tk that will not answer there.
func graphAtIntegrationBranch(ctx context.Context, repo, epicID string) *tk.Graph {
	if epicID == "" {
		return nil
	}
	branch := "epic/" + epicID
	url, err := trackerGitOut(ctx, repo, "config", "--get", "remote.origin.url")
	if err != nil {
		return nil // no origin: nothing to read the branch from
	}
	ref := statusTrackerPeekRef(epicID)
	if _, err := trackerGitOut(ctx, repo, "fetch", "--no-write-fetch-head", "--refmap=",
		"origin", "+refs/heads/"+branch+":"+ref); err != nil {
		return nil // the branch is not on origin (yet): the checkout is the truth there is
	}
	dir, remove, err := tempdir.Make("ticfac-tracker-read-")
	if err != nil {
		return nil
	}
	defer remove()

	if err := extractTrackerSubtree(ctx, repo, ref, dir); err != nil {
		return nil
	}
	// tk answers a git repository with a remote: the archive carries the
	// tracker's records, and the remote names the project the same way the
	// checkout's own does.
	if _, err := trackerGitOut(ctx, dir, "init", "--quiet"); err != nil {
		return nil
	}
	if _, err := trackerGitOut(ctx, dir, "remote", "add", "origin", strings.TrimSpace(url)); err != nil {
		return nil
	}
	client, err := tk.NewContext(ctx, tk.Options{Dir: dir})
	if err != nil {
		return nil
	}
	graph, err := client.GraphAll(ctx, epicID)
	if err != nil {
		return nil
	}
	enrichTasksFromList(ctx, client, &graph)
	return &graph
}

// extractTrackerSubtree writes the branch's `.tick/` subtree into dir:
// `git archive` over the fetched ref, streamed through Go's own tar reader
// into the temp directory. Only `.tick/` is extracted, because only `.tick/`
// is tk's to read; the rest of the tree would be a whole checkout's weight
// for nothing.
func extractTrackerSubtree(ctx context.Context, repo, ref, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "archive", "--format=tar", ref, "--", ".tick")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	// The archive reads objects the fetch already brought local, but the
	// guard applies to every git that CAN reach a remote (tick pul): a bound
	// on a local read costs nothing.
	cmd.Env = append(os.Environ(), gitbin.TransportEnv()...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git archive %s .tick: %w", ref, err)
	}
	reader := tar.NewReader(&stdout)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read the tracker's archive: %w", err)
		}
		name := filepath.Clean(header.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("the tracker's archive carries a path outside its tree: %q", header.Name)
		}
		target := filepath.Join(dir, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(file, reader); err != nil {
				file.Close()
				return fmt.Errorf("extract %s: %w", header.Name, err)
			}
			file.Close()
		default:
			// Symlinks and specials are not tracker records; skip them.
		}
	}
}

// enrichTasksFromList copies the record fields the graph endpoint omits —
// the notes and close metadata the duplicate and duration derivations read —
// from `tk list --all --json` onto the graph's tasks, by id. The two reads
// answer the same tracker at the same head; a list that fails costs the
// enrichment, never the graph.
func enrichTasksFromList(ctx context.Context, client *tk.Client, graph *tk.Graph) {
	list, err := client.List(ctx)
	if err != nil {
		return
	}
	byID := map[string]tk.Tick{}
	for _, record := range list.Ticks {
		byID[record.ID] = record
	}
	for wi := range graph.Waves {
		for ti := range graph.Waves[wi].Tasks {
			task := &graph.Waves[wi].Tasks[ti]
			record, ok := byID[task.ID]
			if !ok {
				continue
			}
			if record.Notes != "" {
				task.Notes = record.Notes
			}
			if record.ClosedAt != "" {
				task.ClosedAt = record.ClosedAt
			}
			if record.ClosedReason != "" {
				task.ClosedReason = record.ClosedReason
			}
		}
	}
}
