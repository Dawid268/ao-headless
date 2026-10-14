package litmus

import (
	"strings"
	"testing"
)

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

func TestCheckEvidenceUsesOnlyScreenshotsOnTheChecksLeg(t *testing.T) {
	for _, leg := range []string{"head", "base"} {
		for _, result := range []CheckState{CheckPass, CheckFail, CheckPartial, CheckAbsent, CheckSkipped} {
			c := Check{Leg: leg, Receipts: []Receipt{{Leg: leg, Action: "click", AfterPath: evidenceFile(t)}}}
			if state, note, err := checkEvidence(c, result, "observed"); err != nil || state != result || note != "observed" {
				t.Fatalf("%s %s: state=%s note=%q err=%v", leg, result, state, note, err)
			}
			c.Receipts[0].Leg = map[string]string{"head": "base", "base": "head"}[leg]
			_, _, err := checkEvidence(c, result, "observed")
			if (err != nil) != (result != CheckSkipped) {
				t.Fatalf("%s %s with the other leg's screenshot: %v", leg, result, err)
			}
		}
	}
}
