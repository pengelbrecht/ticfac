package cli

import (
	"context"

	"github.com/pengelbrecht/ticfac/internal/tk"
)

// tickLabels maps each tick id in repo to its short human label (tk.Label:
// the gloss, else the title cut to the gloss width), read through `tk list`.
//
// A label is how a person recognises a tick, never how a program identifies
// one, so a tracker that cannot be read costs the labels and nothing else:
// the map comes back empty, and every surface falls back to the bare id.
var tickLabels = func(ctx context.Context, repo string) map[string]string {
	out := map[string]string{}
	client, err := tk.NewContext(ctx, tk.Options{Dir: repo})
	if err != nil {
		return out
	}
	list, err := client.List(ctx)
	if err != nil {
		return out
	}
	for _, t := range list.Ticks {
		out[t.ID] = t.Label()
	}
	return out
}

// tickRef names a tick to a person: "id (label)", or the bare id.
func tickRef(labels map[string]string, id string) string {
	return tk.Ref(id, labels[id])
}
