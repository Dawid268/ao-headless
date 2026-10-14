package httpd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/litmus"
)

type testingTimeoutProbe struct {
	controllers.TestingService
	test, cleanup, replay chan time.Duration
}

func (p *testingTimeoutProbe) Test(ctx context.Context, _, _, _ string, _ []string, _ string, _ int) (litmus.TestResult, error) {
	p.test <- remainingRequestBudget(ctx)
	return litmus.TestResult{}, nil
}

// RefuseTester lets the probe's session through so the test route reaches Test and its budget can be read.
func (p *testingTimeoutProbe) RefuseTester(string, string) error { return nil }

func (p *testingTimeoutProbe) Cleanup(ctx context.Context, _ string) (litmus.Run, error) {
	p.cleanup <- remainingRequestBudget(ctx)
	return litmus.Run{}, nil
}

func (p *testingTimeoutProbe) Replay(ctx context.Context, _, _, _ string) (map[string]any, error) {
	p.replay <- remainingRequestBudget(ctx)
	return map[string]any{}, nil
}

func TestTestingTestRouteUsesLongTimeoutAndOthersKeepTheOrdinaryOne(t *testing.T) {
	const configuredTimeout = 250 * time.Millisecond
	probe := &testingTimeoutProbe{test: make(chan time.Duration, 1), cleanup: make(chan time.Duration, 1), replay: make(chan time.Duration, 1)}
	router := NewRouterWithControl(config.Config{RequestTimeout: configuredTimeout}, discardLogger(), nil, APIDeps{Testing: probe}, ControlDeps{})

	for path, budgets := range map[string]chan time.Duration{"/api/v1/testing/test": probe.test, "/api/v1/testing/cleanup": probe.cleanup, "/api/v1/testing/checks/c1/replay": probe.replay} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"sessionId":"s1"}`))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("POST %s = %d: %s", path, res.Code, res.Body.String())
		}
		budget := <-budgets
		if path == "/api/v1/testing/checks/c1/replay" {
			if budget < litmus.RunBudget+time.Minute-10*time.Second || budget > litmus.RunBudget+time.Minute {
				t.Fatalf("replay budget = %s, want 26 minutes", budget)
			}
			continue
		}
		if path == "/api/v1/testing/test" {
			if budget < 11*time.Minute-10*time.Second || budget > 11*time.Minute {
				t.Fatalf("test verb budget = %s, want at least 11 minutes", budget)
			}
			continue
		}
		if budget <= 0 || budget > configuredTimeout {
			t.Fatalf("ordinary POST budget = %s, want at most %s", budget, configuredTimeout)
		}
	}
}
