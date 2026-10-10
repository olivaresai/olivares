// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/modules/sessions"
	sessionegress "github.com/olivaresai/olivares/modules/sessions/egress"
)

func (m *mcpManagement) localEnvironment(ctx context.Context, tenant model.TenantID, row auth.MCPGatewayServer) ([]sessions.EnvVar, []string, error) {
	env := []sessions.EnvVar{}
	patterns := []string{}
	keys := []string{}
	for key := range row.Env {
		keys = append(keys, key)
	}
	for key := range row.EnvSecretRefs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := row.Env[key]
		if reference := row.EnvSecretRefs[key]; reference != "" {
			ref, ok := secret.ParseReference(reference)
			if !ok || m.secrets == nil || ref.Scheme != secret.SchemeStore || reference != "store:"+ref.Locator || !strings.HasPrefix(ref.Locator, "mcp/") {
				return nil, nil, auth.ErrSecretNotFound
			}
			raw, err := m.secrets.Resolve(ctx, tenant, ref.Locator)
			if err != nil {
				return nil, nil, auth.ErrSecretNotFound
			}
			value = string(raw)
			if len(value) < 8 || len(value) > 8192 || strings.ContainsRune(value, 0) {
				return nil, nil, auth.ErrMCPGatewayInvalid
			}
			patterns = append(patterns, value)
		}
		env = append(env, sessions.EnvVar{Name: key, Value: value})
	}
	return env, patterns, nil
}

func (m *mcpManagement) probeLocalServer(ctx context.Context, tenant model.TenantID, row auth.MCPGatewayServer) ([]mcpc.Tool, error) {
	env, patterns, err := m.localEnvironment(ctx, tenant, row)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "olivares-mcp-test-")
	if err != nil {
		return nil, auth.ErrMCPGatewayUnavailable
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	spec := sessions.LaunchSpec{Program: row.Command, Args: row.Args, Dir: dir, Isolation: sessions.IsolationNative, WaitDelay: time.Second, Env: env}
	if err := m.confineLocalServer(&spec, row.EgressHosts...); err != nil {
		return nil, newManagedStdioFailure("process_start", err, patterns)
	}
	if m.eng != nil && m.eng.sessionsMod != nil {
		policy, err := m.eng.sessionsMod.MCPProbePolicy(ctx, tenant, dir, filepath.Dir(spec.Program), row.Args)
		if err != nil {
			return nil, newManagedStdioFailure("process_start", err, patterns)
		}
		if policy != nil {
			// Registry discovery cannot reopen a protected or broad directory.
			readOnly := append([]string{}, spec.Confinement.ReadOnly...)
			for _, path := range policy.ReadOnly {
				if m.allowedMCPCodePath(&spec, path, true) {
					readOnly = append(readOnly, path)
				}
			}
			policy.ReadOnly = readOnly
			spec.Confinement = policy
		}
	}
	client, err := launchManagedStdio(ctx, sessions.NewProcRunner(), spec, patterns)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	if err := client.Initialize(ctx); err != nil {
		return nil, err
	}
	return client.ListTools(ctx)
}

// The agent's resolved launch grants the writable paths. Configured script and
// file arguments add exact read/execute grants, never their parent directory.
// Named code directories are read-only; the session folder is the only writer.
// Egress hosts replace the launch's network with exactly those HTTPS hosts, at
// public addresses only; with none, the command keeps the network of its launch.
func (m *mcpManagement) confineLocalServer(spec *sessions.LaunchSpec, egressHosts ...string) error {
	spec.ConfinementRequired = true
	if len(egressHosts) > 0 {
		spec.NetworkPolicy = &sessionegress.Policy{PublicOnly: true}
		for _, host := range egressHosts {
			spec.NetworkPolicy.Providers = append(spec.NetworkPolicy.Providers, "https://"+host)
		}
	}
	// Each MCP child gets its own HOME and TMPDIR from the confined runner.
	// Never reuse the agent's home or the user's project for package caches.
	env := make([]sessions.EnvVar, 0, len(spec.Env))
	for _, item := range spec.Env {
		switch item.Name {
		case "HOME", "TMPDIR", "TMP", "TEMP":
			continue
		}
		env = append(env, item)
	}
	spec.Env = env
	program := spec.Program
	if !filepath.IsAbs(program) && strings.ContainsRune(program, filepath.Separator) {
		program = filepath.Join(spec.Dir, program)
	}
	resolved, err := exec.LookPath(program)
	if err != nil {
		return newManagedStdioFailure("process_start", err, nil)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return newManagedStdioFailure("process_start", err, nil)
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return newManagedStdioFailure("process_start", err, nil)
	}
	spec.Program = resolved
	if m.eng == nil || m.eng.sessionsMod == nil {
		return errMCPEgressUnconfined(spec)
	}
	rw := []string{spec.Dir}
	ro := []string{resolved}
	spec.Confinement = m.eng.sessionsMod.ConfinementPolicy(rw, ro)
	if spec.Confinement == nil {
		return errMCPEgressUnconfined(spec)
	}
	// The runner also reads the executable's install directory. Refuse a
	// command placed directly in a broad/protected directory before spawning.
	if !m.allowedMCPCodePath(spec, resolved, false) || !m.allowedMCPCodePath(spec, filepath.Dir(resolved), true) {
		return newManagedStdioFailure("process_start", &os.PathError{Op: "execute configured MCP command", Path: resolved, Err: os.ErrPermission}, nil)
	}
	for _, arg := range spec.Args {
		path := arg
		if strings.HasPrefix(path, "-") {
			var ok bool
			_, path, ok = strings.Cut(path, "=")
			if !ok {
				continue
			}
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(spec.Dir, path)
		}
		path, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue // package names, flags and nonexistent files grant nothing
		}
		info, err := os.Stat(path)
		if err != nil || (!info.Mode().IsRegular() && !info.IsDir()) {
			continue
		}
		if !m.allowedMCPCodePath(spec, path, info.IsDir()) {
			if info.IsDir() {
				continue
			}
			return newManagedStdioFailure("process_start", &os.PathError{Op: "read configured MCP argument", Path: path, Err: os.ErrPermission}, nil)
		}
		spec.Confinement.ReadOnly = append(spec.Confinement.ReadOnly, path)
	}
	return nil
}

// The network boundary runs inside the filesystem confinement helper. Without
// it an egress profile cannot hold, so the command does not start.
func errMCPEgressUnconfined(spec *sessions.LaunchSpec) error {
	if spec.NetworkPolicy == nil {
		return nil
	}
	return newManagedStdioFailure("process_start", errors.New("egress hosts need process confinement (Landlock) on the engine host"), nil)
}

func (m *mcpManagement) allowedMCPCodePath(spec *sessions.LaunchSpec, path string, directory bool) bool {
	if directory {
		home, _ := os.UserHomeDir()
		if real, err := filepath.EvalSymlinks(home); err == nil {
			home = real
		}
		if path == string(filepath.Separator) || (home != "" && mcpPathContains(path, home)) || (path != spec.Dir && mcpPathContains(path, spec.Dir)) {
			return false
		}
	}
	for _, protected := range spec.Confinement.Protect {
		if !mcpPathContains(protected, path) {
			continue
		}
		// No inherited account/config grants. Only the already scoped folder
		// and product-managed code below data/tools may cross the data carve-out.
		if m.eng.dataDir != "" && mcpPathContains(protected, m.eng.dataDir) &&
			(mcpPathContains(spec.Dir, path) || mcpPathContains(filepath.Join(m.eng.dataDir, "tools"), path)) {
			continue
		}
		return false
	}
	return true
}

func mcpPathContains(root, path string) bool {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
