package controllers

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	previewutil "github.com/aoagents/agent-orchestrator/backend/internal/preview"
)

const (
	artifactVersionsPath       = "/api/v1/sessions/{sessionId}/artifact-versions"
	artifactVersionPath        = "/api/v1/sessions/{sessionId}/artifact-versions/{version}"
	artifactVersionRestorePath = "/api/v1/sessions/{sessionId}/artifact-versions/restore"
)

// ArtifactVersionService is the version history of session artifact files.
type ArtifactVersionService interface {
	List(ctx context.Context, id domain.SessionID, relPath string) ([]domain.SessionArtifactVersion, error)
	ReadVersion(sess domain.SessionRecord, v domain.SessionArtifactVersion) ([]byte, error)
	RecordSave(ctx context.Context, sess domain.SessionRecord, relPath string) error
	Sync(ctx context.Context, sess domain.SessionRecord, relPath string) error
}

func artifactVersionSummary(v domain.SessionArtifactVersion) ArtifactVersionSummary {
	return ArtifactVersionSummary{
		Version:         v.Version,
		Size:            v.Size,
		Source:          string(v.Source),
		Harness:         string(v.Harness),
		FileFingerprint: v.SHA256,
		CreatedAt:       v.CreatedAt,
	}
}

// listedArtifactPath resolves the request's path to a file the session lists,
// writing the 404 itself when it is not one.
func (c *SessionsController) listedArtifactPath(w http.ResponseWriter, r *http.Request, rawPath string) (domain.Session, string, bool) {
	sess, err := c.Svc.Get(r.Context(), sessionID(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return domain.Session{}, "", false
	}
	clean, ok := previewutil.CleanWorkspacePath(rawPath)
	if !ok || !listsArtifactFile(sess.ArtifactFiles, clean) {
		writeArtifactFileNotFound(w, r)
		return domain.Session{}, "", false
	}
	return sess, clean, true
}

func writeArtifactVersionNotFound(w http.ResponseWriter, r *http.Request) {
	envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "ARTIFACT_VERSION_NOT_FOUND", "artifact version not found", nil)
}

// listArtifactVersions returns the kept versions of one listed artifact file,
// newest first.
func (c *SessionsController) listArtifactVersions(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil || c.ArtifactVersions == nil {
		apispec.NotImplemented(w, r, http.MethodGet, artifactVersionsPath)
		return
	}
	sess, clean, ok := c.listedArtifactPath(w, r, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	// An edit the observer has not yet noticed is recorded now, so the list is
	// current. A failure only leaves the edit for the observer.
	if err := c.ArtifactVersions.Sync(r.Context(), sess.SessionRecord, clean); err != nil {
		slog.Default().Debug("artifact versions: sync failed", "session", sess.ID, "path", clean, "err", err)
	}
	rows, err := c.ArtifactVersions.List(r.Context(), sess.ID, clean)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := ListArtifactVersionsResponse{Path: clean, Versions: make([]ArtifactVersionSummary, len(rows))}
	for i, v := range rows {
		out.Versions[i] = artifactVersionSummary(v)
	}
	envelope.WriteJSON(w, http.StatusOK, out)
}

// findArtifactVersion looks up one version of a file, writing the 404 itself.
func (c *SessionsController) findArtifactVersion(w http.ResponseWriter, r *http.Request, sess domain.Session, clean string, version int64) (domain.SessionArtifactVersion, bool) {
	rows, err := c.ArtifactVersions.List(r.Context(), sess.ID, clean)
	if err != nil {
		envelope.WriteError(w, r, err)
		return domain.SessionArtifactVersion{}, false
	}
	i := slices.IndexFunc(rows, func(v domain.SessionArtifactVersion) bool { return v.Version == version })
	if i < 0 {
		writeArtifactVersionNotFound(w, r)
		return domain.SessionArtifactVersion{}, false
	}
	return rows[i], true
}

// readArtifactVersionText reads a version's content, which must be UTF-8 text.
func (c *SessionsController) readArtifactVersionText(w http.ResponseWriter, r *http.Request, sess domain.Session, v domain.SessionArtifactVersion) (string, bool) {
	data, err := c.ArtifactVersions.ReadVersion(sess.SessionRecord, v)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		writeArtifactVersionNotFound(w, r)
		return "", false
	case err != nil:
		envelope.WriteError(w, r, err)
		return "", false
	case !utf8.Valid(data) || strings.ContainsRune(string(data), 0):
		envelope.WriteError(w, r, apierr.Invalid("ARTIFACT_FILE_BINARY", "Only UTF-8 text versions can be shown", nil))
		return "", false
	}
	return string(data), true
}

// getArtifactVersion returns one version of a listed artifact file with its
// text content.
func (c *SessionsController) getArtifactVersion(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil || c.ArtifactVersions == nil {
		apispec.NotImplemented(w, r, http.MethodGet, artifactVersionPath)
		return
	}
	version, err := strconv.ParseInt(chi.URLParam(r, "version"), 10, 64)
	if err != nil || version < 1 {
		writeArtifactVersionNotFound(w, r)
		return
	}
	sess, clean, ok := c.listedArtifactPath(w, r, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	v, ok := c.findArtifactVersion(w, r, sess, clean, version)
	if !ok {
		return
	}
	content, ok := c.readArtifactVersionText(w, r, sess, v)
	if !ok {
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ArtifactVersionResponse{ArtifactVersionSummary: artifactVersionSummary(v), Path: clean, Content: content})
}

// restoreArtifactVersion rewrites a listed artifact file with an earlier
// version's content. The restore lands as the file's newest version, so the
// history is never rewritten and a restore can itself be undone.
func (c *SessionsController) restoreArtifactVersion(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil || c.ArtifactVersions == nil {
		apispec.NotImplemented(w, r, http.MethodPost, artifactVersionRestorePath)
		return
	}
	var in RestoreArtifactVersionRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	expected := strings.TrimSpace(in.ExpectedFileFingerprint)
	if expected == "" {
		envelope.WriteError(w, r, apierr.Invalid("ARTIFACT_FILE_FINGERPRINT_REQUIRED", "expectedFileFingerprint is required", nil))
		return
	}
	sess, clean, ok := c.listedArtifactPath(w, r, in.Path)
	if !ok {
		return
	}
	v, ok := c.findArtifactVersion(w, r, sess, clean, in.Version)
	if !ok {
		return
	}
	content, ok := c.readArtifactVersionText(w, r, sess, v)
	if !ok {
		return
	}
	if err := c.writeArtifactSnapshotted(r.Context(), sess.SessionRecord, clean, expected, []byte(content)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeArtifactFileNotFound(w, r)
			return
		}
		envelope.WriteError(w, r, err)
		return
	}
	c.recordArtifactSave(r.Context(), sess.SessionRecord, clean)
	envelope.WriteJSON(w, http.StatusOK, ArtifactFileResponse{
		Path:            clean,
		Size:            int64(len(content)),
		FileFingerprint: artifactFingerprint([]byte(content)),
	})
}

// writeArtifactSnapshotted replaces a listed artifact file the way a save does.
// Under the edit lock it first records any content the observer has not
// versioned yet, so the write never destroys the only copy of an agent edit,
// and it releases the lock before the caller records the save.
func (c *SessionsController) writeArtifactSnapshotted(ctx context.Context, sess domain.SessionRecord, relPath, expected string, content []byte) error {
	artifactEditMu.Lock()
	defer artifactEditMu.Unlock()
	if c.ArtifactVersions != nil {
		if err := c.ArtifactVersions.Sync(ctx, sess, relPath); err != nil {
			slog.Default().Debug("artifact versions: sync before save failed", "session", sess.ID, "path", relPath, "err", err)
		}
	}
	return writeArtifactFile(sess.Metadata.ArtifactDir, relPath, expected, content)
}

// recordArtifactSave notes a save as a user version. The save already
// happened and its response must not depend on the client staying connected,
// so it runs on a context that outlives the request. A failure only delays the
// version: the observer records the content as an agent version on a later tick.
func (c *SessionsController) recordArtifactSave(ctx context.Context, sess domain.SessionRecord, relPath string) {
	if c.ArtifactVersions == nil {
		return
	}
	if err := c.ArtifactVersions.RecordSave(context.WithoutCancel(ctx), sess, relPath); err != nil {
		slog.Default().Warn("artifact versions: record save failed", "session", sess.ID, "path", relPath, "err", err)
	}
}
