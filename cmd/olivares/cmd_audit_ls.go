// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// auditListCmd is `olivares audit ls`: the most recent events of the engine's
// ledger, newest first. The other audit verbs prove and export the ledger offline;
// this one answers "what just happened" (HU-29: no command showed recent events).
//
// The API walks the ledger oldest first and every list appends an `audit.read`
// event (HU-15 counted 41 of 56 rows), so this reads the head, then a window
// before it, and leaves the reads out unless --all.
func auditListCmd() *cobra.Command {
	var (
		cfg         agentClientConfig
		limit       int
		system, all bool
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list", "recent"},
		Short:   "Show the most recent audit events, newest first",
		Long: "ls prints the latest events of your organization's audit ledger, newest first: time,\n" +
			"who, what and on which object. --system shows the deployment's own ledger (installs,\n" +
			"sign-ins). Use it to see what happened recently.",
		Example: "  olivares audit ls\n  olivares audit ls --limit 50 --system",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit < 1 || limit > 200 {
				return sentence(exitcode.Usage, "--limit must be between 1 and 200.")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			path := "/v1/audit"
			if system {
				path = "/v1/audit/system"
			}
			var head struct {
				HeadSeq int64 `json:"head_seq"`
			}
			_, b, err := cfg.do(cmd.Context(), "GET", path+"?limit=1", nil)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(b, &head); err != nil {
				return err
			}
			q := url.Values{"limit": {"1000"}}
			q.Set("from", fmt.Sprint(max(int64(1), head.HeadSeq-int64(limit)*5)))
			if !all {
				q.Set("exclude_action", "audit.read")
			}
			_, b, err = cfg.do(cmd.Context(), "GET", path+"?"+q.Encode(), nil)
			if err != nil {
				return err
			}
			var page struct {
				Items []struct {
					Seq        int64  `json:"seq"`
					OccurredAt string `json:"occurred_at"`
					Actor      string `json:"actor"`
					Action     string `json:"action"`
					TargetKind string `json:"target_kind"`
					TargetID   string `json:"target_id"`
				} `json:"items"`
			}
			if err := json.Unmarshal(b, &page); err != nil {
				return err
			}
			items := page.Items
			if len(items) > limit {
				items = items[len(items)-limit:]
			}
			for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
				items[i], items[j] = items[j], items[i]
			}
			table := termrender.Table{Header: []string{"time", "actor", "action", "target"}, Empty: "No events yet."}
			for _, it := range items {
				target := it.TargetKind
				if it.TargetID != "" {
					target += " " + it.TargetID
				}
				table.Rows = append(table.Rows, []string{auditTime(it.OccurredAt), it.Actor, it.Action, target})
			}
			return renderTableOut(cmd, table, items)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().IntVar(&limit, "limit", 20, "how many events (1 to 200)")
	cmd.Flags().BoolVar(&system, "system", false, "the system ledger (installs, sign-ins, deployment-wide actions)")
	cmd.Flags().BoolVar(&all, "all", false, "include the ledger's own read events (audit.read)")
	return cmd
}

// auditTime shows an event time in this computer's zone, to the second.
func auditTime(s string) string {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -0700 MST"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Local().Format("2006-01-02 15:04:05")
		}
	}
	return s
}
