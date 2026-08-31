// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package glm

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.glm"

// Default configuration values.
const (
	defaultBaseURL    = "https://api.z.ai/api/paas/v4"
	defaultModelsPath = "/models"

	// costTypeGLM tags every GLM CostSample so FinOps attributes GLM spend
	// distinctly from other hosted model providers.
	costTypeGLM = "glm"
)

// Finding subjects for the GLM governance posture findings.
const (
	subjectSovereignty = "glm.sovereignty"
	subjectEntitlement = "glm.entitlement"
)

// Source is the GLM model/provider governance source connector. It satisfies
// sdk.SourceConnector (sovereignty and entitlement posture) and
// modelprovider.CatalogProvider (declared catalog only).
type Source struct {
	client *modelprovider.Client

	credential string
	baseURL    string

	doer modelprovider.Doer
	now  func() time.Time
}

// Compile-time proof Source satisfies both contracts.
var (
	_ sdk.SourceConnector           = (*Source)(nil)
	_ modelprovider.CatalogProvider = (*Source)(nil)
)

// New returns a GLM source with the default international Z.ai surface.
func New() *Source {
	return &Source{
		baseURL: defaultBaseURL,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       Name,
		Version:    "0.1.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeSource,
		Title:      "Zhipu GLM (Z.ai catalog + cost metering + sovereignty caveat)",
		Description: "Read-only GLM governance for the Z.ai/BigModel /api/paas/v4 surfaces: declared USD catalog + list-pricing Meter and a PRC-nexus sovereignty caveat. " +
			"Snapshot never parses /models because the response schema is undocumented; Gather uses GET /models only as a best-effort entitlement probe and emits no cost samples. " +
			"GLM exposes no verified usage, billing, balance, admin, key or organization API; spend is metered around the inference path via Meter.",
		ConfigFields: []sdk.ConfigField{
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "GLM API key reference (read-only Bearer; never persisted). Empty = offline declared catalog only."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "GLM /api/paas/v4 base URL; default is the international Z.ai surface."},
		},
	}
}

// Open reads configuration and builds the read-only Bearer client. It never fails for
// a missing credential: with no api_key the connector runs in offline catalog mode
// (Snapshot returns the declared catalog; Gather emits nothing).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := strings.TrimRight(cfg.Get("base_url"), "/"); v != "" {
		s.baseURL = v
	}
	s.credential = cfg.Get("api_key")
	s.client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthBearer, s.credential, nil)
	return nil
}

// Gather emits GLM governance posture. With no credential it returns nil immediately
// (offline — nothing pulled). When credentialed, it ALWAYS emits the sovereignty
// posture finding, then performs a best-effort GET /models liveness/entitlement probe
// with out=nil. The /models body is intentionally discarded because its schema is
// undocumented. Gather emits NO cost samples — GLM exposes no usage/billing API; cost
// is metered around the inference path via Meter.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.credential == "" || s.client == nil {
		return nil
	}
	if err := sink.Emit(ctx, s.sovereigntyCaveat()); err != nil {
		return err
	}
	return s.gatherEntitlement(ctx, sink)
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// sovereigntyCaveat is emitted on every credentialed gather so operators see that
// both GLM hosted surfaces carry PRC-nexus sovereignty risk despite different hosting
// entities.
func (s *Source) sovereigntyCaveat() model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityHigh,
		SubjectKind: subjectSovereignty,
		SubjectRef:  "hosted_api",
		Title:       "Zhipu GLM hosted API: PRC-nexus parent Entity-Listed; caveat applies to z.ai and bigmodel.cn",
		DetailHash: redact.Hash(
			"glm hosted API sovereignty: bigmodel.cn PRC-hosted under National Intelligence Law / Data Security Law / Cybersecurity Law; " +
				"z.ai Singapore deployment shares PRC-domiciled Entity-Listed parent Beijing Zhipu Huazhang; " +
				"no verified admin/audit/usage/billing API; recommend self-hosting MIT open weights via Ollama/vLLM"),
		OccurredAt: s.clock().UTC(),
	}
}

func (s *Source) gatherEntitlement(ctx context.Context, sink sdk.Sink) error {
	if err := s.client.GetJSON(ctx, defaultModelsPath, nil, nil); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if isUnavailable(err) {
			return sink.Emit(ctx, s.entitlementUnverifiedFinding())
		}
		return sink.Emit(ctx, s.entitlementProbeFailedFinding())
	}
	return sink.Emit(ctx, s.entitlementValidFinding())
}

func (s *Source) entitlementValidFinding() model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectEntitlement,
		SubjectRef:  "models",
		Title:       "GLM /models liveness probe valid (API key accepted; response body not parsed)",
		DetailHash:  redact.Hash("glm entitlement probe valid path=" + defaultModelsPath + " base=" + s.baseURL + " body=discarded schema=unverified"),
		OccurredAt:  s.clock().UTC(),
	}
}

func (s *Source) entitlementUnverifiedFinding() model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectEntitlement,
		SubjectRef:  "models",
		Title:       "GLM /models entitlement unverified or API key not entitled",
		DetailHash:  redact.Hash("glm entitlement probe unavailable status=401/403 path=" + defaultModelsPath + " base=" + s.baseURL + " body=discarded schema=unverified"),
		OccurredAt:  s.clock().UTC(),
	}
}

func (s *Source) entitlementProbeFailedFinding() model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectEntitlement,
		SubjectRef:  "models",
		Title:       "GLM /models liveness probe unavailable (best-effort check degraded)",
		DetailHash:  redact.Hash("glm entitlement probe failed path=" + defaultModelsPath + " base=" + s.baseURL + " body=discarded schema=unverified"),
		OccurredAt:  s.clock().UTC(),
	}
}
