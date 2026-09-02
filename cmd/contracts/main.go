// Command contracts is the vendoring gate for the pinned ticks contract
// bundle. Two subcommands, and the split between them is the whole safety
// argument (ticks cloud/factory/CONTRACTS.md, applied here):
//
//	check            every test run, every CI run. NEVER touches the network.
//	verify-upstream  CI only. Fetches the pinned ref and compares.
//	sync             a person adopting a new pin. Requires the network.
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
		fmt.Println("contracts: the vendored bundle matches contracts.pin.json (offline check)")

	case "verify-upstream":
		upstream, pin := fetch(root)
		problems, err := contracts.Diff(root, upstream)
		if err != nil {
			fail(err)
		}
		if len(problems) > 0 {
			fmt.Fprintf(os.Stderr, "the vendored contracts/ is not what %s published at %s:\n",
				pin.Repository, pin.Ref)
			for _, p := range problems {
				fmt.Fprintf(os.Stderr, "  %s\n", p)
			}
			fmt.Fprintln(os.Stderr, "\nRun `go run ./cmd/contracts sync` and commit contracts/ with contracts.pin.json.")
			os.Exit(1)
		}
		fmt.Printf("contracts: the vendored bundle is byte-for-byte %s@%s:%s\n",
			pin.Repository, pin.Ref[:12], pin.Directory)

	case "sync":
		upstream, pin := fetch(root)
		if err := contracts.Write(root, upstream); err != nil {
			fail(err)
		}
		if err := contracts.VerifyPin(root); err != nil {
			fail(err)
		}
		fmt.Printf("contracts: vendored %d files from %s@%s\n", len(upstream), pin.Repository, pin.Ref[:12])

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

	client := &http.Client{Timeout: 60 * time.Second}
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
