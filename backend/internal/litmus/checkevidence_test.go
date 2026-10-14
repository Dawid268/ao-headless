package litmus

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// endWith runs CheckEnd on a running check of the given leg that holds receipts, with a service that has no other state.
func endWith(t *testing.T, leg string, receipts []Receipt, result CheckState, note string) (Check, error) {
	t.Helper()
	svc, now := newSvc(t)
	receipts = append([]Receipt(nil), receipts...)
	for i := range receipts {
		if receipts[i].Action == "test" {
			receipts[i].OutputPath = evidenceFile(t)
		} else if receipts[i].AfterPath != "" {
			receipts[i].AfterPath = evidenceFile(t)
		}
	}
	is(t, "")(nil, svc.store.Save(Run{ID: "run_s1", SessionID: "s1", State: RunRunning, Deadline: now.Add(RunBudget)}))
	is(t, "")(nil, svc.store.SaveChecks("run_s1", []Check{{ID: "c1", Name: "x", Expect: "y", State: CheckRunning, Leg: leg, Receipts: receipts}}))
	return svc.CheckEnd(ctx, "s1", "c1", result, note, false)
}

// ranN is a runner's count of tests, as a receipt holds it (nil: the runner prints no counts).
func ranN(n int) *int { return &n }

func TestCheckEndSameFaultIsBaseOnly(t *testing.T) {
	for _, leg := range []string{"", "head", "base"} {
		for _, result := range []CheckState{CheckFail, CheckPartial} {
			svc, now := newSvc(t)
			is(t, "")(nil, svc.store.Save(Run{ID: "run_s1", SessionID: "s1", State: RunRunning, Deadline: now.Add(RunBudget)}))
			c := Check{ID: "c1", Name: "menu", State: CheckRunning, Leg: leg, Receipts: []Receipt{{Leg: leg, Action: "click", AfterPath: evidenceFile(t)}}}
			is(t, "")(nil, svc.store.SaveChecks("run_s1", []Check{c}))
			got, err := svc.CheckEnd(ctx, "s1", c.ID, result, "same broken row", true)
			if leg != "base" {
				is(t, "TESTING_CHECK_SAME_FAULT")(got, err)
			} else if err != nil || !got.SameFault || got.State != result {
				t.Fatalf("base %s: %+v, %v", result, got, err)
			}
			saved, err := svc.store.Checks("run_s1")
			is(t, "")(nil, err)
			if saved[0].SameFault != (leg == "base") || (leg != "base" && saved[0].State != CheckRunning) {
				t.Fatalf("%s %s: saved %+v", leg, result, saved[0])
			}
		}
	}
}

func TestCheckEvidenceNeedsSavedFiles(t *testing.T) {
	zero := 0
	for _, c := range []struct {
		name   string
		r      Receipt
		result CheckState
		want   string
	}{
		{"blank shot", Receipt{Action: "click"}, CheckPass, "no picture was saved; act again or end it skipped"},
		{"missing shot", Receipt{Action: "click", AfterPath: "missing.png"}, CheckFail, "no picture was saved; act again or end it skipped"},
		{"partial with missing shot", Receipt{Action: "click"}, CheckPartial, "no picture was saved; act again or end it skipped"},
		{"absent with missing shot", Receipt{Action: "click"}, CheckAbsent, "no picture was saved; act again or end it skipped"},
		{"blank test output", Receipt{Action: "test", Exit: &zero}, CheckPass, "no test output was saved"},
		{"missing test output", Receipt{Action: "test", OutputPath: "missing.txt", Exit: &zero}, CheckPass, "no test output was saved"},
		{"directory is not a picture", Receipt{Action: "click", AfterPath: t.TempDir()}, CheckFail, "no picture was saved"},
	} {
		_, _, err := checkEvidence(Check{Receipts: []Receipt{c.r}}, c.result, "")
		is(t, "TESTING_CHECK_EVIDENCE")(nil, err)
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestCheckEndSameFaultRequiresFailedOrPartialBase(t *testing.T) {
	for _, result := range []CheckState{CheckPass, CheckAbsent, CheckSkipped} {
		svc, now := newSvc(t)
		is(t, "")(nil, svc.store.Save(Run{ID: "run_s1", SessionID: "s1", State: RunRunning, Deadline: now.Add(RunBudget)}))
		c := Check{ID: "c1", Name: "account", Leg: "base", State: CheckRunning, Receipts: []Receipt{{Leg: "base", Action: "click", AfterPath: evidenceFile(t)}}}
		is(t, "")(nil, svc.store.SaveChecks("run_s1", []Check{c}))
		is(t, "TESTING_CHECK_SAME_FAULT")(svc.CheckEnd(ctx, "s1", "c1", result, "same", true))
		saved, _ := svc.store.Checks("run_s1")
		if saved[0].State != CheckRunning || saved[0].SameFault {
			t.Fatalf("refused result persisted: %+v", saved[0])
		}
	}
}

func TestCheckEndJudgesTestReceiptsByWhatRan(t *testing.T) {
	code := func(n int) *int { return &n }
	test := func(seq int, r Receipt) Receipt { r.Seq, r.Action = seq, "test"; return r }
	passed := test(8, Receipt{Exit: code(0), Ran: ranN(14)})
	failed := test(3, Receipt{Exit: code(1), Ran: ranN(2)})
	unbuilt := test(5, Receipt{Exit: code(1), NotBuilt: true})
	empty := test(7, Receipt{Exit: code(0), Ran: ranN(0)})
	uncounted := test(10, Receipt{Exit: code(0)}) // go vet, mocha: no counts, so the exit code is the verdict
	missing := test(6, Receipt{Exit: code(1)})    // npm: Missing script
	timeout := test(11, Receipt{Exit: code(-1), Ran: ranN(2), Failing: true})
	noCommand := test(12, Receipt{Exit: code(127)})
	shot := Receipt{Seq: 9, Action: "click", AfterPath: "9-after.png"}
	for _, c := range []struct {
		name     string
		receipts []Receipt
		result   CheckState
		refused  string // the message must contain it; "" means accepted
		state    CheckState
		note     string
	}{
		{"fail on a run that exited 0", []Receipt{passed}, CheckFail, "receipt 8 exited 0 with 14 passing; a fail needs a test that ran and failed", "", ""},
		{"fail on a run that exited 0 and counted nothing", []Receipt{empty}, CheckFail, "receipt 7 exited 0; a fail needs", "", ""},
		{"fail on a run with no counts that exited 0", []Receipt{uncounted}, CheckFail, "receipt 10 exited 0; a fail needs", "", ""},
		{"fail on exit 0 beside a build failure", []Receipt{unbuilt, passed}, CheckFail, "receipt 8 exited 0", "", ""},
		{"fail on a compile failure is a skip", []Receipt{unbuilt}, CheckFail, "", CheckSkipped, notBuiltNote},
		{"fail on two compile failures is a skip", []Receipt{unbuilt, test(6, Receipt{Exit: code(2), NotBuilt: true})}, CheckFail, "", CheckSkipped, notBuiltNote},
		{"fail on a test that ran and failed", []Receipt{passed, failed}, CheckFail, "", CheckFail, "boom"},
		{"fail on a test receipt from before exit codes", []Receipt{{Seq: 2, Action: "test"}}, CheckFail, "", CheckFail, "boom"},
		{"fail backed by a screenshot beside a run that exited 0", []Receipt{passed, shot}, CheckFail, "receipt 8 exited 0 with 14 passing", "", ""},
		{"fail on a screenshot", []Receipt{shot}, CheckFail, "", CheckFail, "boom"},
		{"fail on a timeout", []Receipt{timeout}, CheckFail, "the test timed out or the command was missing; raise --minutes or end it skipped", "", ""},
		{"fail on a missing command beside a screenshot", []Receipt{noCommand, shot}, CheckFail, "the test timed out or the command was missing", "", ""},
		{"fail after retrying a timeout", []Receipt{timeout, failed}, CheckFail, "", CheckFail, "boom"},
		{"pass on a run that exited 1", []Receipt{missing}, CheckPass, "receipt 6 exited 1; a pass needs a test that ran and passed", "", ""},
		{"pass on a compile failure", []Receipt{unbuilt}, CheckPass, "receipt 5 did not compile; a pass needs", "", ""},
		{"pass on a run that counted no tests", []Receipt{empty}, CheckPass, "receipt 7 ran 0 tests; a pass needs", "", ""},
		{"pass on a run with no counts that exited 0", []Receipt{uncounted}, CheckPass, "", CheckPass, "boom"},
		{"pass on a failure beside nothing that passed", []Receipt{failed, unbuilt, empty}, CheckPass, "receipt 3 exited 1", "", ""},
		{"pass on a run that passed", []Receipt{passed}, CheckPass, "", CheckPass, "boom"},
		{"pass on a run that passed beside one that failed", []Receipt{failed, passed}, CheckPass, "", CheckPass, "boom"},
		{"pass after retrying a timeout", []Receipt{timeout, passed}, CheckPass, "", CheckPass, "boom"},
		{"pass on a screenshot beside a failed run", []Receipt{missing, shot}, CheckPass, "receipt 6 exited 1; a pass needs", "", ""},
		{"pass on a test receipt from before exit codes", []Receipt{{Seq: 2, Action: "test"}}, CheckPass, "", CheckPass, "boom"},
		{"partial is the tester's word", []Receipt{passed}, CheckPartial, "", CheckPartial, "boom"},
		{"skipped needs no receipt", nil, CheckSkipped, "", CheckSkipped, "boom"},
		{"absent on a screenshot", []Receipt{shot}, CheckAbsent, "", CheckAbsent, "boom"},
		{"absent on a screenshot beside test runs", []Receipt{passed, failed, shot}, CheckAbsent, "", CheckAbsent, "boom"},
		{"absent on a test run that passed", []Receipt{passed}, CheckAbsent, "absent needs a screenshot of where the feature would be", "", ""},
		{"absent on a test run that failed", []Receipt{failed}, CheckAbsent, "test receipts cannot back it", "", ""},
		{"absent on a test receipt from before exit codes", []Receipt{{Seq: 2, Action: "test"}}, CheckAbsent, "absent needs a screenshot", "", ""},
	} {
		got, err := endWith(t, "", c.receipts, c.result, "boom")
		switch {
		case c.refused != "":
			if err == nil || !strings.Contains(err.Error(), c.refused) {
				t.Errorf("%s: err = %v, want it to contain %q", c.name, err, c.refused)
			}
		case err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case got.State != c.state || got.Note != c.note:
			t.Errorf("%s: stored %s %q, want %s %q", c.name, got.State, got.Note, c.state, c.note)
		}
	}
	// A pass without any receipt is still refused for the missing receipt, not the evidence.
	if _, err := endWith(t, "", nil, CheckPass, ""); err == nil || !strings.Contains(err.Error(), "has no receipt") {
		t.Errorf("a pass with no receipt: %v", err)
	}
	// Absent is accepted on the base leg too, and still needs a receipt of the check's own leg.
	if got, err := endWith(t, "base", []Receipt{{Seq: 1, Leg: "base", Action: "click", AfterPath: "1-after.png"}}, CheckAbsent, "no picker"); err != nil || got.State != CheckAbsent {
		t.Errorf("absent on the base: %v %v", got.State, err)
	}
	if _, err := endWith(t, "base", []Receipt{shot}, CheckAbsent, ""); err == nil || !strings.Contains(err.Error(), "has no receipt on the base leg") {
		t.Errorf("an absent base check leaning on a head screenshot: %v", err)
	}
	if _, err := endWith(t, "", nil, CheckAbsent, ""); err == nil || !strings.Contains(err.Error(), "has no receipt") {
		t.Errorf("an absent with no receipt: %v", err)
	}
	// Only the receipts of the check's own leg are judged: a base check whose own run exited 0 cannot lean on a head screenshot.
	if _, err := endWith(t, "base", []Receipt{shot, {Seq: 4, Leg: "base", Action: "test", Exit: code(0), Ran: ranN(2)}}, CheckFail, ""); err == nil || !strings.Contains(err.Error(), "receipt 4 exited 0 with 2 passing") {
		t.Errorf("a base fail leaning on a head screenshot: %v", err)
	}
}

// On the base a fail needs a test that ran: a timeout, a missing command and a run that counted nothing do not show main failing.
func TestCheckEndBaseFailNeedsATestThatRan(t *testing.T) {
	code := func(n int) *int { return &n }
	test := func(seq int, r Receipt) Receipt { r.Seq, r.Leg, r.Action = seq, "base", "test"; return r }
	for name, r := range map[string]Receipt{
		"timeout":        test(1, Receipt{Exit: code(-1), Ran: ranN(2)}),
		"exit 127":       test(1, Receipt{Exit: code(127)}),
		"build failure":  test(1, Receipt{Exit: code(1), Ran: ranN(0)}),
		"vet, no counts": test(1, Receipt{Exit: code(1)}),
	} {
		want := "a fail needs a test that ran and failed"
		if *r.Exit == -1 || *r.Exit == 127 {
			want = "the test timed out or the command was missing; raise --minutes or end it skipped"
		}
		if _, err := endWith(t, "base", []Receipt{r}, CheckFail, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, r := range map[string]Receipt{
		"counted":                 test(1, Receipt{Exit: code(1), Ran: ranN(3)}),
		"no counts, names a test": test(1, Receipt{Exit: code(1), Failing: true}),
	} {
		if got, err := endWith(t, "base", []Receipt{r}, CheckFail, "boom"); err != nil || got.State != CheckFail {
			t.Errorf("%s: %v %v", name, got.State, err)
		}
	}
}

func TestReceiptJSONKeepsOldReceiptsReadable(t *testing.T) {
	var old Receipt // written before exit codes
	if err := json.Unmarshal([]byte(`{"seq":4,"action":"test","outputPath":"receipts/4-test.txt","at":"2026-10-11T09:00:00Z"}`), &old); err != nil || old.Exit != nil || old.NotBuilt || old.Ran != nil {
		t.Fatalf("old receipt: %+v err %v", old, err)
	}
	zero := 0
	out, err := json.Marshal(Receipt{Seq: 5, Action: "test", Exit: &zero, At: time.Unix(0, 0).UTC()})
	if err != nil || !strings.Contains(string(out), `"exit":0`) || strings.Contains(string(out), "notBuilt") || strings.Contains(string(out), `"ran"`) {
		t.Fatalf("a clean run keeps its exit code and nothing else: %s err %v", out, err)
	}
	if out, _ := json.Marshal(Receipt{Seq: 6, Action: "click"}); strings.Contains(string(out), "exit") || strings.Contains(string(out), "ran") {
		t.Fatalf("a screenshot receipt carries no test fields: %s", out)
	}
}

// A base fail counts toward the verdict only when a test ran and failed: not a timeout, a missing command, a build
// failure or a run with no counts and no failing test named; a run that exited 0 never does.
func TestRanAndFailedNeedsATestThatRan(t *testing.T) {
	code := func(n int) *int { return &n }
	for name, c := range map[string]struct {
		r    Receipt
		want bool
	}{
		"counted and failed":          {Receipt{Exit: code(1), Ran: ranN(4)}, true},
		"no counts, names a test":     {Receipt{Exit: code(1), Failing: true}, true},
		"no counts, names nothing":    {Receipt{Exit: code(1)}, false},
		"counted zero":                {Receipt{Exit: code(1), Ran: ranN(0)}, false},
		"timed out":                   {Receipt{Exit: code(-1), Ran: ranN(4), Failing: true}, false},
		"command not found":           {Receipt{Exit: code(127), Failing: true}, false},
		"did not compile":             {Receipt{Exit: code(1), Ran: ranN(4), NotBuilt: true}, false},
		"exited 0 with a failed name": {Receipt{Exit: code(0), Ran: ranN(4), Failing: true}, false},
	} {
		c.r.Leg, c.r.Action, c.r.OutputPath = "base", "test", evidenceFile(t)
		if got := ranAndFailed(Check{State: CheckFail, Leg: "base", Receipts: []Receipt{c.r}}); got != c.want {
			t.Errorf("%s: ranAndFailed = %v, want %v", name, got, c.want)
		}
	}
}

func TestRanAndFailedJudgesTestsBesidePictures(t *testing.T) {
	shot := Receipt{Action: "click", Leg: "base", AfterPath: evidenceFile(t)}
	for _, code := range []int{0, 1, -1, 127} {
		r := Receipt{Action: "test", Leg: "base", Exit: &code, Ran: ranN(2), Failing: true, OutputPath: evidenceFile(t)}
		c := Check{State: CheckFail, Leg: "base", Receipts: []Receipt{shot, r}}
		if got := ranAndFailed(c); got != (code == 1) {
			t.Errorf("exit %d beside picture: got %t", code, got)
		}
	}
	if ranAndFailed(Check{State: CheckFail, Leg: "base", Receipts: []Receipt{{Action: "test", Leg: "base", Exit: ranN(1), Ran: ranN(2)}}}) {
		t.Error("a failed test with no saved output shows a difference")
	}
}
