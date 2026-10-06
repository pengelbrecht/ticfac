package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func digestOf(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

// img is a dated image whose digest is derived from its tag's first byte.
func img(tag string, daysAgo int) registryImage {
	return registryImage{
		Tag:     tag,
		Digest:  digestOf(tag[0]),
		Created: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -daysAgo),
	}
}

func tags(images []registryImage) []string {
	var out []string
	for _, i := range images {
		out = append(out, i.Tag)
	}
	return out
}

func TestSelectImagesToPruneKeepsTheNewestAndDeletesOldestFirst(t *testing.T) {
	images := []registryImage{img("a", 1), img("b", 7), img("c", 2), img("d", 5), img("e", 3), img("f", 6), img("g", 4)}
	got := tags(selectImagesToPrune(images, 3, []string{digestOf('a')}))
	want := []string{"b", "f", "d", "g"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("pruned %v, want %v (oldest first, newest 3 kept)", got, want)
	}
}

// The image the application serves is kept however old it is: deleting it
// leaves an application that cannot start an instance.
func TestSelectImagesToPruneNeverDeletesAProtectedImage(t *testing.T) {
	images := []registryImage{img("a", 1), img("b", 2), img("c", 3), img("s", 90), img("r", 80)}
	got := selectImagesToPrune(images, 2, []string{digestOf('s'), "", digestOf('r')})
	if len(got) != 1 || got[0].Tag != "c" {
		t.Errorf("pruned %v, want only c: s is served and r a rollout's target", tags(got))
	}
}

// Deleting a tag can delete the manifest under it, so a tag that shares a
// digest with a kept image is kept too.
func TestSelectImagesToPruneKeepsTagsSharingAKeptDigest(t *testing.T) {
	old := img("x", 50)
	old.Digest = digestOf('a')
	images := []registryImage{img("a", 1), img("b", 2), img("c", 3), old}
	got := tags(selectImagesToPrune(images, 2, []string{digestOf('a')}))
	if fmt.Sprint(got) != fmt.Sprint([]string{"c"}) {
		t.Errorf("pruned %v, want [c]: x shares the kept image's digest", got)
	}
}

// An image that cannot be dated or identified is never deleted blind.
func TestSelectImagesToPruneKeepsUnreadableImages(t *testing.T) {
	undated := img("u", 0)
	undated.Created = time.Time{}
	nodigest := img("n", 100)
	nodigest.Digest = ""
	images := []registryImage{img("a", 1), img("b", 2), img("c", 3), undated, nodigest}
	got := tags(selectImagesToPrune(images, 2, []string{digestOf('a')}))
	if fmt.Sprint(got) != fmt.Sprint([]string{"c"}) {
		t.Errorf("pruned %v, want [c]", got)
	}
}

// A keep below the floor is raised to it, so a mistyped 0 cannot empty the
// repository down to the served image.
func TestSelectImagesToPruneFloorsKeep(t *testing.T) {
	images := []registryImage{img("a", 1), img("b", 2), img("c", 3)}
	got := tags(selectImagesToPrune(images, 0, []string{digestOf('z')}))
	if fmt.Sprint(got) != fmt.Sprint([]string{"c"}) {
		t.Errorf("pruned %v, want [c] with keep floored at %d", got, imageKeepFloor)
	}
}

func TestSelectImagesToPruneNothingToDo(t *testing.T) {
	images := []registryImage{img("a", 1), img("b", 2)}
	if got := selectImagesToPrune(images, 5, []string{digestOf('a')}); len(got) != 0 {
		t.Errorf("pruned %v from a repository under the keep count", tags(got))
	}
}

func TestUniqueLayerBytesCountsASharedLayerOnce(t *testing.T) {
	a := registryImage{Layers: []registryBlob{{"sha256:base", 100}, {"sha256:a", 10}}}
	b := registryImage{Layers: []registryBlob{{"sha256:base", 100}, {"sha256:b", 20}}}
	if got := uniqueLayerBytes([]registryImage{a, b}); got != 130 {
		t.Errorf("uniqueLayerBytes = %d, want 130", got)
	}
}

// fakeRegistry serves a managed-registry repository's tags, manifests and
// config blobs the way the registry does, digest pseudo-tags included.
type fakeRegistry struct {
	images []registryImage
	repo   string
	server *httptest.Server
}

func newFakeRegistry(t *testing.T, images []registryImage) *fakeRegistry {
	return newFakeRegistryFor(t, LegacyContainerAppName, images)
}

func newFakeRegistryFor(t *testing.T, repo string, images []registryImage) *fakeRegistry {
	t.Helper()
	r := &fakeRegistry{images: images, repo: repo}
	prefix := "/v2/acct/" + repo + "/"
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if user, pass, ok := req.BasicAuth(); !ok || user != "v1" || pass != "fake-registry-password" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		path, ok := strings.CutPrefix(req.URL.Path, prefix)
		if !ok {
			http.NotFound(w, req)
			return
		}
		switch {
		case path == "tags/list":
			var list []string
			for _, i := range r.images {
				list = append(list, i.Tag, i.Digest)
			}
			json.NewEncoder(w).Encode(map[string]any{"tags": list})
		case strings.HasPrefix(path, "manifests/"):
			tag := strings.TrimPrefix(path, "manifests/")
			for _, i := range r.images {
				if i.Tag == tag {
					w.Header().Set("Docker-Content-Digest", i.Digest)
					json.NewEncoder(w).Encode(map[string]any{
						"config": map[string]any{"digest": "sha256:config-" + i.Tag, "size": 1},
						"layers": i.Layers,
					})
					return
				}
			}
			http.NotFound(w, req)
		case strings.HasPrefix(path, "blobs/sha256:config-"):
			tag := strings.TrimPrefix(path, "blobs/sha256:config-")
			for _, i := range r.images {
				if i.Tag == tag {
					json.NewEncoder(w).Encode(map[string]any{"created": i.Created})
					return
				}
			}
			http.NotFound(w, req)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.server.Close)
	return r
}

// A deploy deletes the 0.x repository's images wholesale when it deletes the
// 0.x application — no keep-back, because with no application serving an
// image none is protected, and the ~45 GB it held was the bulk of the
// account's image storage.
func TestDeployEmptiesTheLegacyImageRepository(t *testing.T) {
	var images []registryImage
	for i, tag := range []string{"t0", "t1", "t2", "t3", "t4", "t5", "t6", "t7"} {
		images = append(images, registryImage{
			Tag:     tag,
			Digest:  digestOf(byte('a' + i)),
			Created: time.Date(2026, 9, 1+i, 0, 0, 0, 0, time.UTC),
			Layers:  []registryBlob{{"sha256:own-" + tag, 800_000_000}},
		})
	}
	reg := newFakeRegistry(t, images)
	t.Setenv("FAKE_WRANGLER_REGISTRY_HOST", strings.TrimPrefix(reg.server.URL, "http://"))
	stateDir := t.TempDir()
	t.Setenv("FAKE_WRANGLER_STATE", stateDir)
	t.Setenv("FAKE_WRANGLER_LOG", filepath.Join(stateDir, "wrangler.log"))
	w := &wrangler{bin: filepath.Join(testdataDir, "fake-wrangler.sh"), dir: t.TempDir(), out: io.Discard}

	var out bytes.Buffer
	deleteRepositoryImages(context.Background(), w, &out, Options{registryBaseURL: reg.server.URL}, LegacyContainerAppName)

	if !strings.Contains(out.String(), "deleted 8") {
		t.Errorf("the deletion is not reported:\n%s", out.String())
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, "deleted-images"))
	if err != nil {
		t.Fatalf("no image was deleted: %v\n%s", err, out.String())
	}
	if got := len(strings.Fields(string(raw))); got != len(images) {
		t.Errorf("deleted %d tags, want %d", got, len(images))
	}
	if strings.Contains(out.String(), "fake-registry-password") {
		t.Error("the registry password reached the deploy's output")
	}
}

// A repository that cannot be read is a warning, never a failed deploy, and
// nothing is deleted.
func TestDeleteRepositoryImagesWithoutRegistryAccessWarnsAndDeletesNothing(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("FAKE_WRANGLER_STATE", stateDir)
	t.Setenv("FAKE_WRANGLER_LOG", filepath.Join(stateDir, "wrangler.log"))
	w := &wrangler{bin: filepath.Join(testdataDir, "fake-wrangler.sh"), dir: t.TempDir(), out: io.Discard}

	var out bytes.Buffer
	deleteRepositoryImages(context.Background(), w, &out, Options{}, LegacyContainerAppName)
	if !strings.Contains(out.String(), "WARNING: not deleting "+LegacyContainerAppName+" images") {
		t.Errorf("the skipped deletion is not reported:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(stateDir, "deleted-images")); err == nil {
		t.Error("an image was deleted without a readable registry")
	}
}

// Storage near the account limit is flagged even when nothing can be pruned.
func TestPruneFlagsStorageNearTheLimit(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("FAKE_WRANGLER_STATE", stateDir)
	t.Setenv("FAKE_WRANGLER_LOG", filepath.Join(stateDir, "wrangler.log"))
	var images []registryImage
	for i := 0; i < 4; i++ {
		tag := fmt.Sprintf("t%d", i)
		images = append(images, registryImage{
			Tag: tag, Digest: digestOf(byte('a' + i)),
			Created: time.Date(2026, 9, 1+i, 0, 0, 0, 0, time.UTC),
			Layers:  []registryBlob{{"sha256:own-" + tag, 10_000_000_000}},
		})
	}
	reg := newFakeRegistryFor(t, FactorySandboxImageRepo, images)
	t.Setenv("FAKE_WRANGLER_REGISTRY_HOST", "registry.test")
	w := &wrangler{bin: filepath.Join(testdataDir, "fake-wrangler.sh"), dir: t.TempDir(), out: io.Discard}

	var out bytes.Buffer
	pruneRepositoryImages(context.Background(), w, &out, Options{registryBaseURL: reg.server.URL},
		FactorySandboxImageRepo, factorySandboxKeepNewest, []string{digestOf('a')}, "the pinned image")
	if !strings.Contains(out.String(), "WARNING: "+FactorySandboxImageRepo+" images still hold 40.0 GB") {
		t.Errorf("storage near the limit is not flagged:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(stateDir, "deleted-images")); err == nil {
		t.Error("an image was deleted from a repository within the keep count")
	}
}

// A FactorySandbox image a live run pins is never pruned (umq v1d, [A4]): the
// pins come from D1, and an answer that cannot be read stops the prune.
func TestPinnedDigestsReadTheLiveRunsPins(t *testing.T) {
	pinned := "registry.cloudflare.com/acct/" + FactorySandboxImageRepo + "@" + digestOf('d')
	out := "banner line\n[{\"results\":[{\"image\":\"" + pinned + "\"},{\"image\":\"no digest\"}],\"success\":true,\"meta\":{}}]\n"
	got, ok := pinnedDigests(out)
	if !ok || len(got) != 1 || got[0] != digestOf('d') {
		t.Fatalf("pinnedDigests = %v, %v; want [%s], true", got, ok, digestOf('d'))
	}
	if got, ok := pinnedDigests("[{\"results\":[],\"success\":true}]"); !ok || len(got) != 0 {
		t.Errorf("no live pins: got %v, %v; want [], true", got, ok)
	}
	for _, bad := range []string{"", "not json", "[]", "[{\"results\":[],\"success\":false}]"} {
		if _, ok := pinnedDigests(bad); ok {
			t.Errorf("pinnedDigests(%q) was read as an answer", bad)
		}
	}

	// The selection keeps the newest three and the pinned one, however old.
	var images []registryImage
	for i := 0; i < 6; i++ {
		images = append(images, registryImage{
			Tag: fmt.Sprintf("t%d", i), Digest: digestOf(byte('a' + i)),
			Created: time.Date(2026, 9, 1+i, 0, 0, 0, 0, time.UTC),
		})
	}
	pruned := tags(selectImagesToPrune(images, factorySandboxKeepNewest, []string{digestOf('a')}))
	if strings.Join(pruned, ",") != "t1,t2" {
		t.Errorf("pruned %v, want [t1 t2] (t0 is pinned, t3-t5 are the newest three)", pruned)
	}
}
