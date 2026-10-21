// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// loopbackEndpoint is a plain-http endpoint on this host itself.
func loopbackEndpoint(base string) bool {
	if !driverfacts.LocalHTTPEndpoint(base) {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return false
	}
	if u.Hostname() == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(u.Hostname())
	return err == nil && ip.IsLoopback()
}

// registerLocalModelServer adds a llama.cpp or vLLM server this engine runs (agenttoolsapi
// modelserver.go) as an openai_compatible provider in the ONE tenant the administrator started
// it from, like the local Ollama (registerLocalOllama), holding the key the engine made for
// this start. A server that starts again has a new key and perhaps another port; the record
// of the same name takes both, so no second record appears and every profile bound to it keeps
// working. The record's models are read here, so an OpenCode launch on it needs no hand test.
func registerLocalModelServer(rt *sessions.Module, st store.Store) func(context.Context, auth.Principal, model.TenantID, string, string, string) error {
	return func(ctx context.Context, actor auth.Principal, tenant model.TenantID, name, endpoint, key string) error {
		if rt == nil || st == nil {
			return errors.New("the sessions module is not available on this node")
		}
		served, err := servesTenant(ctx, st, tenant)
		if err != nil {
			return err
		}
		if !served {
			return errors.New("that organization is not served by this node")
		}
		records, _, err := rt.ListProviderRecords(ctx, tenant, sessions.ProviderRecordActive, sessions.ProviderKindOpenAICompatible, model.Query{Limit: 200})
		if err != nil {
			return err
		}
		ref := ""
		for _, rec := range records {
			// Only the record of the same name is ours: a person's own record that happens to
			// sit at this address keeps its key.
			if rec.DisplayName != name {
				continue
			}
			// It follows the server to a new address, but only if it is a record this host's
			// own server could have made (loopback): a provider a person named like this and
			// pointed at a gateway or a machine on the network is theirs, and its key is not
			// ours to replace.
			if !loopbackEndpoint(rec.BaseURL) {
				return errors.New("a provider named " + name + " already exists at another address; rename it, then start the server again")
			}
			ref = rec.Ref
			break
		}
		if ref == "" {
			rec, err := rt.CreateProviderRecord(ctx, tenant, sessions.CreateProviderRecordInput{
				Kind: sessions.ProviderKindOpenAICompatible, DisplayName: name, BaseURL: endpoint, APIKey: key, Actor: actor,
			})
			if err != nil {
				return err
			}
			ref = rec.Ref
		} else if _, err := rt.PatchProviderRecord(ctx, tenant, ref, sessions.ProviderRecordPatch{BaseURL: &endpoint, APIKey: &key, Actor: actor}); err != nil {
			return err
		}
		// The test reads the server's models into the record. It is also recorded on the record
		// like a test in Providers, but a record that lists no model would only fail the first
		// OpenCode launch later, so the engine says so now.
		tested, err := rt.TestProviderRecord(ctx, tenant, ref)
		if err != nil {
			return fmt.Errorf("the connection test could not run: %w", err)
		}
		if tested.ProbeState != sessions.ProbeOK {
			return fmt.Errorf("the connection test did not succeed (%s): %s", tested.ProbeState, tested.ProbeDetail)
		}
		return nil
	}
}
