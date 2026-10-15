package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

func newUpdateDrainCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use:   "update-drain",
		Short: "Pause new chat admits so this host can be replaced",
		Long:  "Closes new chat-host admissions on the local daemon. Does not stop projects. Safe to run before re-pulling a container image.",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := ctx.inspectDaemon(cmd.Context())
			if err != nil {
				return err
			}
			if st.Port == 0 {
				return fmt.Errorf("daemon is not reachable")
			}
			reqCtx, cancel := context.WithTimeout(cmd.Context(), probeTimeout)
			defer cancel()
			req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, fmt.Sprintf("http://%s:%d/api/v1/system/update-drain", config.LoopbackHost, st.Port), http.NoBody)
			if err != nil {
				return err
			}
			resp, err := ctx.deps.HTTPClient.Do(req)
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fmt.Errorf("update-drain: HTTP %d: %s", resp.StatusCode, body)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Safe to re-pull image. New work on this host is paused. Projects were not stopped.")
			return err
		},
	}
}
