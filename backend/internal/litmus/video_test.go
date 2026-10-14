package litmus

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type videoProbeRunner struct {
	duration string
	calls    []string
}

func (r *videoProbeRunner) Run(_ context.Context, _ string, _ []string, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if strings.HasSuffix(name, "ffprobe") {
		return []byte(r.duration + "\n"), nil
	}
	return nil, nil
}

func TestStepCaptionsSayWhatWasDoneInTheReadersWords(t *testing.T) {
	for _, c := range []struct {
		action     string
		args       []string
		role, name string
		want       string
	}{
		{"click", []string{"@e7"}, "button", "Go back", `click "Go back"`},
		{"click", []string{"@e7"}, "button", "", "click a button"},
		{"click", []string{"@e7"}, "link", "  ", "click a link"},
		{"click", []string{"@e7"}, "", "", "click an element"},
		{"fill", []string{"@e9", "hello"}, "textbox", "Message the agent", `type "hello" into "Message the agent"`},
		{"type", []string{"@e9", "hi"}, "textbox", "", `type "hi" into a text field`},
		{"select", []string{"@e3", "dark"}, "combobox", "Theme", `select "dark" in "Theme"`},
		{"press", []string{"Enter"}, "", "", "press Enter"},
		{"press", []string{"Control+K"}, "button", "ignored", "press Control+K"},
		{"click", []string{"@e7"}, "button", `Say "hi"`, `click "Say 'hi'"`},
	} {
		if got := caption(actionRec{Seq: 3, Action: c.action, Args: c.args, TargetRole: c.role, TargetName: c.name}); got != "Step 3: "+c.want {
			t.Errorf("%s %v (%s %q): %q, want Step 3: %s", c.action, c.args, c.role, c.name, got, c.want)
		}
	}
}

func TestParseRefsReadsRoleAndNameFromASnapshot(t *testing.T) {
	snap := "- generic [ref=e1]\n  - button \"Go back\" [ref=e7]\n  - link \"Say \\\"hi\\\"\" [level=2, ref=e8]\n  - button [disabled, ref=e11]\nnot a node ref=e5\n"
	want := map[string]refInfo{"@e1": {"generic", ""}, "@e7": {"button", "Go back"}, "@e8": {"link", `Say "hi"`}, "@e11": {"button", ""}}
	if got := parseRefs(snap); !reflect.DeepEqual(got, want) {
		t.Fatalf("refs %v, want %v", got, want)
	}
}

func whitePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func pixelAt(t *testing.T, path string, x, y int) color.Color {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img.At(x, y)
}

func TestRingFrameMarksTheClickOnACopyOfTheBeforeShot(t *testing.T) {
	dir := t.TempDir()
	is(t, "")(nil, os.MkdirAll(filepath.Join(dir, "receipts"), 0o750))
	before := filepath.Join(dir, "receipts", "before.png")
	whitePNG(t, before, 200, 100)
	// A 100x50 viewport shown in a 200x100 shot: the click at (50, 25) is at pixel (100, 50).
	out := ringFrame(dir, actionRec{Seq: 7, Before: before, Click: &clickAt{X: 50, Y: 25, W: 100, H: 50}})
	if out != filepath.Join(dir, "receipts", "7-click.png") {
		t.Fatalf("frame %q", out)
	}
	magenta, white := color.RGBA{255, 0, 255, 255}, color.RGBA{255, 255, 255, 255}
	for _, c := range []struct {
		x, y int
		want color.RGBA
	}{{124, 50, magenta}, {76, 50, magenta}, {100, 26, magenta}, {100, 74, magenta}, {100, 50, white}, {105, 55, white}, {5, 5, white}} {
		if got := color.RGBAModel.Convert(pixelAt(t, out, c.x, c.y)); got != c.want {
			t.Errorf("pixel (%d,%d) = %v, want %v", c.x, c.y, got, c.want)
		}
	}
	if got := color.RGBAModel.Convert(pixelAt(t, before, 124, 50)); got != white {
		t.Errorf("the original shot was drawn on: %v", got)
	}
	for name, a := range map[string]actionRec{
		"no point":    {Seq: 1, Before: before},
		"no shot":     {Seq: 2, Click: &clickAt{X: 5, Y: 5, W: 100, H: 50}},
		"off screen":  {Seq: 3, Before: before, Click: &clickAt{X: 500, Y: 5, W: 100, H: 50}},
		"no viewport": {Seq: 4, Before: before, Click: &clickAt{X: 5, Y: 5}},
		"not a png":   {Seq: 5, Before: filepath.Join(dir, "missing.png"), Click: &clickAt{X: 5, Y: 5, W: 100, H: 50}},
	} {
		if got := ringFrame(dir, a); got != "" {
			t.Errorf("%s: drew %q", name, got)
		}
	}
}

// The fallback slideshow shows the marked frame before a click's after-shot, and the caption covers both.
func TestSlideshowShowsTheRingBeforeAClick(t *testing.T) {
	svc, _ := newSvc(t)
	fm := &fakeMedia{}
	svc.media = fm
	dir := t.TempDir()
	is(t, "")(nil, os.MkdirAll(filepath.Join(dir, "receipts"), 0o750))
	before, a1, a2 := filepath.Join(dir, "receipts", "head-before.png"), filepath.Join(dir, "receipts", "1-after.png"), filepath.Join(dir, "receipts", "2-after.png")
	for _, p := range []string{before, a1, a2} {
		whitePNG(t, p, 200, 100)
	}
	acts := []actionRec{
		{Seq: 1, Leg: "head", Action: "click", Args: []string{"@e1"}, Before: before, After: a1, TargetRole: "button", TargetName: "Go", Click: &clickAt{X: 50, Y: 25, W: 100, H: 50}},
		{Seq: 2, Leg: "head", Action: "fill", Args: []string{"@e2", "x"}, Before: a1, After: a2}, // not a click: no ring
	}
	if !svc.slideshow(ctx, dir, "head", acts) {
		t.Fatal("no slideshow")
	}
	list, _ := os.ReadFile(filepath.Join(dir, "slides-head.txt"))
	ring := filepath.Join(dir, "receipts", "1-click.png")
	if got := string(list); !strings.HasPrefix(got, "file '"+ring+"'\nduration 1\nfile '"+a1+"'\nduration 1\nfile '"+a2+"'\nduration 1\n") || strings.Contains(got, "2-click.png") {
		t.Fatalf("slide list:\n%s", got)
	}
	srt, _ := os.ReadFile(filepath.Join(dir, "head-slide-captions.srt"))
	if !strings.Contains(string(srt), "00:00:00,000 --> 00:00:02,000") || !strings.Contains(string(srt), "00:00:02,000 --> 00:00:03,000") {
		t.Fatalf("captions:\n%s", srt)
	}
}

func TestRecordingReceiptRestartsIntoTheNextPartAfterTwoUnchangedSizes(t *testing.T) {
	svc, engine, _, runDir := driveSvc(t)
	runner := &videoProbeRunner{duration: "2.0"}
	svc.runner = runner
	path := recordingPath(runDir, "head")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("same-size"), 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	run.Legs[0].RecordStartedAt = svc.clock()
	if err := svc.store.Save(run); err != nil {
		t.Fatal(err)
	}
	resetRecordingState(runDir, "head")
	e := svc.engineFor("run_s1", Leg{Name: "head"})
	svc.recordingReceipt(ctx, "run_s1", "head", e)
	if got := engine.find("record"); got != nil {
		t.Fatalf("restarted after only one unchanged comparison: %v", got)
	}
	svc.recordingReceipt(ctx, "run_s1", "head", e)
	got := engine.find("record")
	if !reflect.DeepEqual(got, []string{"record", "restart", recordingPartPath(runDir, "head", 0), "--fps", recordFPS}) {
		t.Fatalf("record restart = %v", got)
	}
	state := currentRecordingState(runDir, "head")
	if state.active != recordingPartPath(runDir, "head", 0) || len(state.gaps) != 1 {
		t.Fatalf("recording state = %+v", state)
	}
}

func TestStopFailureKeepsTheRecordingAfterProbingIt(t *testing.T) {
	svc, engine, _, runDir := driveSvc(t)
	runner := &videoProbeRunner{duration: "4.0"}
	svc.runner = runner
	path := recordingPath(runDir, "head")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("still playable"), 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	run.Legs[0].RecordStartedAt = svc.clock()
	if err := svc.store.Save(run); err != nil {
		t.Fatal(err)
	}
	engine.fail["record"] = fmt.Errorf("encoder fell behind")
	svc.stopRecording(ctx, run)
	if !fileExists(path) {
		t.Fatal("stop failure removed the recording")
	}
	if len(runner.calls) == 0 || !strings.Contains(runner.calls[0], "ffprobe") {
		t.Fatalf("ffprobe calls = %v", runner.calls)
	}
}

func TestJoinRecordingPartsWritesAConcatListAndKeepsSources(t *testing.T) {
	svc, _ := newSvc(t)
	runner := &videoProbeRunner{duration: "3.5"}
	svc.runner = runner
	svc.media = &fakeMedia{}
	dir := filepath.Join(t.TempDir(), "with'quote")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	base := recordingPath(dir, "head")
	part := recordingPartPath(dir, "head", 0)
	if err := os.MkdirAll(filepath.Dir(base), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{base, part} {
		if err := os.WriteFile(path, []byte("playable"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	joined, err := svc.joinRecordingParts(ctx, dir, "head")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "head", "head-joined.mp4")
	if joined != want || !fileExists(joined) || !fileExists(base) || !fileExists(part) {
		t.Fatalf("joined=%q sources=%t/%t", joined, fileExists(base), fileExists(part))
	}
	list, err := os.ReadFile(concatListPath(dir, "head"))
	if err != nil {
		t.Fatal(err)
	}
	escape := func(path string) string { return strings.ReplaceAll(path, "'", `'\''`) }
	if got, want := string(list), "file '"+escape(base)+"'\nfile '"+escape(part)+"'\n"; got != want {
		t.Fatalf("concat list = %q", got)
	}
}

func TestRecordingPartForcesGapWhenSidecarIsMissingOrCorrupt(t *testing.T) {
	dir := t.TempDir()
	part := recordingPartPath(dir, "head", 0)
	if err := os.MkdirAll(filepath.Dir(part), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, []byte("part"), 0o600); err != nil {
		t.Fatal(err)
	}
	sidecar := recordingStatePath(recordingPath(dir, "head"))
	for _, contents := range []string{"", "{"} {
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if contents != "" {
			if err := os.WriteFile(sidecar, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if !recordingHasGap(dir, "head", time.Time{}, time.Time{}) {
			t.Fatalf("part file did not force a gap for sidecar %q", contents)
		}
	}
}

func TestClipArgsApplyOneSecondBeforeTwoSecondsAfterAndSixtySecondCap(t *testing.T) {
	args := clipArgs("pr.mp4", "clips/c1-pr.mp4", 0, time.Minute)
	if got := strings.Join(args, " "); got != "-y -ss 0.000 -t 60.000 -i pr.mp4 -c copy -movflags +faststart clips/c1-pr.mp4" {
		t.Fatalf("clip args = %q", got)
	}
	if got := strings.Join(clipArgs("pr.mp4", "clips/c1-pr.mp4", time.Second, 90*time.Second), " "); !strings.Contains(got, "-t 60.000") {
		t.Fatalf("clip cap = %q", got)
	}
	if got := checkClipPath("/run", "c/1"); got != filepath.Join("/run", "clips", "c-1-pr.mp4") {
		t.Fatalf("clip path = %q", got)
	}
}

func TestFinishCheckClipsUsesReceiptTimesAndFallsBackOnRecorderGaps(t *testing.T) {
	t.Run("bounded recording clip", func(t *testing.T) {
		svc, media, runDir, _ := submitSvc(t)
		runner := &videoProbeRunner{duration: "120"}
		svc.runner = runner
		run, err := svc.store.Get("run_s1")
		if err != nil {
			t.Fatal(err)
		}
		start := run.CreatedAt
		run.Legs = []Leg{{Name: "head", RecordStartedAt: start, RecordStoppedAt: start.Add(2 * time.Minute)}}
		if _, err := svc.update("run_s1", func(cur *Run) { cur.Legs = run.Legs }); err != nil {
			t.Fatal(err)
		}
		path := recordingPath(runDir, "head")
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("playable"), 0o600); err != nil {
			t.Fatal(err)
		}
		checks, err := svc.store.Checks("run_s1")
		if err != nil {
			t.Fatal(err)
		}
		checks[0].Receipts = append(checks[0].Receipts, Receipt{Seq: 4, Leg: "head", Action: "click", AfterPath: checks[0].Receipts[0].AfterPath, At: start.Add(100 * time.Second)})
		if err := svc.store.SaveChecks("run_s1", checks); err != nil {
			t.Fatal(err)
		}
		acts := []actionRec{{Seq: 1, Leg: "head", Action: "click", After: checks[0].Receipts[0].AfterPath, At: start.Add(10 * time.Second)}, {Seq: 4, Leg: "head", Action: "click", After: checks[0].Receipts[0].AfterPath, At: start.Add(100 * time.Second)}}
		got := svc.finishCheckClips(ctx, runDir, run, acts, path, start)
		if got["c1"] != checkClipPath(runDir, "c1") || !fileExists(got["c1"]) {
			t.Fatalf("clips = %#v", got)
		}
		found := false
		for _, call := range media.calls {
			found = found || strings.Contains(strings.Join(call, " "), "-ss 9.000 -t 60.000 -i "+path)
		}
		if !found {
			t.Fatalf("clip ffmpeg call = %v", media.calls)
		}
		count := 0
		for _, call := range runner.calls {
			if strings.Contains(call, "ffprobe") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("ffprobe calls = %d, want one cached probe: %v", count, runner.calls)
		}
	})

	t.Run("gap uses check slideshow", func(t *testing.T) {
		svc, media, runDir, _ := submitSvc(t)
		runner := &videoProbeRunner{duration: "120"}
		svc.runner = runner
		run, err := svc.store.Get("run_s1")
		if err != nil {
			t.Fatal(err)
		}
		start := run.CreatedAt
		run.Legs = []Leg{{Name: "head", RecordStartedAt: start, RecordStoppedAt: start.Add(2 * time.Minute)}}
		path := recordingPath(runDir, "head")
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("playable"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.update("run_s1", func(cur *Run) { cur.Legs = run.Legs }); err != nil {
			t.Fatal(err)
		}
		checks, err := svc.store.Checks("run_s1")
		if err != nil {
			t.Fatal(err)
		}
		gap := start.Add(15 * time.Second) // after this check; joined parts still cannot prove its offset
		saveRecordingState(runDir, "head", recordingState{active: path, gaps: []time.Time{gap}})
		acts := []actionRec{{Seq: 1, Leg: "head", Action: "click", After: checks[0].Receipts[0].AfterPath, At: start.Add(10 * time.Second)}}
		got := svc.finishCheckClips(ctx, runDir, run, acts, path, start)
		if got["c1"] != checkClipPath(runDir, "c1") {
			t.Fatalf("gap slideshow missing: %#v", got)
		}
		if slices.ContainsFunc(media.calls, func(call []string) bool { return strings.Contains(strings.Join(call, " "), "-ss ") }) || len(media.calls) == 0 || !strings.Contains(strings.Join(media.calls[len(media.calls)-1], " "), "-f concat") {
			t.Fatalf("gap slideshow call = %v", media.calls)
		}
	})

	t.Run("short recording uses check slideshow", func(t *testing.T) {
		svc, media, runDir, _ := submitSvc(t)
		svc.runner = &videoProbeRunner{duration: "3"}
		run, err := svc.store.Get("run_s1")
		if err != nil {
			t.Fatal(err)
		}
		start := run.CreatedAt
		run.Legs = []Leg{{Name: "head", RecordStartedAt: start, RecordStoppedAt: start.Add(time.Minute)}}
		path := recordingPath(runDir, "head")
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("playable"), 0o600); err != nil {
			t.Fatal(err)
		}
		acts := []actionRec{{Seq: 1, Leg: "head", Action: "click", After: filepath.Join(runDir, "receipts", "1-after.png"), At: start.Add(10 * time.Second)}}
		got := svc.finishCheckClips(ctx, runDir, run, acts, path, start)
		if got["c1"] != checkClipPath(runDir, "c1") || len(media.calls) == 0 {
			t.Fatalf("short recording clip = %#v calls=%v", got, media.calls)
		}
		if slices.ContainsFunc(media.calls, func(call []string) bool { return strings.Contains(strings.Join(call, " "), "-ss ") }) {
			t.Fatalf("short recording used a cut: %v", media.calls)
		}
	})
}

func TestCheckSlideshowClipIsCappedAtSixtySeconds(t *testing.T) {
	svc, media, runDir, _ := submitSvc(t)
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	checks, err := svc.store.Checks("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	start := run.CreatedAt
	var acts []actionRec
	for i := 1; i <= 61; i++ {
		path := filepath.Join(runDir, "receipts", fmt.Sprintf("cap-%d-after.png", i))
		if err := os.WriteFile(path, []byte("png"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := Receipt{Seq: i, Leg: "head", Action: "click", AfterPath: path, At: start.Add(time.Duration(i) * time.Second)}
		if i > 1 {
			checks[0].Receipts = append(checks[0].Receipts, r)
		}
		acts = append(acts, actionRec{Seq: i, Leg: "head", Action: "click", After: path, At: r.At})
	}
	if err := svc.store.SaveChecks("run_s1", checks); err != nil {
		t.Fatal(err)
	}
	got := svc.finishCheckClips(ctx, runDir, run, acts, "", start)
	if got["c1"] != checkClipPath(runDir, "c1") || len(media.calls) == 0 {
		t.Fatalf("clips=%#v calls=%v", got, media.calls)
	}
	list, err := os.ReadFile(filepath.Join(runDir, "slides-check-c1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(list), "duration 1\n"); count != 60 {
		t.Fatalf("slideshow has %d seconds, want 60: %s", count, list)
	}
}
