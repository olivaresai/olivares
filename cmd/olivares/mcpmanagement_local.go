// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
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
	if err := checkPinnedProgram(spec.Program, row.Pin); err != nil {
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
	// The server writes the folder too: it gets the same read-only Git hooks and configuration.
	spec.ConfinementMounts, spec.ConfinementMountsNote = m.eng.sessionsMod.GitMounts(spec.Dir)
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

// pinDeclaredTool resolves a pinned local server to the verified install of its
// catalog declaration (olivares tool install --from): the release's executable
// becomes the command and its SHA-256 the pin. A pin naming another digest or
// command than that install, or a release that is not installed, is refused.
// The stored pin sent back unchanged (disable, assign) is kept as stored, so a
// server whose install changed can still be turned off or reassigned.
func (m *mcpManagement) pinDeclaredTool(ctx context.Context, tenant model.TenantID, id string, in *auth.MCPGatewayServerInput) error {
	pin := in.Pin
	if pin == nil {
		return nil
	}
	if id != "" {
		snapshot, err := m.store.Get(ctx, tenant)
		if err != nil {
			return err
		}
		for _, row := range snapshot.Servers {
			if row.ID == id && row.Pin != nil && *row.Pin == *pin && (in.Command == "" || in.Command == row.Command) {
				in.Command = row.Command
				return nil
			}
		}
	}
	if m.eng == nil || m.eng.dataDir == "" {
		return auth.ErrMCPGatewayUnavailable
	}
	inv, err := toolInstallEngine(ctx).List(ctx, filepath.Join(absOrSame(m.eng.dataDir), "tools"))
	if err != nil {
		return fmt.Errorf("%w: %w", auth.ErrMCPGatewayUnavailable, err)
	}
	for _, release := range inv.Installed {
		if release.Driver != "declared-"+pin.Program || release.Version != pin.Version {
			continue
		}
		if release.State != toolinstall.StateInstalled || release.SHA256 == "" {
			return fmt.Errorf("%w: %s %s is %s here (%s); install it again with olivares tool install --from <url> --version %s", auth.ErrMCPGatewayInvalid, pin.Program, pin.Version, release.State, release.Reason, pin.Version)
		}
		if pin.SHA256 != "" && pin.SHA256 != release.SHA256 || in.Command != "" && in.Command != release.Executable {
			return fmt.Errorf("%w: the pin differs from the installed %s %s", auth.ErrMCPGatewayInvalid, pin.Program, pin.Version)
		}
		in.Command, pin.SHA256 = release.Executable, release.SHA256
		return nil
	}
	return fmt.Errorf("%w: %s %s is not installed here; install it with olivares tool install --from <url> --version %s", auth.ErrMCPGatewayInvalid, pin.Program, pin.Version, pin.Version)
}

// checkPinnedProgram refuses a launch whose resolved executable is not the one
// the server was pinned to.
// ponytail: hashes the executable, then the runner opens it again; verify the opened file when the runner can exec by descriptor.
func checkPinnedProgram(program string, pin *auth.MCPGatewayPin) error {
	if pin == nil {
		return nil
	}
	f, err := os.Open(program)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != pin.SHA256 {
		return fmt.Errorf("the pinned %s %s executable changed since it was pinned; reinstall it or pin the server again", pin.Program, pin.Version)
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
