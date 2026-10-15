package host

import (
	"encoding/json"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The command follows any global flags, and the values of those that take one.
func TestAgentCommand(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"snapshot", "-i"}, "snapshot"},
		{[]string{"--json", "snapshot", "-i"}, "snapshot"},
		{[]string{"--json", "dialog", "accept"}, "dialog"},
		{[]string{"--session", "a", "--json", "dialog", "dismiss"}, "dialog"},
		{[]string{"-p", "kernel", "--max-output", "500", "open", "example.com"}, "open"},
		{[]string{"--headed", "false", "--cdp=9222", "click", "@e1"}, "click"},
		{[]string{"--json"}, ""},
	} {
		if got := agentCommand(c.args); got != c.want {
			t.Errorf("agentCommand(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

// agentHost is a host whose agent-browser is a script: it prints its arguments
// and saves a screenshot where it is told to, as agent-browser does.
func agentHost(t *testing.T) *host {
	dir := t.TempDir()
	ab := filepath.Join(dir, "agent-browser")
	script := `#!/bin/sh
shift 5 # --cdp 9222 --session poc --no-auto-dialog
echo "args: $*"
case " $* " in *" screenshot "*) for a; do :; done; echo png > "$a"; echo "saved to $a" ;; esac
`
	if err := os.WriteFile(ab, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	old := agentDir
	agentDir = filepath.Join(dir, "work")
	t.Cleanup(func() { agentDir = old })
	return &host{abPath: ab, cdpPort: 9222, tabs: map[string]*tab{}}
}

type agentResp struct {
	Output, Error string
	Files         map[string][]byte
}

func runCommand(t *testing.T, h *host, args []string, files map[string][]byte) agentResp {
	body, _ := json.Marshal(map[string]any{"args": args, "files": files})
	rec := httptest.NewRecorder()
	h.handleAgent(rec, httptest.NewRequest("POST", "/agent", strings.NewReader(string(body))))
	var r agentResp
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("%q: %v (%s)", args, err, rec.Body)
	}
	return r
}

func TestHandleAgent(t *testing.T) {
	h := agentHost(t) // one script: a Mac scans each new one for seconds on its first run
	// `--json snapshot` dates the agent's refs like `snapshot` does, and a
	// dialog gates only the tab the agent works in, never `dialog` itself.
	h.tabs["person"], h.tabs["agent"] = &tab{ID: "person"}, &tab{ID: "agent"}
	h.order = []string{"person", "agent"} // the agent's: the newest
	h.setDialog("person", "confirm: Leave?")
	if r := runCommand(t, h, []string{"--json", "snapshot", "-i"}, nil); r.Error != "" {
		t.Fatalf("a dialog in the person's tab refused the agent: %+v", r)
	}
	if h.ctl.snapshotAt().IsZero() {
		t.Error("`--json snapshot` did not date the refs")
	}
	h.setDialog("agent", "alert: Saved")
	if r := runCommand(t, h, []string{"--json", "dialog", "accept"}, nil); r.Error != "" {
		t.Errorf("`--json dialog accept` refused: %+v", r)
	}
	h.setDialog("agent", "")

	// Relative paths are to the agent's directory: an upload's files go up and
	// reach Chromium in full, a screenshot comes back by the name it was given,
	// folder and all.
	r := runCommand(t, h, []string{"upload", "#file", "docs/a.txt"}, map[string][]byte{"docs/a.txt": []byte("hi")})
	if b, err := os.ReadFile(filepath.Join(agentDir, "docs/a.txt")); err != nil || string(b) != "hi" {
		t.Errorf("uploaded file: %q, %v", b, err)
	}
	if !strings.Contains(r.Output, "args: upload #file "+filepath.Join(agentDir, "docs/a.txt")) {
		t.Errorf("upload path not made absolute: %q", r.Output)
	}
	r = runCommand(t, h, []string{"screenshot", "./shots/a.png"}, nil)
	if keys := slices.Collect(maps.Keys(r.Files)); len(keys) != 1 || string(r.Files["./shots/a.png"]) != "png\n" {
		t.Errorf("files back: %q (output %q)", keys, r.Output)
	}
	// The output names files saved where agent-browser chose; "./shot.png" is not "/shot.png".
	var found []string
	for _, m := range absPath.FindAllStringSubmatch(`saved to ./shot.png, "/tmp/a.png" and /b c/d`, -1) {
		found = append(found, m[1])
	}
	if want := []string{"/tmp/a.png", "/b"}; !slices.Equal(found, want) {
		t.Errorf("absolute paths in the output: %q, want %q", found, want)
	}
}
