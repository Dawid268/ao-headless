package litmus

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const (
	homebrewFFmpeg  = "/opt/homebrew/bin/ffmpeg"
	homebrewFFprobe = "/opt/homebrew/bin/ffprobe"
	recordTimeout   = 10 * time.Second // `record start` hangs when the window is not frontmost; never block a run on video
	stopTimeout     = 30 * time.Second
	videoTimeout    = 2 * time.Minute
	recordFPS       = "5" // a lower capture rate keeps the encoder from falling behind on a loaded Mac (it dropped a 3-minute recording at 12, and a 2 minute one at 10)
	noVideo         = "none (no recording and no after-shots to build a slideshow from)"
)

// Media runs ffmpeg with the given arguments. Tests substitute a fake, so no unit test encodes video.
type Media interface {
	Run(ctx context.Context, args ...string) error
}

type execMedia struct{}

func (execMedia) Run(ctx context.Context, args ...string) error {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		bin = homebrewFFmpeg // the daemon's PATH may lack Homebrew
	}
	cmd := process.CommandContext(ctx, bin, append([]string{"-hide_banner", "-loglevel", "error"}, args...)...)
	errOut := &capWriter{max: 64 << 10}
	cmd.Stderr, cmd.WaitDelay = errOut, 2*time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, lastLines(errOut.buf, 5))
	}
	return nil
}

// recordingState is deliberately kept outside run.json. It is recorder health
// metadata, not verdict evidence; its small sidecar survives a daemon restart
// so the active part and known gaps remain identifiable.
type recordingState struct {
	active    string
	lastSize  int64
	observed  bool
	unchanged int
	nextPart  int
	gaps      []time.Time
}

var recordingStates = struct {
	sync.Mutex
	m map[string]recordingState
}{m: map[string]recordingState{}}

type playableCacheEntry struct {
	size     int64
	modified time.Time
	duration float64
}

var playableCache = struct {
	sync.Mutex
	m map[string]playableCacheEntry
}{m: map[string]playableCacheEntry{}}

// recordingPath is where a leg's `record start` writes; cutPath is the captioned video built for the leg.
func recordingPath(runDir, leg string) string { return filepath.Join(runDir, leg, leg+".mp4") }
func cutPath(runDir, leg string) string       { return filepath.Join(runDir, leg+"-cut.mp4") }

func recordingPartPath(runDir, leg string, n int) string {
	return filepath.Join(runDir, leg, fmt.Sprintf("%s-part-%04d.mp4", leg, n))
}

func checkClipPath(runDir, checkID string) string {
	checkID = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, checkID)
	checkID = strings.Trim(checkID, "-")
	if checkID == "" {
		checkID = "check"
	}
	return filepath.Join(runDir, "clips", checkID+"-pr.mp4")
}

func recordingStateKey(runDir, leg string) string { return recordingPath(runDir, leg) }

func recordingStatePath(key string) string {
	return filepath.Join(filepath.Dir(key), ".recording-state.json")
}

type recordingStateDisk struct {
	Active    string      `json:"active"`
	LastSize  int64       `json:"lastSize"`
	Observed  bool        `json:"observed"`
	Unchanged int         `json:"unchanged"`
	NextPart  int         `json:"nextPart"`
	Gaps      []time.Time `json:"gaps,omitempty"`
}

func writeRecordingState(key string, state recordingState) {
	disk := recordingStateDisk{Active: state.active, LastSize: state.lastSize, Observed: state.observed, Unchanged: state.unchanged, NextPart: state.nextPart, Gaps: state.gaps}
	b, err := json.Marshal(disk)
	if err == nil && os.MkdirAll(filepath.Dir(key), 0o750) == nil {
		_ = writeAtomic(recordingStatePath(key), b)
	}
}

func readRecordingState(key string) (recordingState, bool) {
	b, err := os.ReadFile(recordingStatePath(key))
	if err != nil {
		return recordingState{}, false
	}
	var disk recordingStateDisk
	if json.Unmarshal(b, &disk) != nil || disk.Active == "" {
		return recordingState{}, false
	}
	return recordingState{active: disk.Active, lastSize: disk.LastSize, observed: disk.Observed, unchanged: disk.Unchanged, nextPart: disk.NextPart, gaps: disk.Gaps}, true
}

func resetRecordingState(runDir, leg string) {
	key := recordingStateKey(runDir, leg)
	recordingStates.Lock()
	recordingStates.m[key] = recordingState{active: key}
	recordingStates.Unlock()
	writeRecordingState(key, recordingState{active: key})
}

func currentRecordingState(runDir, leg string) recordingState {
	key := recordingStateKey(runDir, leg)
	recordingStates.Lock()
	defer recordingStates.Unlock()
	state := recordingStates.m[key]
	if state.active == "" {
		if disk, ok := readRecordingState(key); ok {
			state = disk
		} else {
			state.active = key
		}
	}
	return state
}

func saveRecordingState(runDir, leg string, state recordingState) {
	key := recordingStateKey(runDir, leg)
	recordingStates.Lock()
	recordingStates.m[key] = state
	recordingStates.Unlock()
	writeRecordingState(key, state)
}

func forgetRecordingState(runDir, leg string) {
	key := recordingStateKey(runDir, leg)
	recordingStates.Lock()
	delete(recordingStates.m, key)
	recordingStates.Unlock()
	playableCache.Lock()
	for path := range playableCache.m {
		if path == key || filepath.Dir(path) == filepath.Dir(key) {
			delete(playableCache.m, path)
		}
	}
	playableCache.Unlock()
}

// startRecording asks the leg's own engine to record its window to <runDir>/<leg>/<leg>.mp4, so each video follows the
// window it belongs to, with the pointer overlay (--cursor) so receipts show where a click landed. If the engine refuses
// the overlay, it restarts without it and says so in <leg>/engine.log. It returns the leg with RecordStartedAt (taken
// just before the `record start` that took effect) and RecordCursor set; a failure or timeout is logged, never fatal.
func (s *Service) startRecording(ctx context.Context, runID string, leg Leg) Leg {
	e := s.engineFor(runID, leg)
	e.mu.Lock()
	defer e.mu.Unlock()
	path := recordingPath(filepath.Join(s.store.root, runID), leg.Name)
	began := s.clock()
	_, err := e.Run(ctx, recordTimeout, "record", "start", path, "--cursor", "--fps", recordFPS)
	leg.RecordCursor, leg.RecordFPS = err == nil, recordFPS
	if err != nil {
		s.engineLog(runID, leg.Name, "record start --cursor failed, restarting the recording without the pointer overlay: "+clip(cell(err.Error()), 300))
		began = s.clock()
		_, err = e.Run(ctx, recordTimeout, "record", "restart", path, "--fps", recordFPS) // restart: the failed start may have left a recording half open
	}
	if err != nil {
		slog.Warn("litmus: record start failed; continuing without a recording", "run", runID, "leg", leg.Name, "error", err)
		return leg
	}
	resetRecordingState(filepath.Join(s.store.root, runID), leg.Name)
	leg.RecordStartedAt, leg.RecordStoppedAt = began, time.Time{}
	return leg
}

// engineLog appends a line to <runDir>/<leg>/engine.log, where decisions about a leg's engine are kept.
func (s *Service) engineLog(runID, leg, line string) {
	dir := filepath.Join(s.store.root, runID, leg)
	if os.MkdirAll(dir, 0o750) != nil {
		return
	}
	if f, err := os.OpenFile(filepath.Join(dir, "engine.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_, _ = fmt.Fprintf(f, "%s %s\n", s.clock().Format(time.RFC3339), line)
		_ = f.Close()
	}
}

// recordingReceipt observes the active recording after a receipt. Two
// consecutive unchanged sizes mean the encoder has stopped making progress;
// restart it into a new part so later receipts remain covered. Drive calls this
// while it already owns e.mu.
func (s *Service) recordingReceipt(ctx context.Context, runID, leg string, e *Engine) {
	if s.store == nil || e == nil {
		return
	}
	run, err := s.store.Get(runID)
	if err != nil {
		return
	}
	i := slices.IndexFunc(run.Legs, func(l Leg) bool { return l.Name == leg })
	if i < 0 || run.Legs[i].RecordStartedAt.IsZero() || !run.Legs[i].RecordStoppedAt.IsZero() {
		return
	}
	runDir := filepath.Join(s.store.root, runID)
	state := currentRecordingState(runDir, leg)
	st, err := os.Stat(state.active)
	size := int64(0)
	if err == nil && st.Mode().IsRegular() {
		size = st.Size()
	} else if !state.observed {
		// The recorder may create its file after the first receipt. Count this
		// missing observation as the first unchanged sample.
		state.observed = true
		state.lastSize = 0
		state.unchanged = 1
		saveRecordingState(runDir, leg, state)
		return
	}
	if !state.observed {
		state.lastSize, state.observed, state.unchanged = size, true, 1
		saveRecordingState(runDir, leg, state)
		return
	}
	if size == state.lastSize {
		state.unchanged++
	} else {
		state.lastSize, state.unchanged = size, 1
	}
	state.lastSize = size
	if state.unchanged < 2 {
		saveRecordingState(runDir, leg, state)
		return
	}

	for {
		part := recordingPartPath(runDir, leg, state.nextPart)
		state.nextPart++
		if _, err := os.Stat(part); os.IsNotExist(err) {
			started := s.clock()
			if _, err := e.Run(ctx, recordTimeout, "record", "restart", part, "--fps", recordFPS); err != nil {
				s.engineLog(runID, leg, "record restart failed: "+clip(cell(err.Error()), 300))
				state.unchanged = 0
				saveRecordingState(runDir, leg, state)
				return
			}
			state.active, state.observed, state.lastSize, state.unchanged = part, false, 0, 0
			state.gaps = append(state.gaps, started)
			s.engineLog(runID, leg, fmt.Sprintf("recording stalled at %d bytes; restarted into %s", size, filepath.Base(part)))
			saveRecordingState(runDir, leg, state)
			return
		}
	}
}

// recordingDuration asks ffprobe for the duration. A non-empty file is not
// enough: an interrupted MP4 often has bytes but no playable index.
func (s *Service) recordingDuration(ctx context.Context, path string) (float64, bool) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
		return 0, false
	}
	playableCache.Lock()
	if cached, ok := playableCache.m[path]; ok && cached.size == st.Size() && cached.modified.Equal(st.ModTime()) {
		playableCache.Unlock()
		return cached.duration, cached.duration > 0
	}
	playableCache.Unlock()
	if s.runner == nil {
		return 0, false
	}
	probe := "ffprobe"
	if _, err := exec.LookPath(probe); err != nil {
		probe = homebrewFFprobe
	}
	out, err := s.runner.Run(ctx, "", nil, probe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path)
	duration := float64(0)
	if err == nil {
		line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		d, parseErr := strconv.ParseFloat(line, 64)
		if parseErr == nil {
			duration = d
		}
	}
	playableCache.Lock()
	playableCache.m[path] = playableCacheEntry{size: st.Size(), modified: st.ModTime(), duration: duration}
	playableCache.Unlock()
	return duration, duration > 0
}

func (s *Service) probeRecording(ctx context.Context, path string) bool {
	_, ok := s.recordingDuration(ctx, path)
	return ok
}

func recordingFiles(runDir, leg string) []string {
	base := recordingPath(runDir, leg)
	parts, _ := filepath.Glob(filepath.Join(runDir, leg, leg+"-part-*.mp4"))
	sort.Strings(parts)
	return append([]string{base}, parts...)
}

func concatListPath(runDir, leg string) string {
	return filepath.Join(runDir, "recording-"+leg+"-concat.txt")
}

// joinRecordingParts creates one seekable recording from all playable pieces.
// It leaves every source part in place so a failed join never destroys the
// only copy of the evidence. The returned path is suitable for clipping.
func (s *Service) joinRecordingParts(ctx context.Context, runDir, leg string) (string, error) {
	var playable []string
	for _, path := range recordingFiles(runDir, leg) {
		if s.probeRecording(ctx, path) {
			playable = append(playable, path)
		}
	}
	if len(playable) == 0 {
		return "", nil
	}
	if len(playable) == 1 {
		return playable[0], nil
	}
	var list strings.Builder
	for _, path := range playable {
		fmt.Fprintf(&list, "file '%s'\n", strings.ReplaceAll(path, "'", `'\''`))
	}
	listPath := concatListPath(runDir, leg)
	if err := os.WriteFile(listPath, []byte(list.String()), 0o600); err != nil {
		return "", err
	}
	joined := filepath.Join(runDir, leg, leg+"-joined.mp4")
	if err := s.media.Run(ctx, "-y", "-f", "concat", "-safe", "0", "-i", listPath, "-c", "copy", "-movflags", "+faststart", joined); err != nil {
		return "", err
	}
	if !fileExists(joined) {
		return "", fmt.Errorf("ffmpeg concat produced no %s", joined)
	}
	return joined, nil
}

func recordingHasGap(runDir, leg string, _, _ time.Time) bool {
	// Joined parts have no silent interval, so absolute receipt offsets are not
	// provable for any check once a restart happened. Use check-only slideshows
	// until each part records its exact wall-clock span.
	if parts, _ := filepath.Glob(filepath.Join(runDir, leg, leg+"-part-*.mp4")); len(parts) > 0 {
		return true
	}
	return len(currentRecordingState(runDir, leg).gaps) > 0
}

// stopRecording ends every recording the run's legs started and the persisted state says is still going (started, not
// stopped), once, and saves RecordStoppedAt. A stop failure never removes the file: finishVideo probes every piece and
// keeps the parts that still have a playable index. It returns the run with that state.
func (s *Service) stopRecording(ctx context.Context, run Run) Run {
	runID := run.ID
	stopped := map[string]time.Time{}
	for _, l := range run.Legs {
		if l.RecordStartedAt.IsZero() || !l.RecordStoppedAt.IsZero() {
			continue
		}
		stopped[l.Name] = s.clock()
		if l.Engine == nil {
			s.engineLog(runID, l.Name, "record stop skipped: engine is gone; keeping recording files for ffprobe")
			continue
		}
		e := s.engineFor(runID, l)
		e.mu.Lock()
		_, err := e.Run(ctx, stopTimeout, "record", "stop")
		e.mu.Unlock()
		if err != nil {
			playable := false
			for _, path := range recordingFiles(filepath.Join(s.store.root, runID), l.Name) {
				playable = playable || s.probeRecording(ctx, path)
			}
			slog.Warn("litmus: record stop failed; keeping recording files", "run", runID, "leg", l.Name, "playable", playable, "error", err)
			s.engineLog(runID, l.Name, fmt.Sprintf("record stop failed, recording kept (playable=%t): %s", playable, clip(cell(err.Error()), 300)))
		}
	}
	if len(stopped) == 0 {
		return run
	}
	out, err := s.update(runID, func(cur *Run) {
		for i, l := range cur.Legs {
			if at, ok := stopped[l.Name]; ok && cur.Legs[i].RecordStoppedAt.IsZero() {
				cur.Legs[i].RecordStoppedAt = at
			}
		}
	})
	if err != nil {
		slog.Warn("litmus: could not save the recording state", "run", runID, "error", err)
		return run
	}
	return out
}

// legVideo is what the video step left for one leg: raw is its recording (<leg>.mp4), cut is <leg>-cut.mp4.
// A cut without a recording is the slideshow of the leg's after-shots.
type legVideo struct {
	leg      string
	raw, cut bool
	// source is the playable file used for cuts. It can be a joined part file
	// while the original recording and each part remain on disk for evidence.
	source string
	// clips maps check ids to per-check PR clips. It is filled by finishVideo;
	// callers that publish comments can use checkClipPath when they need the
	// on-disk path without depending on this internal result.
	clips map[string]string
}

func (v legVideo) files() (out []string) {
	if v.raw {
		out = append(out, v.leg+".mp4")
	}
	if v.cut {
		out = append(out, v.leg+"-cut.mp4")
	}
	return out
}

func (v legVideo) String() string {
	switch {
	case v.raw && v.cut:
		return fmt.Sprintf("%s: recorded (%s.mp4; %s-cut.mp4 keeps a 2 s window around each action)", v.leg, v.leg, v.leg)
	case v.raw:
		return fmt.Sprintf("%s: recorded (%s.mp4)", v.leg, v.leg)
	}
	return fmt.Sprintf("%s: live recording lost, slideshow attached (%s-cut.mp4, the after-shots at 1 fps)", v.leg, v.leg)
}

// videoLine is the verdict's Video: text, one entry per leg that has video.
func videoLine(vs []legVideo) string {
	if len(vs) == 0 {
		return noVideo
	}
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = v.String()
	}
	return strings.Join(parts, "; ")
}

// finishVideo builds each leg's cut from that leg's own recording and only that leg's actions: a 2 s window around
// each action, captioned. A leg without a recording gets a 1 fps slideshow of its after-shots instead.
// ffmpeg failures are logged: the verdict never waits on video.
func (s *Service) finishVideo(ctx context.Context, runDir string, run Run) (out []legVideo) {
	ctx, cancel := context.WithTimeout(ctx, videoTimeout)
	defer cancel()
	all := readActions(runDir)
	for _, leg := range []string{"head", "base"} {
		acts := slices.DeleteFunc(slices.Clone(all), func(a actionRec) bool { return cmp.Or(a.Leg, "head") != leg })
		var l Leg
		if i := slices.IndexFunc(run.Legs, func(l Leg) bool { return l.Name == leg }); i >= 0 {
			l = run.Legs[i]
		}
		started := l.RecordStartedAt
		v, cut := legVideo{leg: leg}, cutPath(runDir, leg)
		var source string
		if !started.IsZero() {
			source, _ = s.joinRecordingParts(ctx, runDir, leg)
		}
		playable := source != ""
		if playable {
			v.raw, v.source = true, source
			if recordingHasGap(runDir, leg, time.Time{}, time.Time{}) {
				// The joined file is playable, but its timeline has no record of
				// the downtime. Keep it as raw evidence and use stills for the
				// action cut so captions never point at the wrong moment.
				v.cut = s.slideshow(ctx, runDir, leg, acts)
			} else if segs := segments(acts, started); len(segs) > 0 {
				duration, _ := s.recordingDuration(ctx, source)
				covered := segs[len(segs)-1].to <= time.Duration(duration*float64(time.Second))
				if covered {
					v.cut = s.encode(ctx, runDir, leg+"-cut-captions.srt", captions(segs), func(srt string) []string { return cutArgs(source, srt, cut, segs) }) == nil
				} else {
					v.cut = s.slideshow(ctx, runDir, leg, acts)
				}
			}
		} else {
			v.cut = s.slideshow(ctx, runDir, leg, acts)
		}
		if leg == "head" {
			v.clips = s.finishCheckClips(ctx, runDir, run, all, source, started)
		}
		forgetRecordingState(runDir, leg)
		if v.raw || v.cut {
			out = append(out, v)
		}
	}
	return out
}

// slideshow turns a leg's after-shots into <leg>-cut.mp4 and reports whether it was made.
func (s *Service) slideshow(ctx context.Context, runDir, leg string, acts []actionRec) bool {
	return s.slideshowTo(ctx, runDir, leg, acts, cutPath(runDir, leg), "slides-"+leg)
}

func (s *Service) slideshowTo(ctx context.Context, runDir, leg string, acts []actionRec, out, prefix string) bool {
	return s.slideshowToLimit(ctx, runDir, leg, acts, out, prefix, 0)
}

func (s *Service) slideshowToLimit(ctx context.Context, runDir, leg string, acts []actionRec, out, prefix string, limit time.Duration) bool {
	var list strings.Builder
	var segs []seg
	file := func(path string) { fmt.Fprintf(&list, "file '%s'\n", strings.ReplaceAll(path, "'", `'\''`)) }
	last, elapsed := "", time.Duration(0)
	for _, a := range acts {
		if a.Action == "test" { // a package test run has no screenshot
			continue
		}
		if st, err := os.Stat(a.After); a.After != "" && err == nil && st.Mode().IsRegular() {
			at, shown := elapsed, time.Second
			if ring := ringFrame(runDir, a); ring != "" { // the frame before the click, with the pointer's place marked
				shown += time.Second
				if limit > 0 && elapsed+shown > limit {
					break
				}
				file(ring)
				list.WriteString("duration 1\n")
			} else if limit > 0 && elapsed+shown > limit {
				break
			}
			segs, last = append(segs, seg{at, at + shown, caption(a)}), a.After
			elapsed += shown
			file(a.After)
			list.WriteString("duration 1\n")
		}
	}
	if last == "" {
		return false
	}
	if os.MkdirAll(filepath.Dir(out), 0o750) != nil {
		return false
	}
	file(last) // the concat demuxer ignores the last duration, so the last still is listed twice
	listPath := filepath.Join(runDir, prefix+".txt")
	if os.WriteFile(listPath, []byte(list.String()), 0o600) != nil {
		return false
	}
	srtName := prefix + "-captions.srt"
	if legName, ok := strings.CutPrefix(prefix, "slides-"); ok {
		srtName = legName + "-slide-captions.srt"
	}
	return s.encode(ctx, runDir, srtName, captions(segs), func(srt string) []string { return slideshowArgs(listPath, srt, out) }) == nil
}

func clipArgs(in, out string, from, duration time.Duration) []string {
	duration = min(duration, time.Minute)
	return []string{"-y", "-ss", fmt.Sprintf("%.3f", from.Seconds()), "-t", fmt.Sprintf("%.3f", duration.Seconds()), "-i", in,
		"-c", "copy", "-movflags", "+faststart", out}
}

func actionForReceipt(r Receipt, all []actionRec) (actionRec, bool) {
	if i := slices.IndexFunc(all, func(a actionRec) bool { return a.Seq == r.Seq && a.onLeg() == r.onLeg() }); i >= 0 {
		a := all[i]
		if a.At.IsZero() {
			a.At = r.At
		}
		if a.After == "" {
			a.After = r.AfterPath
		}
		return a, true
	}
	if r.AfterPath == "" {
		return actionRec{}, false
	}
	return actionRec{Seq: r.Seq, Leg: r.Leg, Action: r.Action, Args: r.Args, Before: r.BeforePath, After: r.AfterPath, At: r.At,
		TargetRole: r.TargetRole, TargetName: r.TargetName}, true
}

func (a actionRec) onLeg() string { return cmp.Or(a.Leg, "head") }

// finishCheckClips writes one clip for each head check. Any recorder restart
// falls back to a check-only slideshow, because concat removes downtime and
// makes absolute receipt offsets unprovable.
func (s *Service) finishCheckClips(ctx context.Context, runDir string, run Run, all []actionRec, source string, started time.Time) map[string]string {
	if s.store == nil {
		return nil
	}
	checks, err := s.store.Checks(run.ID)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, check := range checks {
		if check.onLeg() != "head" {
			continue
		}
		var acts []actionRec
		for _, r := range check.legReceipts() {
			if a, ok := actionForReceipt(r, all); ok {
				acts = append(acts, a)
			}
		}
		if len(acts) == 0 {
			continue
		}
		slices.SortFunc(acts, func(a, b actionRec) int { return a.At.Compare(b.At) })
		first, last := acts[0].At, acts[len(acts)-1].At
		if first.IsZero() || last.IsZero() {
			continue
		}
		path := checkClipPath(runDir, check.ID)
		duration, playable := s.recordingDuration(ctx, source)
		covered := !started.IsZero() && last.Sub(started)+2*time.Second <= time.Duration(duration*float64(time.Second))
		if source != "" && playable && covered && !recordingHasGap(runDir, "head", first, last) {
			from := max(first.Sub(started)-time.Second, 0)
			to := max(last.Sub(started)+2*time.Second, from+time.Millisecond)
			duration := min(to-from, time.Minute)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err == nil && s.media.Run(ctx, clipArgs(source, path, from, duration)...) == nil && fileExists(path) {
				out[check.ID] = path
				continue
			}
		}
		if s.slideshowToLimit(ctx, runDir, "head", acts, path, "slides-check-"+check.ID, time.Minute) {
			out[check.ID] = path
		}
	}
	return out
}

const (
	ringRadius = 24.0
	ringStroke = 3.0
)

// ringFrame copies the shot taken before a click to receipts/<seq>-click.png with a ring at the click's place and
// returns its path, or "" when the click has no coordinates or the shot cannot be read or drawn on.
func ringFrame(runDir string, a actionRec) string {
	if a.Click == nil || a.Before == "" {
		return ""
	}
	f, err := os.Open(a.Before)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	src, err := png.Decode(f)
	if err != nil {
		return ""
	}
	b := src.Bounds()
	cx := float64(b.Min.X) + a.Click.X/a.Click.W*float64(b.Dx()) // CSS pixels to the shot's pixels
	cy := float64(b.Min.Y) + a.Click.Y/a.Click.H*float64(b.Dy())
	if a.Click.W <= 0 || a.Click.H <= 0 || cx < float64(b.Min.X) || cy < float64(b.Min.Y) || cx >= float64(b.Max.X) || cy >= float64(b.Max.Y) {
		return ""
	}
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)
	drawRing(dst, cx, cy)
	out := filepath.Join(runDir, "receipts", fmt.Sprintf("%d-click.png", a.Seq))
	w, err := os.Create(out)
	if err != nil {
		return ""
	}
	err = png.Encode(w, dst)
	if cerr := w.Close(); err != nil || cerr != nil {
		return ""
	}
	return out
}

// drawRing draws a magenta ring of ringRadius and ringStroke centred on (cx, cy), with a 1 px white edge on both
// sides so it shows on any background.
func drawRing(img *image.RGBA, cx, cy float64) {
	edge := ringRadius + ringStroke/2 + 1
	b := img.Bounds()
	for y := max(b.Min.Y, int(cy-edge)); y <= min(b.Max.Y-1, int(cy+edge)); y++ {
		for x := max(b.Min.X, int(cx-edge)); x <= min(b.Max.X-1, int(cx+edge)); x++ {
			d := math.Abs(math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy) - ringRadius)
			switch {
			case d <= ringStroke/2:
				img.SetRGBA(x, y, color.RGBA{R: 255, G: 0, B: 255, A: 255})
			case d <= ringStroke/2+1:
				img.SetRGBA(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			}
		}
	}
}

// encode writes the caption file and runs ffmpeg with the arguments build returns for it.
func (s *Service) encode(ctx context.Context, runDir, srtName, srt string, build func(srtPath string) []string) error {
	path := filepath.Join(runDir, srtName)
	err := os.WriteFile(path, []byte(srt), 0o600)
	if err == nil {
		err = s.media.Run(ctx, build(path)...)
	}
	if err != nil {
		slog.Warn("litmus: video step failed", "run", filepath.Base(runDir), "error", err)
	}
	return err
}

// slideshowArgs turns a concat list of stills into a 1280x720 1 fps H.264 file with a soft caption track
// (this ffmpeg has no drawtext filter, so captions cannot be burned in).
func slideshowArgs(list, srt, out string) []string {
	return []string{"-y", "-f", "concat", "-safe", "0", "-i", list, "-i", srt, "-map", "0:v", "-map", "1:s",
		"-vf", "scale=1280:720:force_original_aspect_ratio=decrease,pad=1280:720:(ow-iw)/2:(oh-ih)/2,fps=1,format=yuv420p",
		"-c:v", "libx264", "-c:s", "mov_text", "-movflags", "+faststart", out}
}

// seg is a stretch of video and its caption.
type seg struct {
	from, to time.Duration
	text     string
}

// segments gives each action a 2 s window of the recording (1 s either side) and merges windows that touch.
func segments(acts []actionRec, start time.Time) (out []seg) {
	for _, a := range acts {
		if a.Action == "test" { // a package test run is not a moment on screen
			continue
		}
		at := a.At.Sub(start)
		if at < 0 {
			continue
		}
		from, to := max(at-time.Second, 0), at+time.Second
		if n := len(out); n > 0 && from <= out[n-1].to {
			out[n-1].to, out[n-1].text = to, out[n-1].text+" / "+caption(a)
		} else {
			out = append(out, seg{from, to, caption(a)})
		}
	}
	return out
}

// caption is the line under a step in the video: Step 3: click "Collapse sidebar".
func caption(a actionRec) string {
	return fmt.Sprintf("Step %d: %s", a.Seq, stepText(a.Action, a.Args, a.TargetRole, a.TargetName))
}

// refInfo is what a snapshot line says about an element: its role and accessible name.
type refInfo struct{ role, name string }

var snapRef = regexp.MustCompile(`^\s*- ([A-Za-z]+)(?: ("(?:[^"\\]|\\.)*"))?[^\n]*?\bref=(e[0-9]+)`)

// parseRefs reads `snapshot` output (`- button "Go back" [ref=e7]`) into refs keyed the way actions spell them (@e7).
func parseRefs(snap string) map[string]refInfo {
	out := map[string]refInfo{}
	for _, l := range strings.Split(snap, "\n") {
		if m := snapRef.FindStringSubmatch(l); m != nil {
			name, _ := strconv.Unquote(m[2])
			out["@"+m[3]] = refInfo{m[1], name}
		}
	}
	return out
}

var roleWords = map[string]string{"button": "a button", "link": "a link", "textbox": "a text field", "combobox": "a field", "checkbox": "a checkbox", "tab": "a tab", "menuitem": "a menu item"}

// plainName is an element's accessible name as a reader should see it: a project row's name is joined to its path
// and age, so only the first line is kept, cut at the first word that is a local path.
func plainName(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	isPath := func(w string) bool {
		return strings.HasPrefix(w, "/") || strings.HasPrefix(w, "~/") || strings.Contains(w, "/Users/") || strings.Contains(w, "/home/")
	}
	words := strings.Fields(s)
	if i := slices.IndexFunc(words, isPath); i > 0 {
		words = words[:i]
	}
	return strings.Join(slices.DeleteFunc(words, isPath), " ")
}

// stepText says what an action did in the words a reader would use: click "Go back", type "hi" into "Message the
// agent", press Enter. An element the snapshot did not name is "a button" (or "an element" with no role either).
func stepText(action string, args []string, role, name string) string {
	quote := func(s string) string {
		return `"` + clip(strings.ReplaceAll(strings.Join(strings.Fields(s), " "), `"`, "'"), 40) + `"`
	}
	target := cmp.Or(roleWords[role], "an element")
	if name = plainName(name); name != "" {
		target = quote(name)
	}
	text := ""
	if len(args) > 1 {
		text = quote(args[1])
	}
	switch action {
	case "press":
		return "press " + strings.Join(args, " ")
	case "fill", "type":
		return "type " + cmp.Or(text, "text") + " into " + target
	case "select":
		return "select " + cmp.Or(text, "an option") + " in " + target
	}
	return action + " " + target
}

// cutArgs joins the segments of in into out, one seeked input per segment, with the captions as a soft track.
func cutArgs(in, srt, out string, segs []seg) []string {
	args, graph := []string{"-y"}, ""
	for i, sg := range segs {
		args = append(args, "-ss", fmt.Sprintf("%.3f", sg.from.Seconds()), "-t", fmt.Sprintf("%.3f", (sg.to-sg.from).Seconds()), "-i", in)
		graph += fmt.Sprintf("[%d:v]", i)
	}
	graph += fmt.Sprintf("concat=n=%d:v=1:a=0,format=yuv420p[v]", len(segs))
	return append(args, "-i", srt, "-filter_complex", graph, "-map", "[v]", "-map", fmt.Sprintf("%d:s", len(segs)),
		"-c:v", "libx264", "-c:s", "mov_text", "-movflags", "+faststart", out)
}

// captions renders segs as SRT on the output timeline: each segment starts where the previous one ended.
func captions(segs []seg) string {
	var b strings.Builder
	var at time.Duration
	for i, sg := range segs {
		d := sg.to - sg.from
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, srtTime(at), srtTime(at+d), sg.text)
		at += d
	}
	return b.String()
}

func srtTime(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d:%02d,%03d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60, d.Milliseconds()%1000)
}
