package controllers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/projectxfer"
)

// ProjectTransferService is the controller-facing contract for export/import jobs.
type ProjectTransferService interface {
	StartExport(ctx context.Context, projectID domain.ProjectID, opts projectxfer.ExportOptions) (string, error)
	StartImport(ctx context.Context, archive io.Reader, opts projectxfer.ImportOptions) (string, error)
	Registry() *projectxfer.Registry
}

// ProjectTransferController owns portable project export/import job routes.
type ProjectTransferController struct {
	Svc ProjectTransferService
}

// Register mounts transfer routes.
func (c *ProjectTransferController) Register(r chi.Router) {
	r.Post("/projects/{id}/export-jobs", c.startExport)
	r.Post("/projects/import-jobs", c.startImport)
	r.Get("/project-transfer-jobs/{jobId}", c.jobStatus)
	r.Get("/project-transfer-jobs/{jobId}/artifact", c.jobArtifact)
	r.Delete("/project-transfer-jobs/{jobId}", c.cancelJob)
}

// StartExportJobRequest is the JSON body for POST …/export-jobs.
type StartExportJobRequest struct {
	IncludeSecrets bool `json:"includeSecrets,omitempty"`
}

// StartTransferJobAccepted returns the async job id.
type StartTransferJobAccepted struct {
	JobID string `json:"jobId"`
}

// ProjectTransferJobIDParam documents {jobId}.
type ProjectTransferJobIDParam struct {
	JobID string `path:"jobId" description:"Export or import job identifier."`
}

// ProjectImportJobQuery is the query string for POST /projects/import-jobs.
type ProjectImportJobQuery struct {
	DestDir        string `query:"destDir" description:"Absolute destination directory on this host."`
	IncludeSecrets bool   `query:"includeSecrets,omitempty" description:"Allow .env and key material from the package."`
	Name           string `query:"name,omitempty" description:"Optional display name override."`
	ProjectID      string `query:"projectId,omitempty" description:"Optional project id override."`
}

// SeedProjectQuery documents POST /projects/seed query parameters.
type SeedProjectQuery struct {
	DestDir        string `query:"destDir" description:"Absolute destination directory on this host."`
	IncludeSecrets bool   `query:"includeSecrets,omitempty" description:"Allow .env and key material in the archive."`
	Name           string `query:"name,omitempty" description:"Optional display name."`
	ProjectID      string `query:"projectId,omitempty" description:"Optional project id."`
}

func (c *ProjectTransferController) startExport(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/projects/{id}/export-jobs")
		return
	}
	var in StartExportJobRequest
	if err := decodeJSONStrict(r, &in); err != nil && !errors.Is(err, io.EOF) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	jobID, err := c.Svc.StartExport(r.Context(), projectID(r), projectxfer.ExportOptions{IncludeSecrets: in.IncludeSecrets})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, StartTransferJobAccepted{JobID: jobID})
}

func (c *ProjectTransferController) startImport(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/projects/import-jobs")
		return
	}
	dest := strings.TrimSpace(r.URL.Query().Get("destDir"))
	if dest == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "IMPORT_DEST_REQUIRED", "Query parameter destDir is required.", nil)
		return
	}
	includeSecrets, _ := strconv.ParseBool(r.URL.Query().Get("includeSecrets"))
	opts := projectxfer.ImportOptions{
		DestDir:        dest,
		IncludeSecrets: includeSecrets,
	}
	if name := strings.TrimSpace(r.URL.Query().Get("name")); name != "" {
		opts.Name = &name
	}
	if id := strings.TrimSpace(r.URL.Query().Get("projectId")); id != "" {
		opts.ProjectID = &id
	}
	body := io.LimitReader(r.Body, projectsvc.SeedMaxArchiveBytes()+64<<20)
	jobID, err := c.Svc.StartImport(r.Context(), body, opts)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, StartTransferJobAccepted{JobID: jobID})
}

func (c *ProjectTransferController) jobStatus(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/project-transfer-jobs/{jobId}")
		return
	}
	id := chi.URLParam(r, "jobId")
	progress, ok := c.Svc.Registry().Get(id)
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "TRANSFER_JOB_NOT_FOUND", "Transfer job not found.", nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, progress)
}

func (c *ProjectTransferController) jobArtifact(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/project-transfer-jobs/{jobId}/artifact")
		return
	}
	id := chi.URLParam(r, "jobId")
	data, ok := c.Svc.Registry().Artifact(id)
	if !ok {
		progress, exists := c.Svc.Registry().Get(id)
		if !exists {
			envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "TRANSFER_JOB_NOT_FOUND", "Transfer job not found.", nil)
			return
		}
		if progress.Error != "" {
			envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "TRANSFER_JOB_FAILED", progress.Error, nil)
			return
		}
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "TRANSFER_ARTIFACT_NOT_READY", "Export artifact is not ready yet.", map[string]any{
			"phase":   progress.Phase,
			"percent": progress.Percent,
			"done":    progress.Done,
		})
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="ao-project-export.tgz"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (c *ProjectTransferController) cancelJob(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/project-transfer-jobs/{jobId}")
		return
	}
	id := chi.URLParam(r, "jobId")
	if _, ok := c.Svc.Registry().Get(id); !ok {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "TRANSFER_JOB_NOT_FOUND", "Transfer job not found.", nil)
		return
	}
	c.Svc.Registry().Cancel(id)
	w.WriteHeader(http.StatusNoContent)
}
