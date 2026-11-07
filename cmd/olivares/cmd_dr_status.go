// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

// drStatus is the part of the engine's backup schedule this command reads, plus
// whether every copy sits on the engine's own data disk.
type drStatus struct {
	Enabled          bool            `json:"enabled"`
	Cron             string          `json:"cron"`
	RetainDays       int             `json:"retain_days"`
	NextRun          string          `json:"next_run,omitempty"`
	LastRun          string          `json:"last_run,omitempty"`
	LastRunStatus    string          `json:"last_run_status,omitempty"`
	LastRunError     string          `json:"last_run_error,omitempty"`
	VerifyCron       string          `json:"verify_cron,omitempty"`
	LastVerify       string          `json:"last_verify,omitempty"`
	LastVerifyStatus string          `json:"last_verify_status,omitempty"`
	LastVerifyError  string          `json:"last_verify_error,omitempty"`
	LocalTarget      string          `json:"local_target,omitempty"`
	Offsite          json.RawMessage `json:"offsite,omitempty"`
	SameDisk         bool            `json:"same_disk"`
}

func drStatusCmd() *cobra.Command {
	flags := &authClientFlags{}
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "status",
		Short:       "Show the engine's backup schedule, last backup, last restore test and where the copies are",
		Long: "Read the running engine's scheduled backups and restore tests. Every install backs\n" +
			"itself up daily and tests a restore weekly from its first start, into its own data\n" +
			"directory; this command says so when that disk holds every copy, and names the step\n" +
			"that adds an offsite one. Requires system administration.",
		Example: "  olivares dr status\n  olivares dr status -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client := bootstrapClient{flags: flags, surface: "dr status"}
			raw, err := client.expect(cmd, http.MethodGet, "/v1/console/dr/schedule", nil, http.StatusOK)
			if err != nil {
				return err
			}
			var st drStatus
			if err := decodeBootstrapJSON(client.surface, raw, &st); err != nil {
				return err
			}
			st.SameDisk = st.LocalTarget == "" && (len(st.Offsite) == 0 || string(st.Offsite) == "null")
			return renderOut(cmd, func(out io.Writer) error { return writeDRStatus(out, st) }, st)
		},
	}
	flags.addPersistent(cmd)
	return cmd
}

func writeDRStatus(out io.Writer, st drStatus) error {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }
	if st.Enabled && st.Cron != "" {
		kept := "every bundle kept"
		if st.RetainDays > 0 {
			kept = fmt.Sprintf("kept %d days", st.RetainDays)
		}
		p("Backups: %s (UTC), %s; next %s\n", termSafe(st.Cron), kept, orDash(termSafe(st.NextRun)))
	} else {
		p("Backups: off\n")
	}
	p("Last backup: %s\n", drOutcome(st.LastRun, st.LastRunStatus, st.LastRunError))
	if st.VerifyCron != "" {
		p("Restore test: %s (UTC); last %s\n", termSafe(st.VerifyCron), drOutcome(st.LastVerify, st.LastVerifyStatus, st.LastVerifyError))
	} else {
		p("Restore test: off\n")
	}
	if st.SameDisk {
		p("Same disk: the backups are on the engine's data disk, so losing that disk loses them.\n")
		p("Add an offsite copy: set an offsite target on the console's Backups page, or copy one bundle with `olivares dr push --in <bundle> --offsite-bucket <bucket>`; keep the recovery key off this host.\n")
	}
	return nil
}

func drOutcome(at, status, errMsg string) string {
	switch {
	case at == "":
		return "none yet"
	case errMsg != "":
		return termSafe(fmt.Sprintf("%s %s: %s", at, status, errMsg))
	default:
		return termSafe(at + " " + status)
	}
}
