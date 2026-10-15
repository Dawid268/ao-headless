//go:build !windows

package previewserver

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPreviewProcessStartTimeKeepsLegacyFormat(t *testing.T) {
	for _, tc := range []struct{ name, locale, zone string }{
		{"UTC", "C", "UTC"},
		{"non-UTC", "C", "Asia/Kolkata"},
		{"localized", "fr_FR.UTF-8", "Europe/Paris"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LC_ALL", tc.locale)
			t.Setenv("TZ", tc.zone)
			want := legacyPSStartTime(t, os.Getpid())
			if got := previewProcessStartTime(os.Getpid()); got != want {
				t.Fatalf("process start time = %q, want legacy ps format %q", got, want)
			}
		})
	}
}

func TestPreviewProcessStartTimeRejectsInvalidPID(t *testing.T) {
	for _, pid := range []int{-1, 0} {
		if got := previewProcessStartTime(pid); got != "" {
			t.Fatalf("process start time for pid %d = %q, want empty identity", pid, got)
		}
	}
}

func legacyPSStartTime(t *testing.T, pid int) string {
	t.Helper()
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("read legacy process start time: %v", err)
	}
	start := strings.TrimSpace(string(out))
	if start == "" {
		t.Fatal("legacy ps returned no process start time")
	}
	return start
}

// fakeStartTime is a launch identity that can never match a live process.
const fakeStartTime = "Thu Jan  1 00:00:00 1970"

func startSleeper(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := previewCommand(args[0], args[1:]...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", args, err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	return cmd
}

// sleeperRunning reports whether the un-reaped child is still alive (WNOHANG:
// 0 means running, pid means it exited and became reapable).
func sleeperRunning(t *testing.T, pid int) bool {
	t.Helper()
	var status syscall.WaitStatus
	n, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	if err != nil {
		t.Fatalf("wait4 %d: %v", pid, err)
	}
	return n == 0
}

// Regression for issue #3475 (A1): a PID recorded by a previous daemon
// generation is stale by construction. If the live process holding that number
// does not carry the recorded launch identity, reap must not kill it — the
// recycled number can belong to anything, including a tmux server whose group
// kill wipes every session at once.
func TestReapPersistedProcessesRefusesMismatchedIdentity(t *testing.T) {
	cmd := startSleeper(t, "sleep", "60")

	dataDir := t.TempDir()
	registry, err := json.Marshal([]persistedProcess{{
		SessionID: "ao-1",
		PID:       cmd.Process.Pid,
		Port:      4173,
		StartedAt: time.Now().UTC(),
		StartTime: fakeStartTime,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, processRegistryFile), registry, 0o600); err != nil {
		t.Fatal(err)
	}

	New(slog.New(slog.NewTextHandler(io.Discard, nil)), dataDir)

	time.Sleep(100 * time.Millisecond)
	if !sleeperRunning(t, cmd.Process.Pid) {
		t.Fatal("reap killed a live process whose identity did not match the recorded preview")
	}
}

// A legacy registry entry (written before launch identities were recorded)
// carries no proof of ownership, so reap must skip it entirely.
func TestReapPersistedProcessesSkipsLegacyEntriesWithoutStartTime(t *testing.T) {
	cmd := startSleeper(t, "sleep", "60")

	dataDir := t.TempDir()
	registry, err := json.Marshal([]persistedProcess{{
		SessionID: "ao-1",
		PID:       cmd.Process.Pid,
		Port:      4173,
		StartedAt: time.Now().UTC(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, processRegistryFile), registry, 0o600); err != nil {
		t.Fatal(err)
	}

	New(slog.New(slog.NewTextHandler(io.Discard, nil)), dataDir)

	time.Sleep(100 * time.Millisecond)
	if !sleeperRunning(t, cmd.Process.Pid) {
		t.Fatal("reap killed a live process from a legacy registry entry with no recorded identity")
	}
}

// Reap still does its job when the recorded identity checks out.
func TestReapPersistedProcessesKillsVerifiedProcess(t *testing.T) {
	cmd := startSleeper(t, "sleep", "60")
	startTime := legacyPSStartTime(t, cmd.Process.Pid)

	dataDir := t.TempDir()
	registry, err := json.Marshal([]persistedProcess{{
		SessionID: "ao-1",
		PID:       cmd.Process.Pid,
		Port:      4173,
		StartedAt: time.Now().UTC(),
		StartTime: startTime,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, processRegistryFile), registry, 0o600); err != nil {
		t.Fatal(err)
	}

	New(slog.New(slog.NewTextHandler(io.Discard, nil)), dataDir)

	waitUntil(t, 2*time.Second, func() bool { return !sleeperRunning(t, cmd.Process.Pid) },
		"verified orphaned preview process was not killed")
}

func TestSignalPreviewGroupRefusesMismatchedStartTime(t *testing.T) {
	cmd := startSleeper(t, "sleep", "60")

	err := signalPreviewGroup(cmd.Process.Pid, fakeStartTime, syscall.SIGKILL)
	if !errors.Is(err, errStalePreviewPID) {
		t.Fatalf("signalPreviewGroup err = %v, want errStalePreviewPID", err)
	}
	time.Sleep(100 * time.Millisecond)
	if !sleeperRunning(t, cmd.Process.Pid) {
		t.Fatal("group kill went through despite a mismatched launch identity")
	}
}

func TestSignalPreviewGroupKillsVerifiedGroup(t *testing.T) {
	cmd := startSleeper(t, "sleep", "60")
	startTime := previewProcessStartTime(cmd.Process.Pid)
	if startTime == "" {
		t.Fatal("could not capture launch identity")
	}

	if err := signalPreviewGroup(cmd.Process.Pid, startTime, syscall.SIGKILL); err != nil {
		t.Fatalf("signalPreviewGroup: %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool { return !sleeperRunning(t, cmd.Process.Pid) },
		"verified preview group was not killed")
}

// Once the root is reaped, the PID no longer provably belongs to AO's
// preview, so no group kill may happen even though descendants survive: a
// leaked preview process is safer than a group kill landing on a recycled
// PID. The orphan must still be alive after the escalation call.
func TestForceKillLeaksOrphanedGroupAfterRootIsReaped(t *testing.T) {
	cmd := previewCommand("/bin/sh", "-c", "sleep 60 & sleep 1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	startTime := previewProcessStartTime(pid)
	if startTime == "" {
		t.Fatal("could not capture launch identity")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("root exit: %v", err)
	}
	if !previewGroupHasMembers(pid) {
		t.Fatal("expected an orphaned group member after the root was reaped")
	}

	if err := forceKillPreviewProcess(cmd, startTime); err != nil {
		t.Fatalf("forceKillPreviewProcess: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if !previewGroupHasMembers(pid) {
		t.Fatal("post-exit escalation killed the orphaned group; it must leak rather than signal an unverifiable PID")
	}
}

func waitUntil(t *testing.T, timeout time.Duration, done func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal(message)
}
