package controllers

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	fsbrowsersvc "github.com/aoagents/agent-orchestrator/backend/internal/service/fsbrowser"
)

// DirectoryBrowserService lists and creates directories on the daemon host.
type DirectoryBrowserService interface {
	List(ctx context.Context, path string) (fsbrowsersvc.Listing, error)
	Mkdir(ctx context.Context, path string) (fsbrowsersvc.Listing, error)
}

// FSController owns the /fs routes used by remote clients to browse and create
// project destination folders. Listing is directories-only and dotfile-free by
// design: the connection credential already authorizes spawning agents (shell
// access), so this reveals nothing new — but there is no reason to serve more
// than the picker needs. Mkdir is limited to creating directories for the same
// picker (transfer / create-project), not arbitrary file writes.
type FSController struct {
	Svc DirectoryBrowserService
}

// Register mounts fs REST routes on the supplied router.
func (c *FSController) Register(r chi.Router) {
	r.Get("/fs/dirs", c.listDirs)
	r.Post("/fs/dirs", c.createDir)
}

func (c *FSController) listDirs(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/fs/dirs")
		return
	}
	listing, err := c.Svc.List(r.Context(), r.URL.Query().Get("path"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, listDirsResponse(listing))
}

func (c *FSController) createDir(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/fs/dirs")
		return
	}
	var in CreateDirRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	path := strings.TrimSpace(in.Path)
	if path == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "FS_PATH_REQUIRED", "path is required", nil)
		return
	}
	listing, err := c.Svc.Mkdir(r.Context(), path)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, listDirsResponse(listing))
}

func listDirsResponse(listing fsbrowsersvc.Listing) ListDirsResponse {
	out := ListDirsResponse{Path: listing.Path, Parent: listing.Parent, Entries: make([]FSEntry, len(listing.Entries)), Truncated: listing.Truncated}
	for i, entry := range listing.Entries {
		out.Entries[i] = FSEntry{Name: entry.Name, Path: entry.Path, GitRepo: entry.GitRepo}
	}
	return out
}
