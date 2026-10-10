// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	mp "github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk"
)

type configuredModelDiscovery struct {
	sessions       *sessions.Module
	catalogSources *sourceReconciler
}

func wireModelAvailability(set moduleSet, st store.Store, sources *sourceReconciler) {
	if set.models == nil {
		return
	}
	discovery := configuredModelDiscovery{sessions: set.sessions, catalogSources: sources}
	set.models.UseConnectorCatalogs(discovery.Catalogs)
	if set.sessions != nil {
		set.models.UseAvailabilitySource(discovery, func(ctx context.Context) ([]model.TenantID, error) { return servedWorkTenants(ctx, st) })
	}
}

// Catalogs reads the configured source roster on demand. It does not add models
// to availability or replace Gather's observation/lifecycle path.
func (d configuredModelDiscovery) Catalogs(ctx context.Context, tenant model.TenantID) ([]models.ConnectorCatalog, error) {
	out := []models.ConnectorCatalog{}
	if d.catalogSources == nil || tenant.IsZero() || tenant.IsSystem() {
		return out, nil
	}
	entries, err := d.catalogSources.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Tenant != tenant.String() || !entry.Enabled || entry.Plugin != nil {
			continue
		}
		if _, plugin := pluginBinaryForKind[entry.Kind]; plugin {
			continue
		}
		conn, ok := buildInProcSource(entry.Kind)
		if !ok {
			continue
		}
		provider, ok := conn.(mp.CatalogProvider)
		if !ok {
			continue
		}
		result := models.ConnectorCatalog{SourceRef: entry.Name, Kind: entry.Kind, Failed: true}
		if entry.Component == conn.Descriptor().Name && (entry.Status == "running" || entry.Status == "stopped") && d.catalogSourceApplied(entry) {
			cat, readErr := readConnectorCatalog(ctx, conn, provider, entry, d.catalogSources.resolver)
			if readErr == nil {
				// A replaced/deleted source cannot publish under its former context.
				current, listErr := d.catalogSources.ListSources(ctx)
				for _, now := range current {
					if listErr == nil && now.Name == entry.Name && now.ID == entry.ID && now.Enabled && now.Tenant == entry.Tenant && now.AppliedRevision == entry.AppliedRevision && connectorCatalogFingerprint(now) == connectorCatalogFingerprint(entry) && d.catalogSourceApplied(now) {
						result.Catalog, result.Failed = cat, false
						break
					}
				}
			}
		}
		out = append(out, result)
	}
	return out, nil
}

func connectorCatalogFingerprint(entry api.SourceRosterEntry) string {
	return fingerprintDef(model.SourceDef{Kind: entry.Kind, Tenant: entry.Tenant, PollSeconds: entry.PollSeconds, Config: entry.Config})
}

func (d configuredModelDiscovery) catalogSourceApplied(entry api.SourceRosterEntry) bool {
	d.catalogSources.mu.Lock()
	defer d.catalogSources.mu.Unlock()
	applied, ok := d.catalogSources.applied[entry.Name]
	return ok && applied.sourceID.String() == entry.ID && applied.fingerprint == connectorCatalogFingerprint(entry)
}

func readConnectorCatalog(ctx context.Context, conn sdk.SourceConnector, provider mp.CatalogProvider, entry api.SourceRosterEntry, resolver *secret.Resolver) (mp.Catalog, error) {
	refused := errors.New("connector catalog unavailable")
	cfg, err := resolveConfig(ctx, resolver, conn.Descriptor(), sdk.Config{Settings: entry.Config})
	if err != nil {
		return mp.Catalog{}, refused
	}
	if err := conn.Open(ctx, cfg); err != nil {
		return mp.Catalog{}, refused
	}
	defer conn.Close(ctx)
	cat, err := provider.Snapshot(ctx)
	if err != nil || len(cat.Models) > 1000 {
		return mp.Catalog{}, refused
	}
	for _, md := range cat.Models {
		if !validProviderModelID(md.Ref) {
			return mp.Catalog{}, refused
		}
	}
	// Only model metadata leaves this reader. Reject reflected credentials even
	// on a successful response; do not surface provider errors, URLs or key inventory.
	cat = mp.Catalog{Provider: mp.Provider{Ref: cat.Provider.Ref}, Models: cat.Models, CapturedAt: cat.CapturedAt}
	encoded, err := json.Marshal(cat)
	if err != nil {
		return mp.Catalog{}, refused
	}
	for key, value := range cfg.Settings {
		_, reference := secret.ParseReference(entry.Config[key])
		secretField := false
		for _, field := range conn.Descriptor().ConfigFields {
			if field.Key == key && field.Secret {
				secretField = true
			}
		}
		if value != "" && (reference || secretField) {
			quoted, _ := json.Marshal(value)
			if bytes.Contains(encoded, quoted[1:len(quoted)-1]) || strings.Contains(string(encoded), value) {
				return mp.Catalog{}, refused
			}
		}
	}
	return cat, nil
}

func modelSourceRevision(values ...string) string {
	encoded, _ := json.Marshal(values)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// Only metadata from the configured product/account home is inspected. No
// credential contents are read and no host or lane credential is borrowed.
func modelHomeRevision(userHome, configHome string) string {
	values := []string{userHome, configHome}
	for _, name := range []string{".credentials.json", "auth.json", "config.toml", "settings.json"} {
		info, err := os.Lstat(filepath.Join(configHome, name))
		if err == nil {
			values = append(values, name, info.ModTime().UTC().Format("2006-01-02T15:04:05.999999999Z"), strconv.FormatInt(info.Size(), 10))
		}
	}
	return modelSourceRevision(values...)
}

func (d configuredModelDiscovery) Sources(ctx context.Context, tenant model.TenantID) ([]models.AvailabilitySource, error) {
	var out []models.AvailabilitySource
	profileHomes := map[string]bool{}
	cursor := ""
	for {
		records, page, err := d.sessions.ListProviderRecords(ctx, tenant, sessions.ProviderRecordActive, "", model.Query{Limit: 500, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, rec := range records {
			out = append(out, models.AvailabilitySource{Ref: rec.Ref, ProviderRef: rec.Ref, ProviderKind: rec.Kind, Revision: modelSourceRevision(rec.Kind, rec.Service, rec.BaseURL, rec.SecretRef, strconv.FormatInt(rec.Version, 10))})
		}
		if !page.HasMore {
			break
		}
		cursor = page.Cursor
	}
	cursor = ""
	for {
		profiles, page, err := d.sessions.ListProfiles(ctx, tenant, sessions.ProfileActive, model.Query{Limit: 500, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, profile := range profiles {
			if profile.AuthSource != sessions.AuthSourceAccountHome || profile.EnvironmentRef != d.sessions.ExecutionEnvironmentRef() {
				continue
			}
			profileHomes[profile.ConfigHome] = true
			// A configured active account remains observable when its homes fail
			// validation. Discover refuses it before inspecting or launching there.
			revision := modelSourceRevision("unavailable", profile.UserHome, profile.ConfigHome, strconv.FormatInt(profile.Version, 10))
			if _, err := d.sessions.ModelProfileHomes(tenant, profile); err == nil {
				revision = modelHomeRevision(profile.UserHome, profile.ConfigHome)
			}
			out = append(out, models.AvailabilitySource{Ref: profile.Ref, AccountRef: profile.Ref, ProviderKind: toolModelProvider(profile.Driver), Driver: profile.Driver, Revision: revision})
		}
		if !page.HasMore {
			break
		}
		cursor = page.Cursor
	}
	for _, driver := range []string{"claude", "codex", "grok"} {
		userHome, configHome, ok := d.sessions.OwnToolLoginHomes(tenant, driver)
		if !ok {
			continue
		}
		if profileHomes[configHome] {
			continue
		}
		if _, err := d.sessions.OwnToolModelHome(tenant, driver); err != nil {
			continue
		}
		out = append(out, models.AvailabilitySource{Ref: "login:" + driver, ProviderKind: toolModelProvider(driver), Driver: driver, Revision: modelHomeRevision(userHome, configHome)})
	}
	return out, nil
}

func toolModelProvider(driver string) string {
	switch driver {
	case "claude":
		return sessions.ProviderKindAnthropic
	case "codex":
		return sessions.ProviderKindOpenAI
	case "grok":
		return sessions.ProviderKindXAI
	default:
		return driver
	}
}

func (d configuredModelDiscovery) Discover(ctx context.Context, tenant model.TenantID, source models.AvailabilitySource) ([]string, error) {
	var ids []string
	var err error
	switch {
	case source.ProviderRef != "":
		rec, getErr := d.sessions.GetProviderRecord(ctx, tenant, source.ProviderRef)
		if getErr != nil {
			return nil, getErr
		}
		if modelSourceRevision(rec.Kind, rec.Service, rec.BaseURL, rec.SecretRef, strconv.FormatInt(rec.Version, 10)) != source.Revision {
			return nil, sessions.ErrProviderRecordChanged
		}
		ids, err = d.sessions.ReadProviderModels(ctx, tenant, rec)
	case source.AccountRef != "":
		profile, getErr := d.sessions.GetProfile(ctx, tenant, source.AccountRef)
		if getErr != nil {
			return nil, getErr
		}
		if _, err := d.sessions.ModelProfileHomes(tenant, profile); err != nil {
			return nil, err
		}
		if modelHomeRevision(profile.UserHome, profile.ConfigHome) != source.Revision {
			return nil, sessions.ErrProviderRecordChanged
		}
		ids, err = d.sessions.ReadProfileModels(ctx, tenant, source.AccountRef)
	default:
		ids, err = d.sessions.ReadOwnToolLoginModels(ctx, tenant, source.Driver)
	}
	if errors.Is(err, sessions.ErrToolModelListUnsupported) {
		return nil, models.ErrModelDiscoveryUnsupported
	}
	if err != nil {
		return nil, err
	}
	// A file-based sign-in can change while the tool answers. Verify the current
	// source revision before publishing models under the old account context.
	current, err := d.Sources(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, now := range current {
		if now.Ref == source.Ref && now.Revision == source.Revision {
			return ids, nil
		}
	}
	return nil, sessions.ErrProviderRecordChanged
}
