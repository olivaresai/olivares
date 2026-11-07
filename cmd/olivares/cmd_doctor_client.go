// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
)

// installShapeClient is a host that runs only the CLI: it holds no installation of its own
// and a saved sign-in names the engine it uses. Like the other shapes it is read
// off the filesystem and the saved sign-in, never a flag.
const installShapeClient = "client"

// doctorClientContext is the saved sign-in of a client host: it names an engine and this
// host has no data directory. A data directory that exists, or that cannot be examined,
// means an installation is being looked at and the local checks apply.
func doctorClientContext(deps doctorDeps, o doctorOptions) (cliResolvedConfig, bool) {
	if _, err := deps.stat(o.dataDir); !errors.Is(err, fs.ErrNotExist) {
		return cliResolvedConfig{}, false
	}
	resolved, err := resolveCLIConfig(cliResolutionOptions{})
	if err != nil || resolved.Server == "" {
		return cliResolvedConfig{}, false
	}
	return resolved, true
}

// doctorClientChecks is what a host that only runs the CLI needs: the engine it signs in
// to answers (over a certificate this computer trusts), that engine takes the saved
// sign-in, and its version is the CLI's. The local-install checks measure a posture this
// host never claimed, and the tools live on the engine's host, not here.
func doctorClientChecks(ctx context.Context, o doctorOptions, resolved cliResolvedConfig, build doctorCheck) []doctorCheck {
	engine := doctorClientEngine(ctx, o, resolved)
	reachable := engine.Status == "pass"
	checks := []doctorCheck{build, engine, doctorClientSignIn(ctx, o, resolved, reachable)}
	if reachable {
		checks = append(checks, doctorClientVersion(ctx, o, resolved))
	}
	return checks
}

// doctorClientEngine asks the engine's liveness endpoint through the saved context's own
// trust (its CA or pin), and says why a failure happened with the command that mends it.
func doctorClientEngine(ctx context.Context, o doctorOptions, resolved cliResolvedConfig) doctorCheck {
	c := doctorCheck{Name: "engine", Required: true}
	status, err := engineGet(ctx, resolved, "/livez", false, nil, o.timeout)
	if err == nil && status == http.StatusOK {
		c.Status, c.Detail = "pass", fmt.Sprintf("%s status=%d", resolved.Server, status)
		return c
	}
	reason, _ := probeProblem(resolved.Server, status, err)
	c.Status, c.Detail = "fail", resolved.Server+" "+reason
	login := "olivares login --server " + resolved.Server
	cause, _ := classifyTransport(err)
	switch cause {
	case transportHostname:
		c.Remediation = "the engine's certificate does not cover this address; use an address it covers, then sign in again: " + login
	case transportUntrusted:
		c.Remediation = "this computer does not trust the engine's certificate; sign in with the engine's tls.crt: " + login + " --ca-cert <file>"
	default:
		c.Remediation = "start the engine on its host, or sign in to the engine you mean: olivares login --server <url>"
	}
	return c
}

// doctorClientSignIn asks the engine who the saved credential is, the read `olivares`
// alone makes. With the engine not answering it cannot be judged, and is then not required,
// so the summary counts one open check for one failure.
func doctorClientSignIn(ctx context.Context, o doctorOptions, resolved cliResolvedConfig, reachable bool) doctorCheck {
	c := doctorCheck{Name: "sign-in", Required: true}
	signIn := "sign in: olivares login --server " + resolved.Server
	switch {
	case resolved.Token == "":
		c.Status, c.Detail, c.Remediation = "fail", "no saved sign-in for "+resolved.Server, signIn
		return c
	case !reachable:
		c.Status, c.Required, c.Detail = "unknown", false, "not checked: the engine did not answer"
		c.Remediation = "start the engine on its host, then rerun doctor"
		return c
	}
	var who authWhoamiResponse
	status, err := engineGet(ctx, resolved, "/v1/auth/whoami", true, &who, o.timeout)
	switch {
	case err == nil && status == http.StatusOK:
		c.Status = "pass"
		c.Detail = "signed in as " + termSafe(firstNonEmptyCLI(who.DisplayName, who.Actor, who.UserID))
		if resolved.ContextName != "" {
			c.Detail += " (context " + termSafe(resolved.ContextName) + ")"
		}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		c.Status, c.Detail, c.Remediation = "fail", "the engine does not take the saved sign-in", signIn
	default:
		c.Status, c.Detail = "fail", "the engine could not check the saved sign-in"
		c.Remediation = "see what the engine reports: olivares status"
	}
	return c
}

// doctorClientVersion compares the engine's version with this CLI's. A difference is a
// warning: the two can still work together, and the fix is to install the matching release.
func doctorClientVersion(ctx context.Context, o doctorOptions, resolved cliResolvedConfig) doctorCheck {
	c := doctorCheck{Name: "engine-version"}
	var info struct {
		Version string `json:"version"`
	}
	status, err := engineGet(ctx, resolved, "/v1/server-info", false, &info, o.timeout)
	switch {
	case err != nil || status != http.StatusOK || info.Version == "":
		c.Status, c.Detail = "unknown", "the engine did not report its version"
		c.Remediation = "start the engine on its host, then rerun doctor"
	case info.Version == version:
		c.Status, c.Detail = "pass", "the engine and this CLI are both "+version
	default:
		c.Status = "warn"
		c.Detail = "the engine is " + termSafe(info.Version) + ", this CLI is " + version
		c.Remediation = "run the same release on both ends and upgrade the older one, on the host that runs it: olivares upgrade"
	}
	return c
}
