//go:build !windows

package previewserver

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

func previewCommand(name string, args ...string) *exec.Cmd {
	cmd := aoprocess.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// errStalePreviewPID reports that a recorded preview PID could not be
// re-verified as the process AO launched, so no signal was sent.
var errStalePreviewPID = errors.New("preview pid is stale or recycled; refusing to signal")

// bootTicksPrefix marks a start time read as the boot's id plus clock ticks
// since that boot.
const bootTicksPrefix = "boot+"

// bootID names the running boot, or "" without /proc (macOS). Ticks since boot
// repeat from one boot to the next, and the registry outlives a reboot: without
// the id, a record left by a daemon that died before the reboot could match an
// unrelated process that started at the same tick.
var bootID = sync.OnceValue(func() string {
	id, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(id))
})

// previewProcessStartTime returns the kernel-reported start time of pid as an
// opaque string, or "" when the process does not exist. It is captured at
// launch and compared verbatim before any group signal: a bare PID stops
// identifying anything the moment the child is reaped, and a group kill on a
// recycled number destroys an unrelated process family — a tmux server is its
// own group leader, so one stale group kill took out every tmux session at
// once (issue #3475).
//
// On Linux it is the boot id plus the start in clock ticks since boot
// (/proc/<pid>/stat field 22). ps derives lstart from the boot time, which
// moves whenever the wall clock steps (a VM resumed from a pause, an NTP
// step): a preview launched before the step stopped matching its own record,
// and AO refused to stop it.
func previewProcessStartTime(pid int) string {
	if pid <= 0 {
		return ""
	}
	if bootID() == "" {
		return psStartTime(pid)
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	// Fields after the command name, which may itself contain ") ".
	fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
	if len(fields) <= 19 {
		return ""
	}
	return bootTicksPrefix + bootID() + "+" + fields[19]
}

// psStartTime is ps's lstart: stable on macOS, where the kernel keeps the
// wall-clock start, and what AO recorded on Linux before boot ticks.
func psStartTime(pid int) string {
	out, err := aoprocess.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// previewGroupHasMembers reports whether any process still belongs to process
// group pid.
func previewGroupHasMembers(pid int) bool {
	return aoprocess.Command("pgrep", "-g", strconv.Itoa(pid)).Run() == nil
}

// signalPreviewGroup delivers sig to pid's process group only while the group
// leader is still alive and its kernel start time matches the recorded one
// verbatim. Anything less is not proof that the group is AO's preview: once
// the leader is gone the number may already have been recycled, and a group
// kill on a recycled PID can take down an unrelated family (a tmux server and
// every agent session in it, issue #3475). A descendant that survives its
// leader is deliberately leaked rather than guessed at.
//
// ponytail: the verify-then-kill pair is not atomic; without pidfd-style
// primitives a recycle in that microsecond window is untestable and accepted.
func signalPreviewGroup(pid int, recordedStart string, sig syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	if recordedStart == "" {
		return errStalePreviewPID
	}
	read := psStartTime // macOS, or a Linux record from before boot ticks
	if strings.HasPrefix(recordedStart, bootTicksPrefix) {
		read = previewProcessStartTime
	}
	current := read(pid)
	if current == "" {
		return nil // leader gone: nothing provably AO's remains
	}
	if current != recordedStart {
		return errStalePreviewPID
	}
	err := syscall.Kill(-pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// signalPreviewRoot signals only the direct child through os.Process, which
// refuses to signal an already-reaped child, so it can never hit a recycled
// PID. It is the fallback for runs whose launch identity could not be
// captured: descendants may leak, innocents never die.
func signalPreviewRoot(cmd *exec.Cmd, sig syscall.Signal) error {
	err := cmd.Process.Signal(sig)
	if err == nil || errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func terminatePreviewProcess(cmd *exec.Cmd, startTime string) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if startTime == "" {
		return signalPreviewRoot(cmd, syscall.SIGTERM)
	}
	return signalPreviewGroup(cmd.Process.Pid, startTime, syscall.SIGTERM)
}

func forceKillPreviewProcess(cmd *exec.Cmd, startTime string) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if startTime == "" {
		return signalPreviewRoot(cmd, syscall.SIGKILL)
	}
	return signalPreviewGroup(cmd.Process.Pid, startTime, syscall.SIGKILL)
}

func forceKillPreviewPID(pid int, startTime string) error {
	return signalPreviewGroup(pid, startTime, syscall.SIGKILL)
}
