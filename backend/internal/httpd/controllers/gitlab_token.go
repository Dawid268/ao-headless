package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

const gitlabTokenFile = "gitlab-token"

// GitLabTokenStatusResponse reports whether a host token is stored. It never
// returns the secret.
type GitLabTokenStatusResponse struct {
	Configured bool `json:"configured"`
}

// PutGitLabTokenRequest stores a GitLab personal access token on this host.
type PutGitLabTokenRequest struct {
	Token string `json:"token"`
}

func gitlabTokenPath(dataDir string) string {
	return filepath.Join(dataDir, gitlabTokenFile)
}

func (c *SystemController) gitlabTokenStatus(w http.ResponseWriter, r *http.Request) {
	if c.DataDir == "" {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/system/gitlab-token")
		return
	}
	raw, err := os.ReadFile(gitlabTokenPath(c.DataDir))
	configured := err == nil && strings.TrimSpace(string(raw)) != ""
	if err != nil && !os.IsNotExist(err) {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, GitLabTokenStatusResponse{Configured: configured})
}

func (c *SystemController) putGitLabToken(w http.ResponseWriter, r *http.Request) {
	if c.DataDir == "" {
		apispec.NotImplemented(w, r, http.MethodPut, "/api/v1/system/gitlab-token")
		return
	}
	var body PutGitLabTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	token := strings.TrimSpace(body.Token)
	if token == "" {
		envelope.WriteError(w, r, errors.New("gitlab token is required"))
		return
	}
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	if err := os.WriteFile(gitlabTokenPath(c.DataDir), []byte(token+"\n"), 0o600); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, GitLabTokenStatusResponse{Configured: true})
}
