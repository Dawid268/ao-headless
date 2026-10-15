package controllers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
)

// fakeArtifactVersions holds versions in memory; RecordSave appends the file's
// current content as a user version.
type fakeArtifactVersions struct {
	dir      string
	versions []domain.SessionArtifactVersion // oldest first
	blobs    map[string]string               // sha256 -> content
}

func (f *fakeArtifactVersions) add(content string, source domain.ArtifactVersionSource) {
	sum := artifactSHA(content)
	f.blobs[sum] = content
	f.versions = append(f.versions, domain.SessionArtifactVersion{
		Path: "plan.md", Version: int64(len(f.versions) + 1), SHA256: sum, Size: int64(len(content)),
		Source: source, CreatedAt: time.Date(2026, 10, 15, 12, len(f.versions), 0, 0, time.UTC),
	})
}

func (f *fakeArtifactVersions) List(_ context.Context, _ domain.SessionID, relPath string) ([]domain.SessionArtifactVersion, error) {
	var out []domain.SessionArtifactVersion
	for i := len(f.versions) - 1; i >= 0; i-- {
		if f.versions[i].Path == relPath {
			out = append(out, f.versions[i])
		}
	}
	return out, nil
}

func (f *fakeArtifactVersions) ReadVersion(_ domain.SessionRecord, v domain.SessionArtifactVersion) ([]byte, error) {
	content, ok := f.blobs[v.SHA256]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(content), nil
}

func (f *fakeArtifactVersions) Sync(_ context.Context, _ domain.SessionRecord, relPath string) error {
	data, err := os.ReadFile(filepath.Join(f.dir, relPath))
	if err != nil {
		return err
	}
	if n := len(f.versions); n == 0 || f.blobs[f.versions[n-1].SHA256] != string(data) {
		f.add(string(data), domain.ArtifactVersionSourceAgent)
	}
	return nil
}

func (f *fakeArtifactVersions) RecordSave(_ context.Context, _ domain.SessionRecord, relPath string) error {
	data, err := os.ReadFile(filepath.Join(f.dir, relPath))
	if err != nil {
		return err
	}
	f.add(string(data), domain.ArtifactVersionSourceUser)
	return nil
}

func artifactVersionServer(t *testing.T) (*httptest.Server, *fakeArtifactVersions, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plan.md"), []byte("v2 agent"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeArtifactVersions{dir: dir, blobs: map[string]string{}}
	fake.add("v1 agent", domain.ArtifactVersionSourceAgent)
	fake.add("v2 agent", domain.ArtifactVersionSourceAgent)

	svc := newFakeSessionService()
	s := svc.sessions["ao-1"]
	s.Metadata.ArtifactDir = dir
	s.ArtifactFiles = []domain.SessionArtifactFile{{Path: "plan.md", Name: "plan.md"}}
	svc.sessions["ao-1"] = s

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Sessions: svc, ArtifactVersions: fake}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv, fake, dir
}

func artifactVersionCall(t *testing.T, method, rawURL string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func TestArtifactVersionsListNewestFirstAndReadOne(t *testing.T) {
	srv, _, _ := artifactVersionServer(t)
	base := srv.URL + "/api/v1/sessions/ao-1/artifact-versions"

	status, body := artifactVersionCall(t, http.MethodGet, base+"?path="+url.QueryEscape("plan.md"), nil)
	var list struct {
		Versions []struct {
			Version int64  `json:"version"`
			Source  string `json:"source"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(body, &list); err != nil || status != http.StatusOK {
		t.Fatalf("list: status=%d body=%s err=%v", status, body, err)
	}
	if len(list.Versions) != 2 || list.Versions[0].Version != 2 || list.Versions[1].Version != 1 || list.Versions[0].Source != "agent" {
		t.Fatalf("versions = %+v, want [2, 1]", list.Versions)
	}

	status, body = artifactVersionCall(t, http.MethodGet, base+"/1?path=plan.md", nil)
	var one struct {
		Version int64  `json:"version"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &one); err != nil || status != http.StatusOK || one.Version != 1 || one.Content != "v1 agent" {
		t.Fatalf("read: status=%d body=%s", status, body)
	}

	for name, target := range map[string]string{
		"unlisted file":   base + "?path=other.md",
		"unknown version": base + "/9?path=plan.md",
		"bad version":     base + "/zero?path=plan.md",
		"escaping path":   base + "?path=" + url.QueryEscape("../plan.md"),
	} {
		if status, _ := artifactVersionCall(t, http.MethodGet, target, nil); status != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", name, status)
		}
	}
}

func TestArtifactVersionRestoreWritesTheFileAndRecordsAUserVersion(t *testing.T) {
	srv, fake, dir := artifactVersionServer(t)
	restore := srv.URL + "/api/v1/sessions/ao-1/artifact-versions/restore"

	// A restore against a stale fingerprint is refused and leaves the file be.
	status, _ := artifactVersionCall(t, http.MethodPost, restore, map[string]any{"path": "plan.md", "version": 1, "expectedFileFingerprint": artifactSHA("something else")})
	if status != http.StatusConflict {
		t.Fatalf("stale restore: status = %d, want 409", status)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "plan.md")); string(data) != "v2 agent" {
		t.Fatalf("file after a refused restore = %q", data)
	}

	status, body := artifactVersionCall(t, http.MethodPost, restore, map[string]any{"path": "plan.md", "version": 1, "expectedFileFingerprint": artifactSHA("v2 agent")})
	if status != http.StatusOK {
		t.Fatalf("restore: status=%d body=%s", status, body)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "plan.md")); string(data) != "v1 agent" {
		t.Fatalf("file after restore = %q, want v1 agent", data)
	}
	last := fake.versions[len(fake.versions)-1]
	if len(fake.versions) != 3 || last.Version != 3 || last.Source != domain.ArtifactVersionSourceUser {
		t.Fatalf("versions = %+v, want a third, user-written one", fake.versions)
	}

	status, _ = artifactVersionCall(t, http.MethodPost, restore, map[string]any{"path": "plan.md", "version": 7, "expectedFileFingerprint": artifactSHA("v1 agent")})
	if status != http.StatusNotFound {
		t.Fatalf("unknown version: status = %d, want 404", status)
	}
}

func TestArtifactVersionsAreNotImplementedWithoutAService(t *testing.T) {
	srv, _ := artifactFileServer(t, map[string]string{"plan.md": "x"})
	status, _ := artifactVersionCall(t, http.MethodGet, srv.URL+"/api/v1/sessions/ao-1/artifact-versions?path=plan.md", nil)
	if status != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", status)
	}
}
