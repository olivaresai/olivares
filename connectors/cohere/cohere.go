// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package cohere is the Olivares AI connector for the Cohere platform — an enterprise AI
// provider with sovereign on-prem/VPC deployment via Model Vault (README.md, module X
// multi-vendor). It is a read-only catalog-only governance source built on the shared
// connectors/modelprovider contract.
//
// HONEST SCOPE (primary-source verified 2026-06-28). Cohere's PUBLIC API surface for
// governance is thin:
//
//   - VERIFIED-SHAPE and REAL: the Models API (GET /v1/models) returns the model catalog
//     with per-model endpoints[], context_length, and deprecation status. This is the
//     connector's authoritative core — Snapshot reads it and enriches each model with
//     declared list pricing, so module X gets a real Cohere catalog.
//
//   - NO PUBLIC API (dashboard-only): usage, billing, org/team management, API key
//     inventory, and audit logs are all dashboard-only (dashboard.cohere.com) with no
//     REST endpoint. Cost flows via the exported Meter helper (declared list pricing ->
//     model.CostSample, provenance=estimated) for the gateway/proxy.
//
//   - SOVEREIGN DEPLOYMENT: Cohere offers Model Vault — VPC/on-prem deployment of their
//     models — as a commercial enterprise option. Operators using Model Vault have full
//     data sovereignty (the models run in THEIR infrastructure). This connector reads the
//     HOSTED API catalog; Model Vault deployments are governed by the operator's own
//     infrastructure governance, not this connector.
//
// READ-ONLY and minimal-data (docs/SECURITY-HARDENING.md-3): every call is a GET via the shared GET-only
// modelprovider client; it carries model identifiers and capabilities only — never
// prompts, completions, or key values. It imports only the SDK and the Apache
// modelprovider contract, never the engine.
package cohere

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.cohere"

// Default configuration values.
const (
	defaultBaseURL    = "https://api.cohere.com"
	defaultModelsPath = "/v1/models"
	defaultMaxPages   = 20

	// costTypeCohere tags every Cohere CostSample so FinOps attributes Cohere spend
	// distinctly from other providers.
	costTypeCohere = "cohere"
)

// Finding subjects for the Cohere governance posture findings.
const (
	subjectCoverage = "cohere.coverage"
)

// Source is the Cohere model/provider governance source connector. It satisfies
// sdk.SourceConnector (the honest coverage caveat as an observation) and
// modelprovider.CatalogProvider (the live model catalog).
type Source struct {
	client *modelprovider.Client

	credential string
	baseURL    string
	maxPages   int

	doer modelprovider.Doer // injected transport (tests); nil => default
	now  func() time.Time   // injectable clock (tests); nil => time.Now
}

// Compile-time proof Source satisfies both contracts.
var (
	_ sdk.SourceConnector           = (*Source)(nil)
	_ modelprovider.CatalogProvider = (*Source)(nil)
)

// New returns a Cohere source with default configuration.
func New() *Source {
	return &Source{
		baseURL:  defaultBaseURL,
		maxPages: defaultMaxPages,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:       Name,
		Version:    "0.1.0",
		APIVersion: sdk.APIVersion,
		Type:       sdk.TypeSource,
		Title:      "Cohere (catalog + cost metering)",
		Description: "Read-only Cohere governance: live model catalog (GET /v1/models) + " +
			"declared list pricing, and cost metering around the inference path " +
			"(Meter -> estimated CostSample). Cohere exposes NO public usage/billing " +
			"API (dashboard-only) — surfaced as an honest coverage caveat. " +
			"Model Vault (on-prem/VPC) deployments are governed by the operator's " +
			"own infrastructure, not this connector.",
		ConfigFields: []sdk.ConfigField{
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "Cohere API key reference (read-only Bearer; never persisted). Empty = offline catalog only."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Cohere API base URL."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per gather."},
		},
	}
}

// Open reads configuration and builds the read-only Bearer client. It never fails for a
// missing credential: with no api_key the connector runs in offline catalog mode
// (Snapshot returns the declared catalog; Gather emits nothing).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := strings.TrimRight(cfg.Get("base_url"), "/"); v != "" {
		s.baseURL = v
	}
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	s.credential = cfg.Get("api_key")

	s.client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthBearer, s.credential, nil)
	return nil
}

// Gather emits the Cohere governance posture. It is a batch source: it returns nil when
// done. With no credential it returns nil immediately (offline -> nothing pulled). When
// credentialed it emits the honest coverage caveat (Cohere has no public usage/billing
// API) and a posture finding about Model Vault sovereign deployment. It emits NO cost
// samples — there is no usage API to read; cost is metered around the inference path via
// the exported Meter helper.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.credential == "" || s.client == nil {
		return nil // offline mode: nothing to pull
	}
	if err := sink.Emit(ctx, s.coverageCaveat()); err != nil {
		return err
	}
	return nil
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

// coverageCaveat is the honest coverage caveat (ARCHITECTURE.md, the directory's honesty bar):
// Cohere exposes no public usage/billing API, so the control plane meters cost around
// the inference path (Meter) and reads the live catalog — but does NOT claim
// usage/cost observability it cannot perform. It also notes the Model Vault sovereign
// deployment option for operators who need data sovereignty.
func (s *Source) coverageCaveat() model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectCoverage,
		SubjectRef:  "cohere",
		Title:       fmt.Sprintf("Cohere usage/billing is dashboard-only; no public API — Model Vault sovereign deployment available (catalog is live, cost is metered around inference)"),
		DetailHash: redact.Hash("cohere: GET /v1/models is the only verified programmatic surface; " +
			"usage, billing, org/team management, API key inventory, and audit logs are all " +
			"dashboard-only (dashboard.cohere.com) with no REST endpoint; cost is metered via Meter " +
			"(estimated from list pricing). Cohere offers Model Vault — VPC/on-prem sovereign " +
			"deployment of their models as a commercial enterprise option; Model Vault deployments " +
			"are governed by the operator's own infrastructure, not this connector"),
		OccurredAt: s.clock().UTC(),
	}
}
