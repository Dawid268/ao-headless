// Package projectxfer packs and restores portable AO project state between hosts
// (local ↔ remote ↔ remote). Auth harnesses, live PIDs, and provider resume IDs
// are never included.
package projectxfer

import (
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const SchemaVersion = 1

// Manifest is the root description of an ao-project-export-v1 package.
type Manifest struct {
	SchemaVersion  int               `json:"schemaVersion"`
	ExportedAt     time.Time         `json:"exportedAt"`
	SourceProjectID domain.ProjectID `json:"sourceProjectId"`
	DisplayName    string            `json:"displayName"`
	Kind           domain.ProjectKind `json:"kind"`
	IncludeSecrets bool              `json:"includeSecrets"`
	Parts          []string          `json:"parts"`
	Notes          []string          `json:"notes,omitempty"`
}

// SettingsPayload is ProjectConfig plus cues (paths kept relative where possible).
type SettingsPayload struct {
	DisplayName string               `json:"displayName"`
	Config      domain.ProjectConfig `json:"config"`
	Cues        []domain.Cue         `json:"cues,omitempty"`
}

// SessionIndexEntry is historical session metadata without host-bound paths.
type SessionIndexEntry struct {
	ID           domain.SessionID     `json:"id"`
	Num          int                  `json:"num"`
	Kind         domain.SessionKind   `json:"kind"`
	Harness      domain.AgentHarness  `json:"harness,omitempty"`
	Mode         domain.SessionMode   `json:"mode,omitempty"`
	DisplayName  string               `json:"displayName,omitempty"`
	Terminated   bool                 `json:"terminated"`
	CreatedAt    time.Time            `json:"createdAt,omitempty"`
	Branch       string               `json:"branch,omitempty"`
}

// ConversationExport is one AO conversation snapshot for transfer.
type ConversationExport struct {
	Scope          domain.ConversationScope `json:"scope"`
	SessionID      string                   `json:"sessionId,omitempty"`
	Messages       []ExportedMessage        `json:"messages"`
	ActivitiesText []string                 `json:"activitiesText,omitempty"`
}

// ExportedMessage is a portable chat line.
type ExportedMessage struct {
	Role      string    `json:"role"`
	Origin    string    `json:"origin,omitempty"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
}

// Progress is the UI-facing job status.
type Progress struct {
	JobID      string  `json:"jobId"`
	Kind       string  `json:"kind"` // export | import
	Phase      string  `json:"phase"`
	Percent    int     `json:"percent"`
	BytesDone  int64   `json:"bytesDone,omitempty"`
	BytesTotal int64   `json:"bytesTotal,omitempty"`
	Detail     string  `json:"detail,omitempty"`
	Error      string  `json:"error,omitempty"`
	ProjectID  string  `json:"projectId,omitempty"`
	Done       bool    `json:"done"`
}

// ExportOptions controls what leaves the machine.
type ExportOptions struct {
	IncludeSecrets bool
}

// ImportOptions names the destination on the receiving host.
type ImportOptions struct {
	DestDir        string
	Name           *string
	ProjectID      *string
	IncludeSecrets bool
}

// Package part filenames inside the outer gzip tar.
const (
	PartManifest      = "manifest.json"
	PartSettings      = "settings.json"
	PartSessions      = "sessions-index.json"
	PartConversations = "conversations.json"
	PartHistoryMD     = "IMPORTED_CHAT_HISTORY.md"
	PartSourceTar     = "source.tar.gz"
	PartAttachments   = "attachments.tar.gz"
	PartArtifacts     = "artifacts.tar.gz"
)
