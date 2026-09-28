// Command contracts is the contracts gate for the repository root `contracts/`
// directory — ticfac's own authored bundle, carrying two contracts vendored
// from a pinned ticks release. Two subcommands, and the split between them is
// the whole safety argument (CONTRACTS.md):
//
//	check            every test run, every CI run. NEVER touches the network.
//	verify-upstream  CI only. Fetches the pinned ticks ref and compares.
//	sync             a person adopting a new ticks pin. Requires the network.
//
// `check` is the gate and makes no network call, so no network failure can
// turn a test run green by skipping it. `sync` is the only thing that needs
// the network and is not on the test path, so a GitHub outage can only make a
// deliberate pin bump fail — never make a test run lie.
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/pengelbrecht/ticfac/internal/contracts"
	"github.com/pengelbrecht/ticfac/internal/httpnet"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: contracts <check|verify-upstream|sync>")
		os.Exit(2)
	}

	root, err := contracts.RepoRoot()
	if err != nil {
		fail(err)
	}

	switch os.Args[1] {
	case "check":
		if err := contracts.VerifyPin(root); err != nil {
			fail(err)
		}
		fmt.Println("contracts: ticfac's bundle verifies, and the ticks-owned files match contracts.pin.json (offline check)")

	case "verify-upstream":
		upstream, pin := fetch(root)
		problems, err := contracts.Diff(root, upstream)
		if err != nil {
			fail(err)
		}
		if len(problems) > 0 {
			fmt.Fprintf(os.Stderr, "the vendored ticks contracts are not what %s published at %s:\n",
				pin.Repository, pin.Ref)
			for _, p := range problems {
				fmt.Fprintf(os.Stderr, "  %s\n", p)
			}
			fmt.Fprintln(os.Stderr, "\nRun `go run ./cmd/contracts sync`, re-cut ticfac's bundle in the same commit, and commit contracts/ with contracts.pin.json.")
			os.Exit(1)
		}
		fmt.Printf("contracts: the vendored ticks contracts are byte-for-byte %s@%s:%s (ticks bundle %s)\n",
			pin.Repository, pin.Ref[:12], pin.Directory, pin.BundleVersion)

	case "sync":
		upstream, pin := fetch(root)
		if err := contracts.Write(root, upstream); err != nil {
			fail(err)
		}
		if err := contracts.VerifyPin(root); err != nil {
			fail(fmt.Errorf("the vendored ticks contracts were written; the offline gate still refuses:\n%w\n\n"+
				"Adopting the new ticks bytes is one deliberate act: re-cut ticfac's bundle\n"+
				"(bump `version` in contracts/bundle.json, refresh the digests, add the\n"+
				"contracts/CHANGELOG.md entry) in the same commit, and commit contracts/\n"+
				"with contracts.pin.json together.", err))
		}
		fmt.Printf("contracts: vendored %d ticks contracts from %s@%s (ticks bundle %s)\n",
			len(pin.Files), pin.Repository, pin.Ref[:12], pin.BundleVersion)

	default:
		fmt.Fprintln(os.Stderr, "usage: contracts <check|verify-upstream|sync>")
		os.Exit(2)
	}
}

// fetch downloads the pinned ref. Every failure exits non-zero and writes
// nothing: there is no path here that warns and continues.
func fetch(root string) (map[string][]byte, *contracts.Pin) {
	pin, err := contracts.LoadPin(root)
	if err != nil {
		fail(err)
	}

	client := httpnet.Client(60 * time.Second)
	url := pin.TarballURL()
	resp, err := client.Get(url)
	if err != nil {
		fail(fmt.Errorf("fetching %s: %w\nThis is the one command that needs the network; nothing was written", url, err))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		fail(fmt.Errorf("%s: %s\nThe pinned ref must be a commit that exists in a PUBLIC repository", url, resp.Status))
	}
	if resp.StatusCode != http.StatusOK {
		fail(fmt.Errorf("%s: %s", url, resp.Status))
	}

	upstream, err := contracts.ExtractBundle(resp.Body, pin.Directory)
	if err != nil {
		fail(fmt.Errorf("%s: %w", url, err))
	}
	return upstream, pin
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "contracts: %v\n", err)
	os.Exit(1)
}
