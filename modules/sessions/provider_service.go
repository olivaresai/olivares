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

func providerServiceBaseURL(kind, service, base string) (string, error) {
	if service == "" {
		return base, nil
	}
	if service != modelprovider.ServiceDeepSeek || kind != ProviderKindOpenAICompatible {
		return "", badRequest("unsupported provider service or kind")
	}
	base = strings.TrimSpace(base)
	if base != "" && base != modelprovider.DeepSeekBaseURL {
		return "", badRequest("DeepSeek service requires its documented endpoint")
	}
	return modelprovider.DeepSeekBaseURL, nil
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
	if rec.Version != version || service != modelprovider.ServiceDeepSeek || rec.Service != service ||
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
	if err != nil || current.Version != version || current.State != ProviderRecordActive || current.SecretRef != rec.SecretRef {
		clear(key)
		return nil, ErrProviderRecordChanged
	}
	return key, nil
}
