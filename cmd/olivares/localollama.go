// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"

	"github.com/olivaresai/olivares/cmd/olivares/internal/agenttoolsapi"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// localOllamaName is the provider the Ollama this engine runs is registered as.
const localOllamaName = "Ollama on this server"

// registerLocalOllama adds the Ollama this engine runs (agenttoolsapi ollama.go) as a
// provider once it answers (HU-R17), at its loopback endpoint, with no key, in the ONE
// tenant the administrator started it from (Root on first-hour check). Sharing a server resource
// with every tenant would be a tenancy decision nobody made; another tenant adds the
// same endpoint in Providers, and the Ollama row shows its address. A start outside a
// served business tenant registers nothing. A tenant that already has an active Ollama
// provider at that endpoint, or one by that name, is left as it is.
func registerLocalOllama(rt *sessions.Module, st store.Store) func(context.Context, auth.Principal, model.TenantID, string) error {
	return func(ctx context.Context, actor auth.Principal, tenant model.TenantID, endpoint string) error {
		if rt == nil || st == nil {
			return errors.New("the sessions module is not available on this node")
		}
		served, err := servesTenant(ctx, st, tenant)
		if err != nil {
			return err
		}
		if !served {
			return errors.New("Ollama was started outside a business tenant; add its endpoint in Providers")
		}
		records, _, err := rt.ListProviderRecords(ctx, tenant, "active", sessions.ProviderKindOllama, model.Query{Limit: 200})
		if err != nil {
			return err
		}
		ref := ""
		for _, rec := range records {
			if strings.TrimRight(rec.BaseURL, "/") == endpoint {
				ref = rec.Ref
				break
			}
		}
		if ref == "" {
			rec, err := rt.CreateProviderRecord(ctx, tenant, sessions.CreateProviderRecordInput{
				Kind: sessions.ProviderKindOllama, DisplayName: localOllamaName, BaseURL: endpoint, Actor: actor,
			})
			if errors.Is(err, sessions.ErrProviderNameTaken) {
				return nil
			}
			if err != nil {
				return err
			}
			ref = rec.Ref
		}
		// The record's model list is what an OpenCode launch on it may use (first-hour check): it is
		// read here, when Ollama starts and after each model download, so nobody has to
		// test the provider in Providers first. A failed read is recorded like a failed
		// test in Providers: the record keeps the probe's state and reason and its model
		// list is cleared, so an OpenCode launch on it is refused until a read succeeds.
		tested, err := rt.TestProviderRecord(ctx, tenant, ref)
		if errors.Is(err, sessions.ErrNoProviderProbe) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("the connection test could not run: %w", err)
		}
		if tested.ProbeState != sessions.ProbeOK {
			return fmt.Errorf("the connection test did not succeed (%s): %s", tested.ProbeState, tested.ProbeDetail)
		}
		return nil
	}
}

// confinedToolCommand confines a child of the agent tools module (a tool's login,
// its status read, a provider probe, the local Ollama) like a session child: it may
// write what the module names, read its program, and never reach the engine's data,
// configuration or the CLI credentials of the engine's user, and a root engine's
// child holds no capability. A host that cannot confine runs it unconfined (boot
// warns), and so does a host that cannot keep the child away from the engine user's
// user manager (the first such child warns, once).
func confinedToolCommand(dataDir string) agenttoolsapi.ChildCommand {
	protect := sessionProtectPaths(dataDir)
	var warnUserManager sync.Once
	return func(ctx context.Context, rw, ro []string, program string, args ...string) (*exec.Cmd, error) {
		cmd, state, err := confine.Command(ctx, confine.Policy{ReadWrite: rw, ReadOnly: ro, Protect: protect}, program, args...)
		if err == nil && state.Mode == confine.ModeLandlock && confine.UserBusExposed(cmd) {
			warnUserManager.Do(func() {
				slog.Warn("agent tools: a tool's login, status read or probe can reach the engine user's user manager, which could start commands outside its confinement; this host gives it no user and mount namespace to hide it with; run olivares doctor (session-confinement)")
			})
		}
		return cmd, err
	}
}
