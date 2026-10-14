package litmus

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type pageTargetsTransport struct {
	t    *testing.T
	body string
}

func (f pageTargetsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path != "/json/list" {
		f.t.Fatalf("unexpected CDP discovery request %s", req.URL)
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(f.body)), Header: http.Header{}}, nil
}

func TestPageURLMatchesPinnedTabTargetID(t *testing.T) {
	for _, tc := range []struct{ name, tabs, targets, want, errorText string }{
		{
			name: "reordered targets and duplicate URLs",
			tabs: `{"success":true,"data":{"tabs":[{"tabId":"t2","targetId":"browser"},{"tabId":"t1","targetId":"app"}]}}`,
			targets: `[{"id":"browser","type":"page","url":"http://app.test/","webSocketDebuggerUrl":"ws://browser"},
{"id":"app","type":"page","url":"http://app.test/","webSocketDebuggerUrl":"ws://app"}]`,
			want: "ws://app",
		},
		{
			name:      "pinned tab gone",
			tabs:      `{"success":true,"data":{"tabs":[{"tabId":"t2","targetId":"browser"}]}}`,
			targets:   `[{"id":"browser","type":"page","webSocketDebuggerUrl":"ws://browser"}]`,
			errorText: "pinned app tab",
		},
		{
			name:      "pinned target gone",
			tabs:      `{"success":true,"data":{"tabs":[{"tabId":"t1","targetId":"app"}]}}`,
			targets:   `[{"id":"browser","type":"page","webSocketDebuggerUrl":"ws://browser"}]`,
			errorText: "pinned app target",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, f, _, _ := driveSvc(t)
			run, _ := svc.store.Get("run_s1")
			e := svc.engineFor(run.ID, run.Legs[0])
			f.out["tab"] = "→ [t1] App - http://app.test/\n  [t2] Browser - http://app.test/\n"
			f.hook = func(args []string) {
				if len(args) == 3 && args[0] == "tab" && args[2] == "--json" {
					f.out["tab"] = tc.tabs
				}
			}
			oldClient := httpClient
			httpClient = &http.Client{Transport: pageTargetsTransport{t, tc.targets}}
			t.Cleanup(func() { httpClient = oldClient })
			got, err := e.pageURL(ctx)
			if tc.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errorText) || got != "" {
					t.Fatalf("missing pinned target returned %q, %v", got, err)
				}
			} else if err != nil || got != tc.want || e.tab != "t1" {
				t.Fatalf("target URL %q, tab %q, error %v; want %q on t1", got, e.tab, err, tc.want)
			}
		})
	}
}
