// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/spf13/cobra"
)

func newSessionIdleCmd() *cobra.Command {
	flags := &authClientFlags{}
	root := &cobra.Command{Annotations: map[string]string{"changes_data": "read"}, Use: "session-idle", Short: "Read or change optional idle session stops", Long: "Manage the instance-wide idle-stop setting without restarting the engine. It is off by default. Only finished sessions with no open view, approval, running child or peer, or newer message can stop.", Example: "  olivares config session-idle get\n  olivares config session-idle set --minutes 6\n  olivares config session-idle set --minutes 0"}
	flags.addPersistent(root)
	client := bootstrapClient{flags: flags, surface: "session-idle"}
	call := func(cmd *cobra.Command, method string, body any) error {
		raw, err := client.expect(cmd, method, "/v1/system/session-idle", body, http.StatusOK)
		if err != nil {
			return err
		}
		var state api.SessionIdleSettings
		if err := decodeBootstrapJSON("session-idle", raw, &state); err != nil {
			return err
		}
		return renderOut(cmd, func(out io.Writer) error {
			_, err := fmt.Fprintf(out, "Stop after minutes in idle: %d (0 is off)\n", state.IdleStopMinutes)
			return err
		}, state)
	}
	root.AddCommand(&cobra.Command{Annotations: map[string]string{"changes_data": "read"}, Use: "get", Short: "Read the saved instance idle-stop setting", Long: "Read the instance idle-stop window from the running engine, in minutes after the five-minute idle projection. Zero means no session is stopped by this policy.", Example: "  olivares config session-idle get -o json", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return call(cmd, http.MethodGet, nil) }})
	var choice api.SessionIdleSettings
	set := &cobra.Command{Annotations: map[string]string{"changes_data": "write"}, Use: "set", Short: "Save the idle-stop window; 0 turns it off", Long: "Choose 0 (off) or 6–10080 minutes after the five-minute idle projection. The saved setting applies on the next lifecycle sweep.", Example: "  olivares config session-idle set --minutes 6", Args: cobra.NoArgs}
	set.Flags().IntVar(&choice.IdleStopMinutes, "minutes", 0, "minutes in idle before stopping (0 is off; enabled: 6–10080)")
	set.RunE = func(cmd *cobra.Command, _ []string) error {
		if !cmd.Flags().Changed("minutes") {
			return fmt.Errorf("choose --minutes (0 turns off)")
		}
		if err := choice.Validate(); err != nil {
			return err
		}
		return call(cmd, http.MethodPut, choice)
	}
	root.AddCommand(set)
	return root
}
