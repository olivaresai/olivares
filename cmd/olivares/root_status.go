// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// root_status.go is what `olivares` alone prints: where the engine is, who is
// signed in, the one next step, and the commands to start with. Measured
// 2026-10-01 (HU-29): it printed the full help, 80 commands, and said nothing about
// this installation.

// rootProbeTimeout bounds each of the few reads the status makes.
const rootProbeTimeout = 2 * time.Second

type rootStatusReport struct {
	Engine    string `json:"engine,omitempty"`
	Reachable bool   `json:"reachable"`
	Context   string `json:"context,omitempty"`
	SignedIn  string `json:"signed_in_as,omitempty"`
	Next      string `json:"next"`
	// Refusal is the readiness sentence of a key its provider refused, when no tool is
	// ready: the cause the next step alone does not name.
	Refusal string `json:"refusal,omitempty"`
}

func runRootStatus(cmd *cobra.Command) error {
	report := rootStatus(cmd.Context())
	return renderOut(cmd, func(w io.Writer) error {
		r := renderTo(w)
		fmt.Fprintf(w, "Olivares AI %s\n\n", version)
		engine := "none found on this machine"
		switch {
		case report.Engine != "" && report.Reachable:
			engine = report.Engine + " (running)"
		case report.Engine != "":
			engine = report.Engine + " (not answering)"
		}
		signed := "no"
		if report.SignedIn != "" {
			signed = report.SignedIn
			if report.Context != "" {
				signed += " (context " + report.Context + ")"
			}
		}
		r.Fields([]termrender.Field{
			{Key: "engine", Value: engine},
			{Key: "signed in", Value: signed},
			{Key: "next", Value: report.Next},
		})
		if report.Refusal != "" {
			r.Line(report.Refusal)
		}
		r.Blank()
		r.Line("Start here:")
		root := cmd.Root()
		for _, c := range root.Commands() {
			if c.GroupID == "start" && c.IsAvailableCommand() {
				fmt.Fprintf(w, "  %-10s %s\n", c.Name(), c.Short)
			}
		}
		r.Blank()
		r.Line("All commands: olivares --help")
		return nil
	}, report)
}

// rootStatus reads the active context (or the engine this host recorded), asks the
// engine whether it is up and who the saved credential is, and picks the next step.
func rootStatus(ctx context.Context) rootStatusReport {
	var report rootStatusReport
	resolved, err := resolveCLIConfig(cliResolutionOptions{})
	if err != nil {
		resolved = cliResolvedConfig{}
	}
	if resolved.Server == "" {
		resolved.Server, resolved.CACert = localEngine()
		resolved.Token, resolved.Tenant = "", ""
	} else {
		report.Context = resolved.ContextName
	}
	report.Engine = resolved.Server
	if report.Engine == "" {
		report.Next = "olivares quickstart"
		return report
	}
	get := func(path string, withToken bool, into any) int {
		r := resolved
		if !withToken {
			r.Token, r.Tenant = "", ""
		}
		client, headers, err := cliTransport(cliTransportOptions{Resolved: r, Timeout: rootProbeTimeout, Stderr: io.Discard})
		if err != nil {
			return 0
		}
		cctx, cancel := context.WithTimeout(ctx, rootProbeTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, r.Server+path, nil)
		if err != nil {
			return 0
		}
		req.Header = headers
		resp, err := client.Do(req)
		if err != nil {
			return 0
		}
		defer func() { _ = resp.Body.Close() }()
		if into != nil && resp.StatusCode == http.StatusOK {
			_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(into)
		}
		return resp.StatusCode
	}
	report.Reachable = get("/livez", false, nil) == http.StatusOK
	switch {
	case !report.Reachable:
		report.Next = "olivares doctor"
		return report
	case resolved.Token == "":
		report.Next = "olivares login"
		return report
	}
	var who authWhoamiResponse
	if get("/v1/auth/whoami", true, &who) != http.StatusOK {
		report.Next = "olivares login"
		return report
	}
	report.SignedIn = firstNonEmptyCLI(who.DisplayName, who.Actor, who.UserID)
	report.Next = "olivares session start <folder>"
	cfg := agentClientConfig{resolved: resolved, server: resolved.Server, token: resolved.Token,
		tenant: resolved.Tenant, timeout: rootProbeTimeout}
	// A tool that can run a session now (its own login, a key or a local model) makes
	// the session the next step, as `session start` would pick it; only with none ready
	// do the tool rows say what to install or sign in.
	ready, refused := cfg.firstReadyTool(ctx)
	if ready != "" {
		return report
	}
	report.Refusal = refused
	if rows, err := cfg.toolRows(ctx); err == nil {
		report.Next = rootNextFromTools(rows)
	}
	return report
}

// rootNextFromTools picks the next step from the engine's tool rows, in the order
// the console uses: the first installed tool in sessionToolOrder, then
// its sign-in. A signed-in Codex with no Claude Code is a working path, so the next
// step is a Codex session, not "install Claude Code" (measured 2026-10-01: the two disagreed).
func rootNextFromTools(rows []toolRow) string {
	byDriver := map[string]toolRow{}
	for _, row := range rows {
		byDriver[row.Driver] = row
	}
	for _, driver := range sessionToolOrder {
		row, ok := byDriver[driver]
		if !ok || !row.Installed {
			continue
		}
		if row.SignedIn != nil && !*row.SignedIn {
			return "olivares tool login " + driver
		}
		if driver == "claude" {
			return "olivares session start <folder>"
		}
		return "olivares session start <folder> --tool " + driver
	}
	return "olivares tool install claude"
}
