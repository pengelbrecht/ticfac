package runstate

import (
	"fmt"
	"regexp"
	"strings"
)

// GitHub's errors, counted by class (tick rlp), and the one refusal no retry
// can change: a credential refused by a repository it was never minted for
// (tick gy9). docs/analysis/github-failures.md is the measurement both come
// from.

// The classes a remote failure is counted under. They are the analysis's
// categories, so a run's counts read against the same table: the pipe, the
// remote taking a push and not moving the ref, the remote's own storage, a
// refused credential, and a credential refused by a repository it does not
// reach. An answer about the request (a ref that is not there, a lease the
// remote refused) is not a GitHub error and is not counted.
const (
	GitHubErrorNetwork          = "network"
	GitHubErrorRefUpdateFailed  = "ref_update_failed"
	GitHubErrorServerFault      = "server_fault"
	GitHubErrorAuthRefused      = "auth_refused"
	GitHubErrorCrossRepoRefused = "cross_repo_refused"
)

// GitHubErrorClasses is every class, in the order a report lists them.
var GitHubErrorClasses = []string{
	GitHubErrorNetwork, GitHubErrorRefUpdateFailed, GitHubErrorServerFault,
	GitHubErrorAuthRefused, GitHubErrorCrossRepoRefused,
}

var serverFaultMarkers = []string{
	"remote: fatal error in commit_refs",
	"remote: error: unable to write file",
	"remote: fatal: unable to rename temporary",
	"remote: internal server error",
	"the requested url returned error: 5",
}

// GitHubErrorClass is the class a failed remote git command is counted under,
// or "" when it is not a GitHub error at all. own is the repository the run's
// credential is for (owner/name), "" when unknown.
func GitHubErrorClass(err error, own string) string {
	if err == nil {
		return ""
	}
	text := strings.ToLower(err.Error())
	switch ClassifyRemote(err) {
	case RemoteAuthRefused:
		if _, cross := CrossRepoRefusal(err, own); cross {
			return GitHubErrorCrossRepoRefused
		}
		return GitHubErrorAuthRefused
	case RemoteTransient:
		switch {
		case remoteRefFailed.MatchString(text) && !containsAny(text, serverFaultMarkers):
			return GitHubErrorRefUpdateFailed
		case containsAny(text, serverFaultMarkers):
			return GitHubErrorServerFault
		}
		return GitHubErrorNetwork
	}
	return ""
}

// IsRefUpdateFailed answers whether a failure is the remote taking a push and
// not moving the ref — the bare "(failed)" that tracked the other run's push.
func IsRefUpdateFailed(err error) bool {
	return err != nil && remoteRefFailed.MatchString(strings.ToLower(err.Error())) &&
		!containsAny(strings.ToLower(err.Error()), serverFaultMarkers)
}

// RemoteCrossRepoRefusedClass names a cross-repository refusal as a run's stop
// reason and in the refusal's own text.
const RemoteCrossRepoRefusedClass = "remote_cross_repo_refused"

var (
	permissionTo   = regexp.MustCompile(`permission to ([a-z0-9._-]+/[a-z0-9._-]+?)(?:\.git)? denied`)
	unableToAccess = regexp.MustCompile(`unable to access '([^']+)'`)
)

// CrossRepoRefusal answers the repository that refused a credential, and
// whether it is a repository other than own — the one the credential was
// minted for. Cloud run run_5c7c (hn6, 2026-10-04) pushed a finding routed to
// the ticks repository with an installation token the factory mints for the
// run's ONE repository: "Permission to <owner>/ticks.git denied to
// ticfac[bot]" was certain, not a blip, and the four retries an auth refusal
// earns bought nothing. With own unknown nothing is cross-repository.
func CrossRepoRefusal(err error, own string) (string, bool) {
	own = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(own), ".git"))
	if err == nil || own == "" {
		return "", false
	}
	text := strings.ToLower(err.Error())
	if !containsAny(text, []string{"the requested url returned error: 403", "permission to "}) {
		return "", false
	}
	repo := ""
	if m := permissionTo.FindStringSubmatch(text); m != nil {
		repo = m[1]
	} else if m := unableToAccess.FindStringSubmatch(text); m != nil {
		repo = repoOfURL(m[1])
	}
	if repo == "" || repo == own {
		return repo, false
	}
	return repo, true
}

// repoOfURL is owner/name of a forge URL: its last two path segments.
func repoOfURL(url string) string {
	url = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(url), "/"), ".git")
	if _, rest, ok := strings.Cut(url, "://"); ok {
		url = rest
	} else if _, rest, ok := strings.Cut(url, ":"); ok {
		url = rest
	}
	parts := strings.Split(strings.Trim(url, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

// RemoteCrossRepoRefusedError is a credential refused by a repository other
// than the one it was minted for. It is never retried: waiting cannot grant a
// token a repository its mint did not name. Its first line is the class and
// the remedy, for the halt line that quotes it.
type RemoteCrossRepoRefusedError struct {
	What string
	// Repo is the repository that refused; Own the one the credential is for.
	Repo, Own string
	Err       error
}

func (e *RemoteCrossRepoRefusedError) Error() string {
	return fmt.Sprintf("%s: %s was refused by %s, a repository this run's credential was not minted for (it is "+
		"for %s), so it is not retried: on the factory's GitHub App rung a run's token reaches its own repository "+
		"only — a write to %s needs a credential for %s (file it there by hand, or run where the operator's own "+
		"credential reaches both): %v",
		RemoteCrossRepoRefusedClass, e.What, e.Repo, e.Own, e.Repo, e.Repo, e.Err)
}

func (e *RemoteCrossRepoRefusedError) Unwrap() error { return e.Err }

// CrossRepoRefused names the repository that refused, for a caller that
// switches on the refusal without importing this type.
func (e *RemoteCrossRepoRefusedError) CrossRepoRefused() string { return e.Repo }
