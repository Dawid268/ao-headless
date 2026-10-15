package projectxfer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/projectxfer"
)

type fakeProjects struct {
	project projectsvc.Project
}

func (f *fakeProjects) Get(_ context.Context, id domain.ProjectID) (projectsvc.GetResult, error) {
	if id != f.project.ID {
		return projectsvc.GetResult{}, os.ErrNotExist
	}
	p := f.project
	return projectsvc.GetResult{Status: "ok", Project: &p}, nil
}

type fakeSessions struct {
	rows []domain.SessionRecord
}

func (f *fakeSessions) ListSessions(_ context.Context, _ domain.ProjectID) ([]domain.SessionRecord, error) {
	return f.rows, nil
}

type fakeChats struct{}

func (fakeChats) ProjectConversation(context.Context, domain.ProjectID) (domain.ConversationRecord, bool, error) {
	return domain.ConversationRecord{}, false, nil
}
func (fakeChats) ConversationForSession(context.Context, domain.SessionID) (domain.ConversationRecord, bool, error) {
	return domain.ConversationRecord{}, false, nil
}
func (fakeChats) LoadConversationMessages(context.Context, string) ([]projectxfer.ExportedMessage, []string, error) {
	return nil, nil, nil
}

type fakeWriter struct {
	t       *testing.T
	destDir string
	gotCfg  domain.ProjectConfig
	project projectsvc.Project
}

func (w *fakeWriter) SeedAndRegister(_ context.Context, archive io.Reader, in projectsvc.SeedAndRegisterInput) (projectsvc.SeedAndRegisterResult, error) {
	if err := os.MkdirAll(in.DestDir, 0o700); err != nil {
		return projectsvc.SeedAndRegisterResult{}, err
	}
	data, err := io.ReadAll(archive)
	if err != nil {
		return projectsvc.SeedAndRegisterResult{}, err
	}
	if len(data) == 0 {
		w.t.Fatal("expected non-empty source archive")
	}
	// Minimal unpack via project.SeedArchive so filters stay consistent.
	if _, err := projectsvc.SeedArchive(context.Background(), bytes.NewReader(data), projectsvc.SeedArchiveInput{
		DestDir: in.DestDir,
	}); err != nil {
		return projectsvc.SeedAndRegisterResult{}, err
	}
	name := "imported"
	if in.Name != nil {
		name = *in.Name
	}
	id := domain.ProjectID("imported")
	if in.ProjectID != nil {
		id = domain.ProjectID(*in.ProjectID)
	}
	w.project = projectsvc.Project{ID: id, Name: name, Path: in.DestDir, Kind: domain.ProjectKindSingleRepo}
	w.destDir = in.DestDir
	return projectsvc.SeedAndRegisterResult{Path: in.DestDir, Project: w.project}, nil
}

func (w *fakeWriter) UpdateSettings(_ context.Context, id domain.ProjectID, in projectsvc.UpdateSettingsInput) (projectsvc.Project, error) {
	w.gotCfg = in.Config
	w.project.ID = id
	w.project.Name = in.DisplayName
	w.project.Config = &in.Config
	return w.project, nil
}

func TestExportImportRoundTrip(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "node_modules"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(src, "node_modules", "x.js"), []byte("skip"), 0o600)

	cfg := domain.ProjectConfig{SessionPrefix: "xfer", DefaultBranch: "main"}
	project := projectsvc.Project{
		ID:     "src-proj",
		Name:   "Source",
		Path:   src,
		Kind:   domain.ProjectKindSingleRepo,
		Config: &cfg,
	}
	dataDir := t.TempDir()
	svc := projectxfer.New(projectxfer.Deps{
		DataDir:  dataDir,
		Projects: &fakeProjects{project: project},
		Sessions: &fakeSessions{rows: []domain.SessionRecord{{
			ID: "sess-1", Kind: domain.KindWorker, DisplayName: "One",
			CreatedAt: time.Now().UTC(), Metadata: domain.SessionMetadata{Branch: "feat/x"},
		}}},
		Chats:  fakeChats{},
		Writer: &fakeWriter{t: t},
	})

	jobID, err := svc.StartExport(context.Background(), project.ID, projectxfer.ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var artifact []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		progress, ok := svc.Registry().Get(jobID)
		if !ok {
			t.Fatal("job missing")
		}
		if progress.Error != "" {
			t.Fatalf("export failed: %s", progress.Error)
		}
		if progress.Done {
			var ready bool
			artifact, ready = svc.Registry().Artifact(jobID)
			if !ready {
				t.Fatal("done without artifact")
			}
			if progress.Percent != 100 {
				t.Fatalf("percent=%d", progress.Percent)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(artifact) == 0 {
		t.Fatal("timed out waiting for export")
	}

	writer := &fakeWriter{t: t}
	svc2 := projectxfer.New(projectxfer.Deps{
		DataDir: t.TempDir(),
		Writer:  writer,
	})
	dest := filepath.Join(t.TempDir(), "dest")
	importID, err := svc2.StartImport(context.Background(), bytes.NewReader(artifact), projectxfer.ImportOptions{
		DestDir: dest,
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		progress, ok := svc2.Registry().Get(importID)
		if !ok {
			t.Fatal("import job missing")
		}
		if progress.Error != "" {
			t.Fatalf("import failed: %s", progress.Error)
		}
		if progress.Done {
			if progress.ProjectID == "" {
				t.Fatal("missing project id")
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if writer.gotCfg.SessionPrefix != "xfer" {
		t.Fatalf("settings not applied: %#v", writer.gotCfg)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "node_modules")); !os.IsNotExist(err) {
		t.Fatalf("node_modules should be denied, err=%v", err)
	}
	hist := filepath.Join(dest, projectxfer.PartHistoryMD)
	if _, err := os.Stat(hist); err != nil {
		t.Fatal(err)
	}
}

func TestProgressJSONShape(t *testing.T) {
	p := projectxfer.Progress{JobID: "abc", Kind: "export", Phase: "packing_source", Percent: 40, Done: false}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"jobId", "kind", "phase", "percent", "done"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("missing %s in %s", key, b)
		}
	}
}
