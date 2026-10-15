package daemon

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/multi"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/githubpat"
)

// githubLoginTimeout bounds the username lookup, so a slow GitHub never holds
// up the status check that asks for it.
const githubLoginTimeout = 5 * time.Second

// githubStatusService is the GitHub token service plus the username AO is
// signed in as. The username comes from the SCM provider, which already
// resolves it from the same sources the observer uses (AO_GITHUB_TOKEN, then
// the gh CLI), so a user signed in through gh has one without storing a token.
type githubStatusService struct {
	*githubpat.Service
	identity ports.ScopedIdentityResolver
}

func newGitHubStatusService(svc *githubpat.Service, provider *multi.Provider) githubStatusService {
	s := githubStatusService{Service: svc}
	if provider != nil {
		s.identity = provider
	}
	return s
}

// Login is best effort: a missing provider or a failed lookup is no username.
func (s githubStatusService) Login(ctx context.Context) string {
	if s.identity == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, githubLoginTimeout)
	defer cancel()
	identity, err := s.identity.AuthenticatedIdentityForProvider(ctx, "github", "")
	if err != nil || !identity.Human {
		return ""
	}
	return identity.Login
}
