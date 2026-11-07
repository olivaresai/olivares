// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// sessionUsagePath is the engine's one read over the session runs the caller may read.
const sessionUsagePath = "/v1/m/sessions/usage"

// sessionUsageDimensions are the group_by values the engine answers.
var sessionUsageDimensions = []string{"model", "account", "provider", "tool"}

// accountNameLimit is the one page of provider accounts a table reads names from.
const accountNameLimit = 200

// newSessionUsageCmd totals what the sessions the caller may read used, by one dimension.
// The engine does the sums; this prints them, and keeps a sum nobody reported as "-" and
// null, never 0.
func newSessionUsageCmd() *cobra.Command {
	var (
		cfg          agentClientConfig
		by           string
		since, until string
		daily        bool
	)
	cmd := &cobra.Command{
		Annotations: map[string]string{"changes_data": "read"},
		Use:         "usage",
		Short:       "Show what sessions used, by model, account, provider or tool",
		Long: "usage totals the sessions you may read: how many ran, their tokens and their cost, one row\n" +
			"per model, account, provider or tool (--by). A session counts on the day it started.\n\n" +
			"A total is - when no session in the row reported it: unknown is not zero. The last two\n" +
			"columns count the sessions that reported no tokens and no cost, so a cost that looks small\n" +
			"because half the sessions never reported one says so. Costs are what the tools reported.\n\n" +
			"--since and --until take 2h, 3d, a date like 2026-10-01 or a full time. --daily adds the day.",
		Example: "  olivares session usage\n" +
			"  olivares session usage --by tool --since 7d -o json\n" +
			"  olivares session usage --by account --since 2026-10-01 --daily",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !slices.Contains(sessionUsageDimensions, by) {
				return sentence(exitcode.Usage, "--by must be one of %s, not %q.", strings.Join(sessionUsageDimensions, ", "), oneRow(by))
			}
			query := url.Values{"group_by": {by}}
			now := time.Now()
			if v, err := parseSince(since, now); err != nil {
				return err
			} else if v != "" {
				query.Set("since", v)
			}
			if v, err := parseSince(until, now); err != nil {
				return sentence(exitcode.Usage, "Until %q is not a time. Use 2h, 3d, a date like 2026-10-01 or a time like 2026-10-01T08:30:00Z.", oneRow(until))
			} else if v != "" {
				query.Set("until", v)
			}
			if daily {
				query.Set("bucket", "day")
			}
			if err := cfg.resolve(); err != nil {
				return err
			}
			_, raw, err := cfg.do(cmd.Context(), "GET", sessionUsagePath+"?"+query.Encode(), nil)
			if err != nil {
				return err
			}
			// Numbers stay exact and null stays null: -o json prints what the engine said.
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			var body map[string]any
			if err := dec.Decode(&body); err != nil {
				return sentence(exitcode.Server, "The engine's usage answer is not JSON: %v", err)
			}
			items, _ := body["items"].([]any)
			return renderOut(cmd, func(out io.Writer) error {
				var names map[string]string
				if by == "account" && len(items) > 0 {
					names = cfg.accountNames(cmd.Context())
				}
				renderTo(out).Table(sessionUsageTable(by, daily, items, names))
				return nil
			}, body)
		},
	}
	cfg.addFlags(cmd)
	cmd.Flags().StringVar(&by, "by", "model", "group by model, account, provider or tool")
	cmd.Flags().StringVar(&since, "since", "", "only sessions started since this time (2h, 3d, 2026-10-01 or a full time)")
	cmd.Flags().StringVar(&until, "until", "", "only sessions started before this time")
	cmd.Flags().BoolVar(&daily, "daily", false, "split every row by the day its sessions started")
	_ = cmd.RegisterFlagCompletionFunc("by", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return sessionUsageDimensions, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

// sessionUsageTable is the engine's rows in a person's words. A missing sum is "-".
func sessionUsageTable(by string, daily bool, items []any, names map[string]string) termrender.Table {
	table := termrender.Table{Header: []string{by}, Empty: "No sessions in that time."}
	if daily {
		table.Header = []string{"day", by}
	}
	table.Header = append(table.Header, "runs", "tokens in", "tokens out", "cost", "no tokens", "no cost")
	count := func(row map[string]any, key string) string {
		if n, ok := intField(row, key); ok {
			return fmt.Sprint(n)
		}
		return "-"
	}
	for _, it := range items {
		row, _ := it.(map[string]any)
		key := termSafe(str(row, "key"))
		if name := names[key]; name != "" {
			key = termSafe(name)
		}
		if key == "" {
			key = "(none)"
		}
		cost := "-"
		if micro, ok := intField(row, "cost_micro_usd"); ok {
			cost = fmt.Sprintf("$%.4f", float64(micro)/1_000_000)
		}
		cells := []string{key}
		if daily {
			cells = []string{termSafe(str(row, "day")), key}
		}
		table.Rows = append(table.Rows, append(cells, count(row, "runs"), count(row, "input_tokens"), count(row, "output_tokens"),
			cost, count(row, "runs_without_tokens"), count(row, "runs_without_cost")))
	}
	return table
}

// accountNames maps account refs to their names for a table. It is best effort, one page:
// a caller who may not list accounts, or an account beyond the page, shows the ref.
func (c *agentClientConfig) accountNames(ctx context.Context) map[string]string {
	_, raw, err := c.do(ctx, "GET", providerAccountsPath+"?"+url.Values{"limit": {fmt.Sprint(accountNameLimit)}}.Encode(), nil)
	if err != nil {
		return nil
	}
	var page struct {
		Items []struct {
			Ref  string `json:"account_ref"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &page) != nil {
		return nil
	}
	names := make(map[string]string, len(page.Items))
	for _, a := range page.Items {
		names[a.Ref] = a.Name
	}
	return names
}
