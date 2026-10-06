package credentials

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadMissingFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)

	f, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom on missing file: %v", err)
	}
	if got := f.Get(KeyToken); got != "" {
		t.Errorf("Get(%s) = %q, want empty", KeyToken, got)
	}
}

func TestSetThenGetRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)

	f, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	f.Set(KeyURL, "https://ticks-factory.acme.workers.dev")
	f.Set(KeyToken, "tkf_abc")
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.Get(KeyURL); got != "https://ticks-factory.acme.workers.dev" {
		t.Errorf("factory_url = %q", got)
	}
	if got := reloaded.Get(KeyToken); got != "tkf_abc" {
		t.Errorf("factory_token = %q", got)
	}
}

func TestSavePreservesUnknownLinesAndComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	original := "# my factory config\nfactory_github_repo=acme/widgets\nsomething_else=keep me\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	f.Set(KeyURL, "https://f.example.com")
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"# my factory config", "factory_github_repo=acme/widgets", "something_else=keep me",
		"factory_url=https://f.example.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("saved file lost %q:\n%s", want, got)
		}
	}
}

func TestSetReplacesInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("factory_url=https://old\nfactory_token=t\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	f, _ := LoadFrom(path)
	f.Set(KeyURL, "https://new")
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "https://old") {
		t.Errorf("old value still present:\n%s", data)
	}
	if strings.Count(string(data), "factory_url=") != 1 {
		t.Errorf("factory_url duplicated:\n%s", data)
	}
}

// The file holds bearer tokens: it must never be group- or world-readable.
func TestSaveUsesOwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	path := filepath.Join(t.TempDir(), FileName)

	f, _ := LoadFrom(path)
	f.Set(KeyToken, "tkf_secret")
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

func TestSaveTightensPermissionsOnExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("factory_token=t\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, _ := LoadFrom(path)
	f.Set(KeyToken, "tkf_secret")
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

// ShellGetCommand's output must be Get's, byte for byte, over every file
// shape an operator's hand can leave behind — because two consumers read
// ~/.ticfacrc from two worlds: the Go side through Get, and the shell
// (pi's `!command` credential override, tick frr) through this command.
// They drifted once: the shell read demanded an exact `^key=` line and
// returned EVERY match, so a hand-edited `key = value` (spaces around the
// '=', which Get reads fine) sent pi an EMPTY credential and a duplicated
// key produced a two-line header value. Each case below is one shape of
// that drift; the missing-file case is the documented empty fallback.
func TestShellGetCommandAgreesWithGet(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shell command runs where pi runs: a POSIX shell")
	}
	for name, content := range map[string]string{
		"a plain line":                "factory_cloudflare_api_token=cf_plain\n",
		"hand-edited spaces":          "factory_cloudflare_api_token = cf_spaced \n",
		"leading whitespace":         "\t factory_cloudflare_api_token=cf_indented\n",
		"a duplicated key":            "factory_cloudflare_api_token=cf_first\nfactory_cloudflare_api_token=cf_second\n",
		"a commented-out key":         "# factory_cloudflare_api_token=cf_commented\n",
		"an indented comment":         "   # factory_cloudflare_api_token=cf_commented\n",
		"a longer key sharing prefix": "factory_cloudflare_api_token_extra=cf_other\n",
		"the key inside a comment tail": "x# factory_cloudflare_api_token=cf_other\n",
		"an empty value":              "factory_cloudflare_api_token=\n",
		"a value that keeps its equals": "factory_cloudflare_api_token=cf=a=b\n",
		"other keys ignored":          "factory_gateway_url=https://gateway.example.com/v1/acct/gw\nfactory_url=https://f.example.com\n",
		"blanks around the line":      "\n\n   factory_cloudflare_api_token=cf_blank_bordered\n\n",
	} {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, FileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		want := shellGetWantsGet(t, home, KeyCloudflareAPIToken, name)
		got := shellRun(t, home, ShellGetCommand(KeyCloudflareAPIToken), name)
		if got != want {
			t.Errorf("%s: the shell command printed %q, want Get's %q: the two readers of ~/.ticfacrc must not drift", name, got, want)
		}
	}

	// The file may not exist at all: the documented fallback of both readers
	// is the empty value, not an error pi's credential command can show.
	home := t.TempDir()
	if got, want := shellRun(t, home, ShellGetCommand(KeyCloudflareAPIToken), "no file"), shellGetWantsGet(t, home, KeyCloudflareAPIToken, "no file"); got != want {
		t.Errorf("no file: the shell command printed %q, want Get's %q", got, want)
	}
}

// shellGetWantsGet is Get's answer for the credential file in home.
func shellGetWantsGet(t *testing.T, home, key, name string) string {
	t.Helper()
	f, err := LoadFrom(filepath.Join(home, FileName))
	if err != nil {
		t.Fatalf("%s: LoadFrom: %v", name, err)
	}
	return f.Get(key)
}

// shellRun runs command under sh with HOME pointed at home and returns its
// stdout, one trailing newline trimmed: pi resolves `!command` values the
// same way, through a shell.
func shellRun(t *testing.T, home, command, name string) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("%s: no sh on PATH: the command runs where pi runs", name)
	}
	var stdout bytes.Buffer
	cmd := exec.Command(sh, "-c", command)
	cmd.Dir = home
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: run %q: %v", name, command, err)
	}
	return strings.TrimSuffix(stdout.String(), "\n")
}

func TestPathHonoursHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if want := filepath.Join(home, FileName); path != want {
		t.Errorf("Path() = %q, want %q", path, want)
	}
}
