package runstate

import (
	"fmt"
	"strings"
	"testing"

	"hegel.dev/go/hegel"
)

// Properties of ClassifyRemote (tick 89g), the classifier that decides whether
// a failed remote git operation is waited through or stopped for.
//
// The classifier's whole design is its ORDER (see ClassifyRemote): the
// answers about the REPOSITORY are read first, then the answers about WHO IS
// ASKING, then the bare denials, then the answers about the PIPE. Both a
// connection reset and a rejected public key end with the same line —
// "fatal: Could not read from remote repository." — so precedence is the
// only thing standing between a refusal and an unbounded retry of it.
//
// The two properties, one per direction of the mistake:
//
//   - NO SILENT WAIT: a text the classifier reads as RemoteTransient really
//     carries a pipe marker — a generated text built from a repository answer,
//     a who answer, a bare denial and a pipe answer, in any order, any case,
//     never comes out transient while one of the first three is present.
//     This is the dangerous direction: a refusal retried until a bound is
//     spent is a slower refusal with the run's clock spent on it (tick enj).
//   - NO SILENT STOP: a text that really carries a pipe marker and no
//     repository, who or denial answer is never left unclassified, because
//     the run that stops over a reset is the run a person had to watch
//     (epic-ncv, epic-hn6, fork exhaustion on 2026-10-06 — each of those was
//     a stop this classifier had no word for).
//
// The generator draws its markers from the classifier's OWN lists, so a
// marker added to one is exercised the moment it lands; the noise around them
// is arbitrary text that carries no marker at all, so whatever the classifier
// says about a case is said about the markers and never about the noise.

// remoteMarkerLists is the classifier's own precedence order, highest first:
// the list's index is the rank its answer wins at.
var remoteMarkerLists = [][]string{terminalMarkers, authMarkers, deniedMarkers, transientMarkers}

// remoteClassOfRank is the class each list's markers earn when theirs is the
// highest-ranked marker in the text.
var remoteClassOfRank = []RemoteClass{RemoteTerminal, RemoteAuthRefused, RemoteTerminal, RemoteTransient}

// remoteNoiseCarriesNoMarker says whether arbitrary noise can stand around
// the drawn markers: it must carry no marker of its own, or the case stops
// being about the markers the generator chose.
func remoteNoiseCarriesNoMarker(text string) bool {
	lower := strings.ToLower(text)
	for _, list := range remoteMarkerLists {
		if containsAny(lower, list) {
			return false
		}
	}
	return !remoteRefFailed.MatchString(lower) && !forkExhausted.MatchString(lower)
}

// genRemoteError draws a failed command's error: one marker from each list
// the draw included, surrounded by noise, in a random order. The returned
// ranks are the indices of the lists that contributed, lowest first.
func genRemoteError(tc hegel.TestCase) (string, []int) {
	var parts []string
	var ranks []int
	for rank, list := range remoteMarkerLists {
		if !hegel.Draw(tc, hegel.WeightedBooleans(0.5)) {
			continue
		}
		ranks = append(ranks, rank)
		parts = append(parts, hegel.Draw(tc, hegel.SampledFrom(list)))
		if hegel.Draw(tc, hegel.WeightedBooleans(0.5)) {
			noise := hegel.Draw(tc, hegel.Text().MinSize(0).MaxSize(60))
			// Noise that carries a marker of its own is redrawn, not assumed
			// away: an Assume here would let the engine shrink the whole case
			// out of the property, and the property is about the markers the
			// draw DID choose. The redraws are BOUNDED — a generator that kept
			// landing on markers would otherwise be a hang — and the fallback
			// is a line git really prints, carrying nothing the classifier reads.
			for tries := 0; tries < 8 && !remoteNoiseCarriesNoMarker(noise); tries++ {
				noise = hegel.Draw(tc, hegel.Text().MinSize(0).MaxSize(60))
			}
			if !remoteNoiseCarriesNoMarker(noise) {
				noise = "error: unable to create temporary object directory"
			}
			parts = append(parts, noise)
		}
	}
	// Interleave: the drawn order is already arbitrary, but git prints its
	// own answers in its own order, so a shuffled join is the honest shape.
	for i := len(parts) - 1; i > 0; i-- {
		j := hegel.Draw(tc, hegel.Integers(0, i))
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, "\n"), ranks
}

func TestPBTRemoteClassificationNeverWaitsOnAnAnswer(t *testing.T) {
	t.Parallel()
	var cases, waits, stops int
	hegel.Test(t, func(ht *hegel.T) {
		text, ranks := genRemoteError(ht)
		if len(ranks) == 0 {
			// A text with no marker at all: nothing was asked, so nothing
			// was refused either way — still worth the round trip below.
			ht.Assume(false)
		}
		cases++
		class := ClassifyRemote(fmt.Errorf("git push: exit status 128: %s", text))

		// The highest-ranked list that contributed decides. Everything
		// above it in the order wins over everything below, whatever order
		// the lines arrived in — which is the two-pass trick the classifier
		// exists for.
		want := remoteClassOfRank[ranks[0]]
		if class != want {
			ht.Fatalf("ClassifyRemote ranked a text carrying %q (rank %d, %v) as %v, want %v:\n%s",
				remoteMarkerLists[ranks[0]][0], ranks[0], class, class, want, text)
		}
		// The dangerous direction, stated on its own whatever the rank:
		// a text carrying a repository or who answer is never a wait.
		for _, rank := range ranks {
			if rank <= 2 && class == RemoteTransient {
				ht.Fatalf("a text carrying %q was classified RemoteTransient: a refusal waited through is a slower refusal",
					remoteMarkerLists[rank][0])
			}
		}
		if class == RemoteTransient {
			waits++
		} else {
			stops++
		}
	}, hegel.WithTestCases(400))
	t.Logf("%d generated remote failures: %d waited through, %d stopped for", cases, waits, stops)
	if cases == 0 {
		t.Fatal("the generator drew no cases: the property is vacuous")
	}
	// Not vacuous in the direction that matters: a generator that only ever
	// drew refusals would prove nothing about the waits.
	if waits == 0 {
		t.Errorf("no generated failure was waited through: the transient half of the classifier is unexercised")
	}
}

func TestPBTRemoteClassificationIgnoresCaseAndNeverInventsAClass(t *testing.T) {
	t.Parallel()
	hegel.Test(t, func(ht *hegel.T) {
		text, ranks := genRemoteError(ht)
		ht.Assume(len(ranks) > 0)
		lower := ClassifyRemote(fmt.Errorf("fetch: %s", text))
		upper := ClassifyRemote(fmt.Errorf("fetch: %s", strings.ToUpper(text)))
		if lower != upper {
			ht.Fatalf("the case of the remote's words changed the verdict: %v vs %v for\n%s", lower, upper, text)
		}

		// Arbitrary text the classifier reads as a wait really carries a pipe
		// marker — the classifier may recognise, never invent.
		noise := hegel.Draw(ht, hegel.Text().MinSize(0).MaxSize(120))
		class := ClassifyRemote(fmt.Errorf("fetch: %s", noise))
		lowered := strings.ToLower(noise)
		switch class {
		case RemoteTransient:
			if !containsAny(lowered, transientMarkers) && !remoteRefFailed.MatchString(lowered) &&
				!forkExhausted.MatchString(lowered) {
				ht.Fatalf("arbitrary text with no pipe marker was classified RemoteTransient:\n%q", noise)
			}
		case RemoteTerminal:
			if !containsAny(lowered, terminalMarkers) && !containsAny(lowered, deniedMarkers) {
				ht.Fatalf("arbitrary text with no refusal marker was classified RemoteTerminal:\n%q", noise)
			}
		case RemoteAuthRefused:
			if !containsAny(lowered, authMarkers) {
				ht.Fatalf("arbitrary text with no credential marker was classified RemoteAuthRefused:\n%q", noise)
			}
		case RemoteUnclassified:
			if containsAny(lowered, terminalMarkers) || containsAny(lowered, authMarkers) ||
				containsAny(lowered, deniedMarkers) || containsAny(lowered, transientMarkers) ||
				remoteRefFailed.MatchString(lowered) || forkExhausted.MatchString(lowered) {
				ht.Fatalf("text carrying a known marker was left unclassified:\n%q", noise)
			}
		}
	}, hegel.WithTestCases(300))
}
