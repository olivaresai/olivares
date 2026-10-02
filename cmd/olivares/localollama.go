// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"

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
// tenant the administrator started it from (Root on FH 033). Sharing a server resource
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
		// The record's model list is what an OpenCode launch on it may use (FH 087): it is
		// read here, when Ollama starts and after each model download, so nobody has to
		// test the provider in Providers first. A failed read is recorded like a failed
		// test in Providers: the record keeps the probe's state and reason and its model
		// list is cleared, so an OpenCode launch on it is refused until a read succeeds.
		_, _ = rt.TestProviderRecord(ctx, tenant, ref)
		return nil
	}
}

// confinedOllamaCommand confines the local Ollama like a session child: it may write
// its models and its home, read its release, and never reach the engine's data,
// configuration or the CLI credentials of the engine's user.
func confinedOllamaCommand(dataDir string) func(context.Context, []string, []string, string, ...string) (*exec.Cmd, error) {
	protect := sessionProtectPaths(dataDir)
	return func(ctx context.Context, rw, ro []string, program string, args ...string) (*exec.Cmd, error) {
		cmd, _, err := confine.Command(ctx, confine.Policy{ReadWrite: rw, ReadOnly: ro, Protect: protect}, program, args...)
		return cmd, err
	}
}
