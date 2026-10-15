package projectxfer

import (
	"context"
	"io"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
)

// ProjectServiceWriter adapts project.Manager to ProjectWriter.
type ProjectServiceWriter struct {
	Mgr projectsvc.Manager
}

func (w ProjectServiceWriter) SeedAndRegister(ctx context.Context, archive io.Reader, in projectsvc.SeedAndRegisterInput) (projectsvc.SeedAndRegisterResult, error) {
	return w.Mgr.SeedAndRegister(ctx, archive, in)
}

func (w ProjectServiceWriter) UpdateSettings(ctx context.Context, id domain.ProjectID, in projectsvc.UpdateSettingsInput) (projectsvc.Project, error) {
	return w.Mgr.UpdateSettings(ctx, id, in)
}
