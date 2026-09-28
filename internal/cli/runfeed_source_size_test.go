package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"unicode/utf16"
)

// The bare `ticfac` printed "# the feed read for run_… carried 1132 bytes
// against a 1130-byte standing size; following only what arrived" for every
// cloud run, on every invocation. Nothing had gone wrong: the factory's
// events route counted the feed with JavaScript's `text.length` — UTF-16
// code units — and every feed line the reconciler writes carries an em dash
// ("—"), one UTF-16 unit and three UTF-8 bytes. One em dash is the 2-byte
// gap. The factory now counts UTF-8 bytes; a deployment that predates the
// fix still counts code units, and the client reads that count as the same
// whole feed rather than as a bounded read. Only a read that actually came
// back short of the size the factory states is warned about.
func TestTheCloudFeedWarnsOnlyWhenARealReadCameBackShort(t *testing.T) {
	text := `{"schema_version":1,"at":"2026-09-28T01:00:00Z","run_id":"run_0d2e","tick_id":null,"attempt":null,` +
		`"stage":"run_finished","detail":"failed: the orchestrator exited 8 — boot 3"}` + "\n"
	utf8Bytes := len(text)
	utf16Units := len(utf16.Encode([]rune(text)))
	if utf8Bytes == utf16Units {
		t.Fatal("the fixture must carry a character whose UTF-8 and UTF-16 lengths differ")
	}

	for _, tc := range []struct {
		name     string
		total    int
		wantWarn bool
	}{
		{"a factory counting UTF-8 bytes", utf8Bytes, false},
		{"a factory that predates the fix, counting UTF-16 code units", utf16Units, false},
		{"a read that really came back short", utf8Bytes + 500, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, _ := newCloudFactory(t, func(request cloudFactoryRequest) (int, any) {
				return 200, map[string]any{
					"run_id": "run_0d2e", "state": "failed",
					"text": text, "bytes": tc.total, "total_bytes": tc.total,
				}
			})
			configureCloudFactory(t, endpoint)
			client, err := newCloudClient()
			if err != nil {
				t.Fatal(err)
			}
			var warn bytes.Buffer
			source := &cloudFeedSource{client: client, runID: "run_0d2e", warn: &warn}

			chunk, size, err := source.ReadAt(context.Background(), 0)
			if err != nil {
				t.Fatal(err)
			}
			if string(chunk) != text || size != int64(utf8Bytes) {
				t.Errorf("the read answered %d bytes at size %d, want the whole %d-byte feed", len(chunk), size, utf8Bytes)
			}
			warned := strings.Contains(warn.String(), "standing size")
			if warned != tc.wantWarn {
				t.Errorf("warned=%v, want %v: %q", warned, tc.wantWarn, warn.String())
			}
		})
	}
}
