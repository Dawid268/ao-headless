//go:build !darwin && !windows

package processalive

import (
	"context"
	"strconv"
	"strings"
	"time"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

func processStartedAt(ctx context.Context, pid int) time.Time {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	cmd := aoprocess.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(cmd.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		return time.Time{}
	}
	start, err := time.Parse(time.ANSIC, strings.TrimSpace(string(out)))
	if err != nil {
		return time.Time{}
	}
	return start.UTC()
}
