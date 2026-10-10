// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// toolProvider is one snapshot of GET /v1/m/agenttools/providers: what a tool itself
// reports about one login on the engine's host.
type toolProvider struct {
	Driver      string `json:"driver"`
	Default     bool   `json:"default"`
	State       string `json:"state"`
	Version     string `json:"version"`
	AuthMethod  string `json:"auth_method"`
	Plan        string `json:"plan"`
	Email       string `json:"email"`
	NextCommand string `json:"next_command"`
	Limits      []struct {
		Label    string     `json:"label"`
		Percent  float64    `json:"percent"`
		ResetsAt *time.Time `json:"resets_at"`
		Severity string     `json:"severity"`
	} `json:"limits"`
	Models []struct {
		ID string `json:"id"`
	} `json:"models"`
	Notes     []string  `json:"notes"`
	CheckedAt time.Time `json:"checked_at"`
	Source    string    `json:"source"`
	Stale     bool      `json:"stale"`
	Error     string    `json:"error"`
}

const providerModelsShown = 6

func newToolProvidersCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:   "providers",
		Short: "Show each tool's own login: email, plan, usage windows and models",
		Long: "providers asks the engine what each agent tool itself reports about its login on the\n" +
			"engine's host: the sign-in state, the account email (masked), the plan, the usage windows\n" +
			"with their reset time (Claude Code and Codex on a subscription), the version and the\n" +
			"models. The default login of the user running the engine is listed first; a login made\n" +
			"through `olivares tool login` is listed as its own entry. The engine asks each tool at\n" +
			"most once a minute; a failed refresh shows the last answer, marked stale, with the\n" +
			"command that failed. Olivares never opens a credential file and never signs in here:\n" +
			"a tool that is not signed in shows its own login command.",
		Example: "  olivares tool providers\n  olivares tool providers -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.resolve(); err != nil {
				return err
			}
			path := agentToolsPath + "/providers"
			if cfg.tenant != "" {
				path += "?tenant_id=" + url.QueryEscape(cfg.tenant)
			}
			status, b, err := cfg.do(cmd.Context(), "GET", path, nil, http.StatusOK, http.StatusNotFound)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return toolHTTPErr(status, b)
			}
			var out struct {
				Providers []toolProvider `json:"providers"`
			}
			if err := json.Unmarshal(b, &out); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				renderToolProviders(renderTo(w), out.Providers, time.Now())
				return nil
			}, json.RawMessage(b))
		},
	}
	cfg.addFlags(cmd)
	return cmd
}

func renderToolProviders(rr *termrender.Renderer, providers []toolProvider, now time.Time) {
	for i, p := range providers {
		if i > 0 {
			rr.Blank()
		}
		title := toolName(p.Driver) + " (own login)"
		if !p.Default {
			title = toolName(p.Driver) + " (signed in through Olivares)"
		}
		rr.Line(strings.TrimSpace(title + "  " + p.Version))
		role, text := termrender.RoleOK, ""
		switch p.State {
		case "ready":
			text = "signed in"
			if p.Email != "" {
				text += " as " + p.Email
			}
			for _, part := range []string{p.Plan, p.AuthMethod} {
				if part != "" {
					text += " · " + part
				}
			}
		case "not_signed_in":
			role, text = termrender.RoleWarn, "not signed in. Run: "+p.NextCommand
		case "not_installed":
			role, text = termrender.RoleMuted, "not installed. Run: "+p.NextCommand
		case "unknown":
			role, text = termrender.RoleMuted, "sign-in state not reported by the tool"
		default:
			role, text = termrender.RoleFail, "could not be read: "+p.Error
		}
		rr.Line("  " + rr.Paint(text, role))
		if p.Stale {
			rr.Line("  " + rr.Paint("stale, the last answer is shown. "+p.Error, termrender.RoleWarn))
		}
		for _, l := range p.Limits {
			role := termrender.RoleNone
			switch {
			case l.Percent >= 100:
				role = termrender.RoleFail
			case l.Percent >= 90 || l.Severity == "warning":
				role = termrender.RoleWarn
			}
			line := fmt.Sprintf("  %-16s %s%% used", l.Label, strconv.FormatFloat(l.Percent, 'f', -1, 64))
			if l.ResetsAt != nil {
				line += ", " + untilReset(*l.ResetsAt, now)
			}
			rr.Line(rr.Paint(line, role))
		}
		if n := len(p.Models); n > 0 {
			ids := make([]string, 0, providerModelsShown+1)
			for j, m := range p.Models {
				if j == providerModelsShown {
					ids = append(ids, fmt.Sprintf("+%d more (-o json)", n-j))
					break
				}
				ids = append(ids, m.ID)
			}
			rr.Line("  models: " + strings.Join(ids, ", "))
		}
		for _, note := range p.Notes {
			rr.Line("  " + rr.Paint(note, termrender.RoleMuted))
		}
		if p.State == "not_signed_in" || p.State == "not_installed" {
			continue
		}
		rr.Line("  " + rr.Paint("checked "+p.CheckedAt.Local().Format("15:04:05")+" · "+p.Source, termrender.RoleMuted))
	}
}

// untilReset says when a usage window resets, relative to now.
func untilReset(at, now time.Time) string {
	d := at.Sub(now)
	switch {
	case d <= 0:
		return "reset time passed"
	case d >= 48*time.Hour:
		return fmt.Sprintf("resets in %d d %d h", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("resets in %d h %d min", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("resets in %d min", max(1, int(d.Minutes())))
}
