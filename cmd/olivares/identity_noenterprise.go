//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	claudewif "github.com/olivaresai/olivares/connectors/claude-wif"
	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	executor "github.com/olivaresai/olivares/core/runtime/executor"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk"
	"github.com/spf13/cobra"
)

var errIdentityRequiresBusiness = errors.New("directory rosters and agent federation require Business Identity & Scale")

func buildRosterProvider(string) (identitysource.GraphProvider, sdk.SourceConnector, bool) {
	return nil, nil, false
}
func wireRoster(context.Context, *runtime.Runtime, *governance.Module, *wifGraphAdapter, sourcesConfig, *secret.Resolver, *slog.Logger) {
}
func validateIdentitySourcesConfig(cfg sourcesConfig) error {
	if len(cfg.Identity) > 0 {
		return errIdentityRequiresBusiness
	}
	for _, src := range cfg.Sources {
		if src.Plugin != nil {
			continue
		}
		switch src.Kind {
		case "ldap", "idp", "okta", "entra", "vault", "infisical":
			return errIdentityRequiresBusiness
		}
	}
	return nil
}
func newIdentityPostureProvider(func(string) string, *slog.Logger) governance.IdentityPostureProvider {
	return nil
}

type wifGraphAdapter struct{}

func newWifGraphAdapter(*slog.Logger) *wifGraphAdapter { return &wifGraphAdapter{} }
func (*wifGraphAdapter) WifGraph(context.Context, model.TenantID) (claudewif.WIFGraph, bool) {
	return claudewif.WIFGraph{}, false
}
func (*wifGraphAdapter) FederationExchangeParams(model.TenantID) ([]claudewif.ExchangeParams, bool) {
	return nil, false
}

// Keep a nonnil broker so an opted-in WIF launch cannot fall back to a static token.
type wifCredentialBroker struct{}

func newWIFCredentialBroker(func(string) string, federationResolver, modelprovider.Doer, *slog.Logger) *wifCredentialBroker {
	return &wifCredentialBroker{}
}
func (*wifCredentialBroker) sessionSource(string) sessions.CredentialSource {
	return sessions.CredentialSourceFunc(func(context.Context, sessions.CredentialRequest) (sessions.Credential, error) {
		return sessions.Credential{}, errIdentityRequiresBusiness
	})
}
func (*wifCredentialBroker) executorSource(model.TenantID, string) executor.CredentialSource {
	return executor.MintFunc(func(context.Context, executor.MintRequest) (executor.Credential, error) {
		return executor.Credential{}, errIdentityRequiresBusiness
	})
}
func (*wifCredentialBroker) Close() error { return nil }
func unavailableIdentityCommand(name string, aliases ...string) *cobra.Command {
	short := "Identity management requires Business Identity & Scale"
	path := "olivares identity " + name
	switch name {
	case "nhi":
		short = "Agent identity lifecycle requires Business Identity & Scale"
		path = "olivares governance nhi"
	case "wif":
		short = "Workload identity federation requires Business Identity & Scale"
	case "external-keys":
		short = "External key inventory requires Business Identity & Scale"
	case "residency":
		short = "Residency posture requires Business Identity & Scale"
	}
	return &cobra.Command{
		Use: name, Aliases: aliases, Hidden: true, DisableFlagParsing: true,
		Short:   short,
		Long:    "This command is unavailable in this build. Directory rosters and agent federation require Business Identity & Scale.",
		Example: "  " + path + " # Reports the Business requirement",
		RunE: func(cmd *cobra.Command, args []string) error {
			// DisableFlagParsing passes --help to RunE. Help answers as it always did; only running the command is refused.
			if completionWantsHelp(args) {
				return cmd.Help()
			}
			return errIdentityRequiresBusiness
		},
	}
}
func editionIdentityCommands(*authClientFlags) []*cobra.Command {
	return []*cobra.Command{unavailableIdentityCommand("wif", "federation"), unavailableIdentityCommand("external-keys", "cmek"), unavailableIdentityCommand("residency")}
}

// Serving reports inactive Business protection without changing retained data; offline backup stays quiet.
func editionIdentityStartup(ctx context.Context, st store.Store) error {
	var orgs []model.Org
	if err := st.System(ctx, func(sys store.SystemScope) error { var err error; orgs, err = sys.ListOrgs(ctx); return err }); err != nil {
		return fmt.Errorf("verify stored Business identity protection: %w", err)
	}
	var caepEnabled, nhiProtected bool
	tenants := map[model.TenantID]bool{}
	for _, org := range orgs {
		tenants[org.TenantID] = true
	}
	for tenant := range tenants {
		if tenant.IsZero() || tenant.IsSystem() {
			continue
		}
		err := st.View(ctx, tenant, func(sc store.Scope) error {
			org, err := sc.Org(ctx)
			if err != nil {
				return err
			}
			required, err := governance.StoredNHIRequiresBusiness(ctx, sc)
			if err != nil {
				return err
			}
			nhiProtected = nhiProtected || required
			raw, present := org.Settings["caep_set"]
			if !present {
				return nil
			}
			body, err := json.Marshal(raw)
			if err != nil {
				return err
			}
			var cfg auth.CAEPSetConfig
			if err := json.Unmarshal(body, &cfg); err != nil {
				return err
			}
			caepEnabled = caepEnabled || cfg.Enabled
			return nil
		})
		if errors.Is(err, store.ErrResidencyViolation) {
			continue
		}
		if err != nil {
			return fmt.Errorf("verify stored Business identity protection: %w", err)
		}
	}
	var inactive []string
	if caepEnabled {
		inactive = append(inactive, "CAEP receiver")
	}
	if nhiProtected {
		inactive = append(inactive, "NHI rotation and escalation protection")
	}
	if len(inactive) > 0 {
		slog.WarnContext(ctx, strings.Join(inactive, "; ")+" inactive in Community: requires Business Identity & Scale; stored data is retained")
	}
	return nil
}

func governanceNHICmd(*authClientFlags) *cobra.Command { return unavailableIdentityCommand("nhi") }

func validateNHIActuatorsConfig(cfg nhiActuatorsConfig) error {
	if len(cfg.Tenants) > 0 {
		return errIdentityRequiresBusiness
	}
	return nil
}
func buildNHIActuatorBindings(nhiActuatorsConfig, *slog.Logger) []governance.LifecycleActuatorBinding {
	return nil
}
