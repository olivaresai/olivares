// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

// root_status.go is what `olivares` alone prints: where the engine is, who is
// signed in, the one next step, and the commands to start with. Measured
// 2026-10-01: it printed the full help, 80 commands, and said nothing about
// this installation.

// rootProbeTimeout bounds each of the few reads the status makes.
const rootProbeTimeout = 2 * time.Second

type rootStatusReport struct {
	Engine    string `json:"engine,omitempty"`
	Reachable bool   `json:"reachable"`
	// Reason is why the engine did not answer the probe, in a few words: an engine
	// that is down is not one this computer cannot trust, and Next names the command for each.
	Reason   string `json:"reason,omitempty"`
	Context  string `json:"context,omitempty"`
	SignedIn string `json:"signed_in_as,omitempty"`
	Next     string `json:"next"`
	// Refusal is the readiness sentence of a key its provider refused, when no tool is
	// ready: the cause the next step alone does not name.
	Refusal string `json:"refusal,omitempty"`
}

// engineState is how the engine is described next to its address: running, the
// reason the probe failed, or "not answering" when it gave none.
func (r rootStatusReport) engineState() string {
	switch {
	case r.Reachable:
		return "running"
	case r.Reason != "":
		return r.Reason
	}
	return "not answering"
}

func runRootStatus(cmd *cobra.Command) error {
	report := rootStatus(cmd.Context())
	return renderOut(cmd, func(w io.Writer) error {
		r := renderTo(w)
		fmt.Fprintf(w, "Olivares AI %s\n\n", version)
		engine := "none found on this machine"
		if report.Engine != "" {
			engine = report.Engine + " (" + report.engineState() + ")"
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
		if report.Engine == "" {
			// Next stays quickstart (the menu runs it); a computer that only runs the CLI
			// signs in to an engine elsewhere.
			r.Line("To use an engine on another computer: olivares login --server <url>")
		}
		r.Blank()
		r.Line("Start here:")
		root := cmd.Root()
		for _, c := range root.Commands() {
			// ui is named by the hint line below, not repeated here: the bare
			// command's only allowed change on its own is that one line.
			if c.GroupID == "start" && c.IsAvailableCommand() && c.Name() != "ui" {
				fmt.Fprintf(w, "  %-10s %s\n", c.Name(), c.Short)
			}
		}
		r.Blank()
		r.Line("All commands: olivares --help")
		// The hint advertises exactly the check `olivares ui` makes: both
		// streams a terminal, so the menu it names cannot then refuse.
		if in, ok := cmd.InOrStdin().(*os.File); ok {
			if out, ok := w.(*os.File); ok && tui.CheckTerminal(in, out) == nil {
				r.Line("Interactive: olivares ui")
			}
		}
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
	get := func(path string, withToken bool, into any) (int, error) {
		return engineGet(ctx, resolved, path, withToken, into, rootProbeTimeout)
	}
	status, probeErr := get("/livez", false, nil)
	report.Reachable = probeErr == nil && status == http.StatusOK
	switch {
	case !report.Reachable:
		report.Reason, report.Next = probeProblem(resolved.Server, status, probeErr)
		return report
	case resolved.Token == "":
		report.Next = "olivares login"
		return report
	}
	var who authWhoamiResponse
	// Whatever the reason, a saved sign-in the engine does not take is mended by signing in.
	if status, _ := get("/v1/auth/whoami", true, &who); status != http.StatusOK {
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

// engineGet reads one path of the engine the context names, with the saved credential
// only when withToken, and decodes a 200 body into into when it is not nil. It returns
// the HTTP status, or the transport error when the engine gave none.
func engineGet(ctx context.Context, resolved cliResolvedConfig, path string, withToken bool, into any, timeout time.Duration) (int, error) {
	if !withToken {
		resolved.Token, resolved.Tenant = "", ""
	}
	client, headers, err := cliTransport(cliTransportOptions{Resolved: resolved, Timeout: timeout, Stderr: io.Discard})
	if err != nil {
		return 0, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, resolved.Server+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header = headers
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if into != nil && resp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(into)
	}
	return resp.StatusCode, nil
}

// dockerEngineNext is the documented way to the engine a container runs: the CLI
// inside it has the CA in its own data volume (INSTALL.md, `docker exec olivares ...`).
// ponytail: assumes the documented container name, and any loopback engine this host
// did not record gets it; upgrade when the engine records its container for the host.
const dockerEngineNext = "docker exec -it olivares olivares ui"

// probeProblem says why the engine's liveness probe failed and which command fixes
// that case. status is the HTTP status when the engine answered with one, err the
// transport error when it did not.
func probeProblem(server string, status int, err error) (reason, next string) {
	next = "olivares doctor"
	if err == nil {
		return fmt.Sprintf("answers HTTP %d", status), next
	}
	cause, _ := classifyTransport(err)
	var netErr net.Error
	switch {
	case cause == transportHostname:
		return "certificate does not cover this address", next
	case cause == transportUntrusted && loopbackWithoutLocalEngine(server):
		// The CA of an engine on this host's loopback that this host did not record is
		// in a container's data volume.
		return "certificate not trusted", dockerEngineNext
	case cause == transportUntrusted:
		return "certificate not trusted", "olivares login"
	case cause == transportRefused:
		return "connection refused", next
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return fmt.Sprintf("no answer in %d s", int(rootProbeTimeout/time.Second)), next
	}
	return "cannot connect: " + shortLine(err.Error()), next
}

// loopbackWithoutLocalEngine is whether server is an address on this computer that no
// engine of this host's data directory recorded.
func loopbackWithoutLocalEngine(server string) bool {
	u, err := url.Parse(server)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host != "localhost" && !net.ParseIP(host).IsLoopback() {
		return false
	}
	origin, _ := localEngine()
	return origin == "" || !sameLocalOrigin(server, origin)
}

// shortLine is s as one row of at most 60 characters.
func shortLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}

// rootNextFromTools picks the next step from the engine's tool rows, in the order
// the console uses: the first installed tool in sessionToolOrder, then
// its sign-in. A signed-in Codex with no Claude Code is a working path, so the next
// step is a Codex session, not "install Claude Code" (measured 2026-10-01: the two disagreed).
func rootNextFromTools(rows []toolRow) string {
	_, next := nextToolStep(rows)
	return next
}

// nextToolStep is rootNextFromTools with the tool the step is about, for the
// screen that puts its cursor on that tool's row.
func nextToolStep(rows []toolRow) (driver, command string) {
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
			return driver, "olivares tool login " + driver
		}
		if driver == "claude" {
			return driver, "olivares session start <folder>"
		}
		return driver, "olivares session start <folder> --tool " + driver
	}
	return "claude", "olivares tool install claude"
}
