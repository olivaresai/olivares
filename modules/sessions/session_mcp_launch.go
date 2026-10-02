// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// SessionMCPLaunchSource reads the existing registry and configures the one MCP
// endpoint after credentials/env are injected and before the agent is spawned.
// It mints no credential. Cleanup belongs to that exact launch's lifetime.
type SessionMCPLaunchSource interface {
	ConfigureSessionMCP(context.Context, model.TenantID, string, string, *LaunchSpec) (func(), error)
}

func (m *Module) UseSessionMCPLaunchSource(source SessionMCPLaunchSource) { m.rt.sessionMCP = source }

// ConfigureSessionMCP writes only public connection settings under run/runRef.
// Claude reads its generated JSON; Codex receives the equivalent mcp_servers
// config overrides without changing the account's config.toml. The generated
// TOML records those same overrides. Credentials are sourced from child env.
// Formats: code.claude.com/docs/en/mcp and
// learn.chatgpt.com/docs/config-file/config-reference (bearer_token_env_var).
func ConfigureSessionMCP(spec *LaunchSpec, driver, dataDir, runRef, endpoint, tokenEnv string) (func(), error) {
	noop := func() {}
	if spec == nil {
		return noop, errors.New("session MCP launch is unavailable")
	}
	if !filepath.IsAbs(dataDir) || runRef == "" || runRef == "." || runRef == ".." || filepath.Base(runRef) != runRef || strings.ContainsAny(runRef, "\\\r\n\x00") {
		return noop, errors.New("session MCP configuration path is invalid")
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/session/mcp" {
		return noop, errors.New("session MCP endpoint is invalid")
	}
	if tokenEnv != "OLIVARES_HOOK_PEP_TOKEN" && tokenEnv != "OLIVARES_WORK_TOKEN" {
		return noop, errors.New("session MCP credential source is invalid")
	}
	found := false
	for _, item := range spec.Env {
		if item.Name == tokenEnv && item.Value != "" {
			found = true
		}
	}
	if !found {
		return noop, errors.New("session MCP credential is unavailable")
	}
	if driver != "claude" && driver != "codex" {
		return noop, nil
	}
	dir := filepath.Join(dataDir, "run", runRef)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return noop, errors.New("session MCP configuration could not be created")
	}
	var raw []byte
	suffix := "json"
	if driver == "claude" {
		raw, _ = json.Marshal(map[string]any{"mcpServers": map[string]any{"olivares": map[string]any{"type": "http", "url": endpoint, "headers": map[string]string{"Authorization": "Bearer ${" + tokenEnv + "}"}}}})
	} else {
		suffix = "toml"
		raw = []byte("[mcp_servers.olivares]\nurl = " + strconv.Quote(endpoint) + "\nbearer_token_env_var = " + strconv.Quote(tokenEnv) + "\n")
	}
	file, err := os.CreateTemp(dir, "mcp-*."+suffix)
	if err != nil {
		return noop, errors.New("session MCP configuration could not be created")
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err = file.Write(raw); err != nil {
		_ = file.Close()
		cleanup()
		return noop, errors.New("session MCP configuration could not be written")
	}
	if err = file.Close(); err != nil {
		cleanup()
		return noop, errors.New("session MCP configuration could not be written")
	}
	spec.AllowRead(path)
	if driver == "claude" {
		spec.Args = append(spec.Args, "--mcp-config", path, "--strict-mcp-config")
	} else {
		spec.Args = append([]string{"-c", "mcp_servers.olivares.url=" + strconv.Quote(endpoint), "-c", "mcp_servers.olivares.bearer_token_env_var=" + strconv.Quote(tokenEnv)}, spec.Args...)
	}
	return cleanup, nil
}
