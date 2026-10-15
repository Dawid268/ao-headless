package controllers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/projectxfer"
)

type fakeTransferSvc struct {
	reg      *projectxfer.Registry
	exportID string
	importID string
}

func (f *fakeTransferSvc) StartExport(_ context.Context, _ domain.ProjectID, _ projectxfer.ExportOptions) (string, error) {
	if f.exportID == "" {
		f.exportID = f.reg.Begin("export")
		f.reg.Complete(f.exportID, "proj", []byte("gzip-bytes"))
	}
	return f.exportID, nil
}

func (f *fakeTransferSvc) StartImport(_ context.Context, _ io.Reader, _ projectxfer.ImportOptions) (string, error) {
	if f.importID == "" {
		f.importID = f.reg.Begin("import")
		f.reg.Complete(f.importID, "imported", nil)
	}
	return f.importID, nil
}

func (f *fakeTransferSvc) Registry() *projectxfer.Registry { return f.reg }

func TestProjectTransferAPI_ExportImportJobs(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := projectxfer.NewRegistry()
	svc := &fakeTransferSvc{reg: reg}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		ProjectTransfer: svc,
	}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/projects/demo/export-jobs", `{"includeSecrets":false}`)
	if status != http.StatusAccepted {
		t.Fatalf("start export = %d, want 202; body=%s", status, body)
	}
	var started struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(body, &started); err != nil || started.JobID == "" {
		t.Fatalf("export job response = %s", body)
	}

	body, status, _ = doRequest(t, srv, http.MethodGet, "/api/v1/project-transfer-jobs/"+started.JobID, "")
	if status != http.StatusOK {
		t.Fatalf("job status = %d; body=%s", status, body)
	}
	if !strings.Contains(string(body), `"done":true`) {
		t.Fatalf("expected done job, got %s", body)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/project-transfer-jobs/"+started.JobID+"/artifact", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("artifact status = %d", resp.StatusCode)
	}
	artifact, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(artifact) != "gzip-bytes" {
		t.Fatalf("artifact = %q", artifact)
	}

	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/projects/import-jobs", "pkg")
	assertErrorCode(t, body, status, http.StatusBadRequest, "IMPORT_DEST_REQUIRED")

	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/projects/import-jobs?destDir=/tmp/dest", "pkg")
	if status != http.StatusAccepted {
		t.Fatalf("start import = %d; body=%s", status, body)
	}
}
