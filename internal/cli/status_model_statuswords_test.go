package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pengelbrecht/ticfac/internal/runlife"
	"github.com/pengelbrecht/ticfac/internal/statusmodel"
	"github.com/pengelbrecht/ticfac/internal/tk"
)

// TestStatusJSONCarriesTheStatusWords (epic ymf, tick lck): the versioned
// model `status --json` emits carries the plain-language status word, the
// exception note, the phase track and the tick groups — the fields the watch
// redesign renders and the phone page shares — derived from the same
// checkout the rest of the model reads, not from a second gathering.
func TestStatusJSONCarriesTheStatusWords(t *testing.T) {
	now := time.Now()
	repo, home := modelFixture(t, now)

	life, err := runlife.Claim(repo, "epic-rmod")
	if err != nil {
		t.Fatalf("claim the run as this process: %v", err)
	}
	t.Cleanup(func() { life.Release("test") })

	realGraph := epicGraph
	t.Cleanup(func() { epicGraph = realGraph })
	epicGraph = func(context.Context, string, string) *tk.Graph { return fakeGraph() }
	t.Setenv("HOME", home)

	var out bytes.Buffer
	if code := Run([]string{"status", "--repo", repo, "--json", "epic-rmod"}, &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("a live run exited %d: %s", code, out.String())
	}
	var model statusmodel.Model
	if err := json.Unmarshal(out.Bytes(), &model); err != nil {
		t.Fatalf("the JSON model does not decode: %v\n%s", err, out.String())
	}

	if model.Waves == nil || len(*model.Waves) != 1 || len((*model.Waves)[0].Ticks) != 2 {
		t.Fatalf("the tracker's one wave did not ride: %+v", model.Waves)
	}
	ticks := (*model.Waves)[0].Ticks
	if ticks[0].Status != statusmodel.WordMerged {
		t.Errorf("the closed tick reads status %q, want %q", ticks[0].Status, statusmodel.WordMerged)
	}
	if ticks[1].Status != statusmodel.WordWritingCode {
		t.Errorf("the dispatched tick reads status %q, want %q", ticks[1].Status, statusmodel.WordWritingCode)
	}
	if ticks[1].Exception != nil {
		t.Errorf("the dispatched tick carries exception %q, want null on its first try", *ticks[1].Exception)
	}

	if len(model.Lifecycle.Track) != 5 || model.Lifecycle.Here != 0 {
		t.Errorf("the phase track reads %+v at %d, want five steps with the marker at building",
			model.Lifecycle.Track, model.Lifecycle.Here)
	}
	if model.Lifecycle.Track[0].Label != "building" || model.Lifecycle.Track[0].State != statusmodel.PhaseStateActive {
		t.Errorf("the track's first step reads %+v, want building active", model.Lifecycle.Track[0])
	}

	if model.Groups == nil {
		t.Fatal("the tracker answered and the model carries no groups")
	}
	if len(model.Groups.Done) != 1 || model.Groups.Done[0] != "t1" {
		t.Errorf("the DONE group reads %+v, want t1", model.Groups.Done)
	}
	if len(model.Groups.Now) != 1 || model.Groups.Now[0] != "t2" {
		t.Errorf("the NOW group reads %+v, want t2", model.Groups.Now)
	}
	if len(model.Groups.UpNext) != 0 || len(model.Groups.Held) != 0 {
		t.Errorf("the UP NEXT and HELD groups read %+v and %+v, want empty",
			model.Groups.UpNext, model.Groups.Held)
	}
}
