package provideraccounts

import (
	"errors"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func (h *harness) launch(session string, agent domain.AgentHarness, related bool) map[string]string {
	h.t.Helper()
	env, err := h.svc.LaunchAccountEnv(h.ctx, domain.SessionID(session), agent, related)
	if err != nil {
		h.t.Fatalf("launch %s on %s: %v", session, agent, err)
	}
	return env
}

// A switch moves the session's route to the target's provider, onto its account
// there, and a switch back finds the account it had before.
func TestASwitchMovesTheRouteAndASwitchBackRestoresTheAccount(t *testing.T) {
	h := setup(t)
	h.signIn("claude", "max@example.com")
	work := h.signIn("claude", "work@example.com")
	codex := h.signIn("codex", "pro@example.com")
	h.assign("s1", domain.HarnessClaudeCode, work)
	claudeEnv := h.launch("s1", domain.HarnessClaudeCode, false)
	planned, err := h.svc.PlannedAccountEnv(h.ctx, "s1", domain.HarnessCodex)
	if err != nil || planned["AO_PROXY_TICKET"] != claudeEnv["ANTHROPIC_AUTH_TOKEN"] {
		t.Fatalf("planned=%v err=%v", planned, err)
	}
	h.route("s1", work) // planning moved nothing
	if env := h.launch("s1", domain.HarnessCodex, false); !reflect.DeepEqual(env, planned) {
		t.Fatalf("the target got %v, planned %v", env, planned)
	}
	h.route("s1", codex)
	if route, _, _ := h.svc.SessionAccount(h.ctx, "s1"); route.Provider != "codex" {
		t.Fatalf("route=%+v", route)
	}
	if env := h.launch("s1", domain.HarnessClaudeCode, false); !reflect.DeepEqual(env, claudeEnv) {
		t.Fatalf("switch back env=%v", env)
	}
	h.route("s1", work)
	// The account page lists the session once under each account it has a route on.
	views, _ := h.svc.Accounts(h.ctx, false, false)
	for _, view := range views {
		if want := view.ID == work || view.ID == codex; want != reflect.DeepEqual(view.Sessions, []string{"s1"}) {
			t.Fatalf("account %s lists %v", view.ID, view.Sessions)
		}
	}
	if err = h.svc.ForgetAccount(h.ctx, "s1"); err != nil || len(h.store.get().Routes) != 0 {
		t.Fatalf("a deleted session kept routes: %+v err=%v", h.store.get().Routes, err)
	}
}

// A switch to a provider AO holds no account for leaves the agent its own setup,
// and keeps the session's account for a switch back.
func TestASwitchToAProviderWithoutAccountsKeepsTheAgentsOwnSetup(t *testing.T) {
	h := setup(t)
	work := h.signIn("claude", "work@example.com")
	h.assign("s1", domain.HarnessClaudeCode, work)
	if env := h.launch("s1", domain.HarnessCodex, false); env != nil {
		t.Fatalf("env=%v", env)
	}
	if _, managed, _ := h.svc.SessionAccount(h.ctx, "s1"); managed {
		t.Fatal("the session still has a route")
	}
	h.launch("s1", domain.HarnessClaudeCode, false)
	h.route("s1", work)
}

// A reviewer on another provider runs on the worker's account for that
// provider, which starts as its default; the worker's own route is left alone.
func TestAReviewerOnAnotherProviderGetsAnAccountOfItsOwn(t *testing.T) {
	h := setup(t)
	work := h.signIn("claude", "work@example.com")
	h.assign("w1", domain.HarnessClaudeCode, work)
	worker := h.launch("w1", domain.HarnessClaudeCode, false)
	if env := h.launch("w1", domain.HarnessCodex, true); env != nil {
		t.Fatalf("no Codex account yet, env=%v", env)
	}
	h.signIn("codex", "pro@example.com")
	reviewer := h.launch("w1", domain.HarnessCodex, true)
	if reviewer["AO_PROXY_TICKET"] == "" || reviewer["AO_PROXY_TICKET"] == worker["ANTHROPIC_AUTH_TOKEN"] {
		t.Fatalf("reviewer env=%v", reviewer)
	}
	if same := h.launch("w1", domain.HarnessClaudeCode, true); !reflect.DeepEqual(same, worker) {
		t.Fatalf("a reviewer on the worker's provider got %v", same)
	}
	h.route("w1", work)
	// A related launch waiting for a sign-in is refused rather than run on this computer's login.
	h.assign("w2", domain.HarnessClaudeCode, work)
	if err := h.act(work, "sign-out"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.LaunchAccountEnv(h.ctx, "w2", domain.HarnessClaudeCode, true); !errors.Is(err, ports.ErrProviderLoginRequired) {
		t.Fatalf("err=%v", err)
	}
}
