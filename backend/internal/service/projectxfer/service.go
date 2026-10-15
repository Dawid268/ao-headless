package projectxfer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
)

// ProjectReader loads the portable bits of one project.
type ProjectReader interface {
	Get(ctx context.Context, id domain.ProjectID) (projectsvc.GetResult, error)
}

// CueReader lists cues for a project.
type CueReader interface {
	SelectCuesByProject(ctx context.Context, projectID domain.ProjectID) ([]domain.Cue, error)
}

// SessionReader lists sessions for a project.
type SessionReader interface {
	ListSessions(ctx context.Context, project domain.ProjectID) ([]domain.SessionRecord, error)
}

// ConversationReader loads AO chat history for transfer.
type ConversationReader interface {
	ProjectConversation(ctx context.Context, project domain.ProjectID) (domain.ConversationRecord, bool, error)
	ConversationForSession(ctx context.Context, session domain.SessionID) (domain.ConversationRecord, bool, error)
	LoadConversationMessages(ctx context.Context, conversationID string) ([]ExportedMessage, []string, error)
}

// ProjectWriter registers and configures a project on the receiving host.
type ProjectWriter interface {
	SeedAndRegister(ctx context.Context, archive io.Reader, in projectsvc.SeedAndRegisterInput) (projectsvc.SeedAndRegisterResult, error)
	UpdateSettings(ctx context.Context, id domain.ProjectID, in projectsvc.UpdateSettingsInput) (projectsvc.Project, error)
}

// CueWriter inserts transferred cues.
type CueWriter interface {
	InsertCue(ctx context.Context, cue domain.Cue) error
}

// Service runs export/import jobs against local durable state.
type Service struct {
	dataDir string
	reg     *Registry
	projects ProjectReader
	cues     CueReader
	sessions SessionReader
	chats    ConversationReader
	writer   ProjectWriter
	cueWrite CueWriter
	clock    func() time.Time
}

// Deps wires durable collaborators.
type Deps struct {
	DataDir   string
	Registry  *Registry
	Projects  ProjectReader
	Cues      CueReader
	Sessions  SessionReader
	Chats     ConversationReader
	Writer    ProjectWriter
	CueWrite  CueWriter
	Clock     func() time.Time
}

// New builds a transfer service.
func New(d Deps) *Service {
	reg := d.Registry
	if reg == nil {
		reg = NewRegistry()
	}
	clock := d.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Service{
		dataDir:  d.DataDir,
		reg:      reg,
		projects: d.Projects,
		cues:     d.Cues,
		sessions: d.Sessions,
		chats:    d.Chats,
		writer:   d.Writer,
		cueWrite: d.CueWrite,
		clock:    clock,
	}
}

// Registry exposes job progress to HTTP.
func (s *Service) Registry() *Registry { return s.reg }

// StartExport begins an async export; poll Registry for progress and Artifact.
func (s *Service) StartExport(ctx context.Context, projectID domain.ProjectID, opts ExportOptions) (string, error) {
	if s.projects == nil {
		return "", fmt.Errorf("project transfer: projects reader not configured")
	}
	got, err := s.projects.Get(ctx, projectID)
	if err != nil {
		return "", err
	}
	if got.Project == nil {
		return "", fmt.Errorf("project %s is not available for export", projectID)
	}
	jobID := s.reg.start("export")
	go s.runExport(jobID, *got.Project, opts)
	return jobID, nil
}

func (s *Service) runExport(jobID string, project projectsvc.Project, opts ExportOptions) {
	ctx := context.Background()
	s.reg.set(jobID, func(p *Progress) {
		p.Phase = "packing_state"
		p.Percent = 5
		p.Detail = "Collecting settings"
	})
	pkg, err := s.buildPackage(ctx, project, opts, func(phase string, percent int, detail string) {
		s.reg.set(jobID, func(p *Progress) {
			p.Phase = phase
			p.Percent = percent
			p.Detail = detail
		})
	})
	if err != nil {
		s.reg.fail(jobID, err)
		return
	}
	s.reg.succeed(jobID, string(project.ID), pkg)
}

// StartImport begins an async import from a package reader.
func (s *Service) StartImport(ctx context.Context, archive io.Reader, opts ImportOptions) (string, error) {
	if s.writer == nil {
		return "", fmt.Errorf("project transfer: writer not configured")
	}
	if strings.TrimSpace(opts.DestDir) == "" {
		return "", fmt.Errorf("destDir is required")
	}
	data, err := io.ReadAll(io.LimitReader(archive, projectsvc.SeedMaxArchiveBytes()+64<<20))
	if err != nil {
		return "", fmt.Errorf("read import package: %w", err)
	}
	jobID := s.reg.start("import")
	go s.runImport(jobID, data, opts)
	return jobID, nil
}

func (s *Service) runImport(jobID string, data []byte, opts ImportOptions) {
	ctx := context.Background()
	s.reg.set(jobID, func(p *Progress) {
		p.Phase = "importing"
		p.Percent = 10
		p.Detail = "Reading package"
	})
	projectID, err := s.applyPackage(ctx, bytes.NewReader(data), opts, func(phase string, percent int, detail string) {
		s.reg.set(jobID, func(p *Progress) {
			p.Phase = phase
			p.Percent = percent
			p.Detail = detail
		})
	})
	if err != nil {
		s.reg.fail(jobID, err)
		return
	}
	s.reg.succeed(jobID, projectID, nil)
}

func (s *Service) buildPackage(
	ctx context.Context,
	project projectsvc.Project,
	opts ExportOptions,
	report func(phase string, percent int, detail string),
) ([]byte, error) {
	report("packing_state", 8, "Settings")
	cfg := domain.ProjectConfig{}
	if project.Config != nil {
		cfg = *project.Config
	}
	settings := SettingsPayload{
		DisplayName: project.Name,
		Config:      cfg,
	}
	if s.cues != nil {
		cues, err := s.cues.SelectCuesByProject(ctx, project.ID)
		if err != nil {
			return nil, fmt.Errorf("list cues: %w", err)
		}
		settings.Cues = cues
	}

	report("packing_state", 15, "Sessions")
	var sessions []SessionIndexEntry
	if s.sessions != nil {
		rows, err := s.sessions.ListSessions(ctx, project.ID)
		if err != nil {
			return nil, fmt.Errorf("list sessions: %w", err)
		}
		for i, row := range rows {
			sessions = append(sessions, SessionIndexEntry{
				ID:          row.ID,
				Num:         i + 1,
				Kind:        row.Kind,
				Harness:     row.Harness,
				Mode:        domain.NormalizeSessionMode(row.Mode),
				DisplayName: row.DisplayName,
				Terminated:  row.IsTerminated,
				CreatedAt:   row.CreatedAt,
				Branch:      row.Metadata.Branch,
			})
		}
	}

	report("packing_state", 25, "Conversations")
	conversations, historyMD, err := s.collectConversations(ctx, project.ID, sessions)
	if err != nil {
		return nil, err
	}

	report("packing_source", 40, "Source tree")
	sourceBuf := &bytes.Buffer{}
	if err := packSourceTree(project.Path, sourceBuf, opts.IncludeSecrets); err != nil {
		return nil, fmt.Errorf("pack source: %w", err)
	}

	report("packing_state", 70, "Attachments")
	attachBuf := &bytes.Buffer{}
	hasAttach, err := packDataSubdir(s.dataDir, "attachments", sessionIDs(sessions), attachBuf)
	if err != nil {
		return nil, fmt.Errorf("pack attachments: %w", err)
	}

	report("packing_state", 80, "Artifacts")
	artBuf := &bytes.Buffer{}
	hasArt, err := packDataSubdir(s.dataDir, "artifacts", sessionIDs(sessions), artBuf)
	if err != nil {
		return nil, fmt.Errorf("pack artifacts: %w", err)
	}

	report("packing_state", 90, "Writing package")
	parts := []string{PartManifest, PartSettings, PartSessions, PartConversations, PartHistoryMD, PartSourceTar}
	if hasAttach {
		parts = append(parts, PartAttachments)
	}
	if hasArt {
		parts = append(parts, PartArtifacts)
	}
	manifest := Manifest{
		SchemaVersion:   SchemaVersion,
		ExportedAt:      s.clock().UTC(),
		SourceProjectID: project.ID,
		DisplayName:     project.Name,
		Kind:            project.Kind,
		IncludeSecrets:  opts.IncludeSecrets,
		Parts:           parts,
		Notes: []string{
			"Harness auth (Codex/Cursor/gh) is not included — sign in on the destination host.",
			"Provider conversation resume IDs are cleared; start new sessions after import.",
		},
	}

	out := &bytes.Buffer{}
	if err := writePackage(out, manifest, settings, sessions, conversations, historyMD, sourceBuf.Bytes(), attachBuf.Bytes(), artBuf.Bytes()); err != nil {
		return nil, err
	}
	report("done", 100, "Export ready")
	return out.Bytes(), nil
}

func sessionIDs(sessions []SessionIndexEntry) []string {
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, string(s.ID))
	}
	return out
}

func (s *Service) collectConversations(
	ctx context.Context,
	projectID domain.ProjectID,
	sessions []SessionIndexEntry,
) ([]ConversationExport, string, error) {
	if s.chats == nil {
		return nil, "", nil
	}
	var exports []ConversationExport
	var md strings.Builder
	md.WriteString("# Imported chat history\n\n")
	md.WriteString("Transferred from another AO host. Provider resume is not available — use this as context.\n\n")

	if rec, ok, err := s.chats.ProjectConversation(ctx, projectID); err != nil {
		return nil, "", err
	} else if ok {
		msgs, acts, err := s.chats.LoadConversationMessages(ctx, rec.ID)
		if err != nil {
			return nil, "", err
		}
		exports = append(exports, ConversationExport{
			Scope: domain.ConversationScopeProject, Messages: msgs, ActivitiesText: acts,
		})
		writeHistorySection(&md, "Project orchestrator", msgs)
	}

	for _, sess := range sessions {
		rec, ok, err := s.chats.ConversationForSession(ctx, sess.ID)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		msgs, acts, err := s.chats.LoadConversationMessages(ctx, rec.ID)
		if err != nil {
			return nil, "", err
		}
		if len(msgs) == 0 {
			continue
		}
		exports = append(exports, ConversationExport{
			Scope: domain.ConversationScopeSession, SessionID: string(sess.ID),
			Messages: msgs, ActivitiesText: acts,
		})
		title := string(sess.ID)
		if sess.DisplayName != "" {
			title = sess.DisplayName
		}
		writeHistorySection(&md, title, msgs)
	}
	return exports, md.String(), nil
}

func writeHistorySection(md *strings.Builder, title string, msgs []ExportedMessage) {
	fmt.Fprintf(md, "## %s\n\n", title)
	for _, m := range msgs {
		role := m.Role
		if role == "" {
			role = "message"
		}
		fmt.Fprintf(md, "**%s:** %s\n\n", role, strings.TrimSpace(m.Text))
	}
}

func (s *Service) applyPackage(
	ctx context.Context,
	r io.Reader,
	opts ImportOptions,
	report func(phase string, percent int, detail string),
) (string, error) {
	report("importing", 15, "Unpacking package")
	parts, err := readPackage(r)
	if err != nil {
		return "", err
	}
	var manifest Manifest
	if err := json.Unmarshal(parts[PartManifest], &manifest); err != nil {
		return "", fmt.Errorf("manifest: %w", err)
	}
	if manifest.SchemaVersion != SchemaVersion {
		return "", fmt.Errorf("unsupported export schema %d", manifest.SchemaVersion)
	}
	var settings SettingsPayload
	if err := json.Unmarshal(parts[PartSettings], &settings); err != nil {
		return "", fmt.Errorf("settings: %w", err)
	}

	report("importing", 35, "Seeding source")
	source := parts[PartSourceTar]
	if len(source) == 0 {
		return "", fmt.Errorf("package missing source.tar.gz")
	}
	name := opts.Name
	if name == nil && settings.DisplayName != "" {
		n := settings.DisplayName
		name = &n
	}
	seeded, err := s.writer.SeedAndRegister(ctx, bytes.NewReader(source), projectsvc.SeedAndRegisterInput{
		DestDir:        opts.DestDir,
		IncludeSecrets: opts.IncludeSecrets || manifest.IncludeSecrets,
		Name:           name,
		ProjectID:      opts.ProjectID,
	})
	if err != nil {
		return "", err
	}

	report("importing", 55, "Applying settings")
	if _, err := s.writer.UpdateSettings(ctx, seeded.Project.ID, projectsvc.UpdateSettingsInput{
		DisplayName: settings.DisplayName,
		Config:      settings.Config,
	}); err != nil {
		return "", fmt.Errorf("apply settings: %w", err)
	}

	report("importing", 65, "Cues")
	if s.cueWrite != nil {
		for _, cue := range settings.Cues {
			cue.ProjectID = seeded.Project.ID
			cue.ID = domain.CueID(fmt.Sprintf("%s-%s", seeded.Project.ID, cue.Name))
			_ = s.cueWrite.InsertCue(ctx, cue) // ignore name conflicts
		}
	}

	report("importing", 75, "Chat history file")
	if hist := parts[PartHistoryMD]; len(hist) > 0 {
		_ = os.WriteFile(filepath.Join(seeded.Path, PartHistoryMD), hist, 0o600)
	}
	if conv := parts[PartConversations]; len(conv) > 0 {
		dir := filepath.Join(seeded.Path, ".ao", "imported-history")
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(filepath.Join(dir, PartConversations), conv, 0o600)
	}
	if sess := parts[PartSessions]; len(sess) > 0 {
		dir := filepath.Join(seeded.Path, ".ao", "imported-history")
		_ = os.MkdirAll(dir, 0o700)
		_ = os.WriteFile(filepath.Join(dir, PartSessions), sess, 0o600)
	}

	report("importing", 85, "Attachments")
	if att := parts[PartAttachments]; len(att) > 0 && s.dataDir != "" {
		_ = unpackDataSubdir(s.dataDir, "attachments", bytes.NewReader(att))
	}
	report("importing", 92, "Artifacts")
	if art := parts[PartArtifacts]; len(art) > 0 && s.dataDir != "" {
		_ = unpackDataSubdir(s.dataDir, "artifacts", bytes.NewReader(art))
	}

	report("done", 100, "Import complete")
	return string(seeded.Project.ID), nil
}
