// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/model"
)

// ProviderServiceProbe adds explicit service selection without redefining legacy
// probes. Untagged registrations continue to use ProviderProbe unchanged.
type ProviderServiceProbe interface {
	ProbeService(context.Context, ProviderProbeRequest, string) (ProviderProbeResult, error)
}

// maxPresetRefEcho bounds how much of an unknown ref an error repeats back.
const maxPresetRefEcho = 64

// providerServiceBaseURL resolves a record's service to its row of the preset
// catalog and holds the record to it: the row's kind and its documented endpoint.
// No service leaves the base as typed.
func providerServiceBaseURL(kind, service, base string) (string, error) {
	if service == "" {
		return base, nil
	}
	row, ok := modelprovider.LookupPreset(service)
	if !ok {
		if len(service) > maxPresetRefEcho {
			service = service[:maxPresetRefEcho] + "…"
		}
		return "", badRequest("unknown provider preset " + strconv.Quote(service) + "; valid presets: " + strings.Join(modelprovider.PresetRefs(), ", "))
	}
	if kind != row.Kind {
		return "", badRequest("preset " + row.Ref + " has kind " + row.Kind)
	}
	if base = strings.TrimRight(strings.TrimSpace(base), "/"); base != "" && base != row.BaseURL {
		return "", badRequest("preset " + row.Ref + " requires its documented endpoint")
	}
	return row.BaseURL, nil
}

func (m *Module) probeProviderService(ctx context.Context, rec ProviderRecord, key string) (ProviderProbeResult, error) {
	req := ProviderProbeRequest{Kind: rec.Kind, BaseURL: rec.BaseURL, APIKey: key}
	if rec.Service == "" {
		return m.rt.ProviderProbe.Probe(ctx, req)
	}
	p, ok := m.rt.ProviderProbe.(ProviderServiceProbe)
	if !ok {
		return ProviderProbeResult{}, badRequest("provider service discovery is unavailable")
	}
	return p.ProbeService(ctx, req, rec.Service)
}

// ParseProviderCredentialReference parses the non-secret immutable provider pin
// used only by service execution profiles: provider:<record-ref>:<record-version>.
// The version names the key and endpoint the record held then; the pin holds while
// they stay in place, across connection tests, renames and default-model changes.
func ParseProviderCredentialReference(value string) (string, int64, bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != "provider" || !validProviderRecordRef(parts[1]) {
		return "", 0, false
	}
	version, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || version <= 0 || strconv.FormatInt(version, 10) != parts[2] {
		return "", 0, false
	}
	return parts[1], version, true
}

// ResolveProviderCredential is an in-process port for an already authorized
// governed inference attempt. It checks the exact tenant, active provider
// revision, service and operation before opening the sealed value. The caller
// must run its content/budget/intent gates first and must not log the result.
func (m *Module) ResolveProviderCredential(ctx context.Context, tenant model.TenantID, ref string, version int64, service, endpoint string) ([]byte, error) {
	rec, err := m.GetProviderRecord(ctx, tenant, ref)
	if err != nil {
		return nil, err
	}
	if rec.State != ProviderRecordActive {
		return nil, ErrProviderRecordRevoked
	}
	if !rec.pinHolds(version) || service != modelprovider.ServiceDeepSeek || rec.Service != service ||
		rec.Kind != ProviderKindOpenAICompatible || rec.BaseURL != modelprovider.DeepSeekBaseURL || endpoint != modelprovider.DeepSeekChatURL {
		return nil, ErrProviderRecordChanged
	}
	if m.rt.ProviderVault == nil {
		return nil, ErrNoProviderVault
	}
	key, err := m.rt.ProviderVault.Open(ctx, tenant, rec.SecretRef)
	if err != nil {
		return nil, openFailure(err)
	}
	current, err := m.GetProviderRecord(ctx, tenant, ref)
	if err != nil || !current.pinHolds(version) || current.State != ProviderRecordActive || current.SecretRef != rec.SecretRef {
		clear(key)
		return nil, ErrProviderRecordChanged
	}
	return key, nil
}

// pinHolds reports whether the key and endpoint in place at record version v are
// still the record's: set at or before v, and v not newer than the record. A row
// from before the credential version was recorded holds only its current version.
func (r ProviderRecord) pinHolds(v int64) bool {
	since := r.CredentialVersion
	if since == 0 {
		since = r.Version
	}
	return since <= v && v <= r.Version
}
