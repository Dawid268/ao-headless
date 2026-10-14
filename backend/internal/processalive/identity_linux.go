//go:build linux

package processalive

import (
	"context"
	"os"
	"strconv"
	"strings"
)

func identify(ctx context.Context, pid int) Identity {
	path := "/proc/" + strconv.Itoa(pid) + "/stat"
	stat, err := os.ReadFile(path)
	if err != nil {
		return Identity{}
	}
	ticks := linuxStartTicks(string(stat))
	if ticks == "" {
		return Identity{}
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	bootID := strings.TrimSpace(string(boot))
	if err != nil || bootID == "" {
		return Identity{}
	}
	startedAt := processStartedAt(ctx, pid)
	// The slower wall-clock lookup must describe the same process incarnation.
	current, err := os.ReadFile(path)
	if err != nil || linuxStartTicks(string(current)) != ticks {
		return Identity{}
	}
	return Identity{StartID: "linux:" + bootID + ":" + ticks, StartedAt: startedAt}
}

func linuxStartTicks(stat string) string {
	// comm (field 2) can contain spaces and parentheses; field 3 follows its last ).
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return ""
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 20 {
		return ""
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return ""
	}
	return strconv.FormatUint(ticks, 10)
}
