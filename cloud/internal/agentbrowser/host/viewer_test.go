package host

import (
	"encoding/json"
	"testing"
	"time"
)

// A page can call the shims' binding itself, from any frame and whenever it
// likes: what it puts on the person's clipboard gets through only as a tab's
// write would, from its main frame and just after the person's click or key.
func TestClipboardReports(t *testing.T) {
	v := &viewer{h: &host{}, session: "S", targetID: "T", shimQ: make(chan func(), 1),
		ctxFrame: map[string]string{"S/1": "T", "S/2": "F"}, children: map[string]string{"C": "S"}}
	relayed := func(sid string, ctx int64, payload string) bool {
		params, _ := json.Marshal(map[string]any{"name": "__rbUI", "payload": payload, "executionContextId": ctx})
		v.onEvent(sid, cdpMsg{Method: "Runtime.bindingCalled", Params: params, SessionID: sid})
		select {
		case <-v.shimQ:
			return true
		default:
			return false
		}
	}
	copied := `{"kind":"clipboard","text":"x"}`
	if relayed("S", 1, copied) {
		t.Error("relayed with no click or key from the person")
	}
	v.lastGesture = time.Now()
	for _, c := range []struct {
		name, sid string
		ctx       int64
		payload   string
		want      bool
	}{
		{"main frame", "S", 1, copied, true},
		{"iframe", "S", 2, copied, false},
		{"cross-site iframe", "C", 1, copied, false},
		{"kind hidden from a Go struct", "S", 2, `{"kind":"clipboard","Kind":"tooltip","text":"x"}`, false},
		{"another report from an iframe", "S", 2, `{"kind":"tooltip","text":"x"}`, true},
	} {
		if got := relayed(c.sid, c.ctx, c.payload); got != c.want {
			t.Errorf("%s: relayed %v, want %v", c.name, got, c.want)
		}
	}
	v.lastGesture = time.Now().Add(-activation)
	if relayed("S", 1, copied) {
		t.Error("relayed after the person's click or key had expired")
	}
}
