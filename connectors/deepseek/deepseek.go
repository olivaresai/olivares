// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package deepseek is the Olivares AI connector for the DeepSeek platform — a Chinese
// frontier provider whose hosted API (api.deepseek.com) runs on servers in the People's
// Republic of China under PRC law (National Intelligence Law, Data Security Law,
// Cybersecurity Law). It is a read-only catalog-only governance source built on the
// shared connectors/modelprovider contract.
//
// SOVEREIGNTY CAVEAT. The hosted DeepSeek API stores and processes data on PRC
// servers subject to Chinese data-access laws. Multiple governments (Italy, Australia,
// Taiwan, US federal agencies) have restricted its use. This connector reads ONLY the
// model catalog (GET /models) and account-balance posture (GET /user/balance; verified
// again 2026-07-04; only account surface is GET /user/balance) — metadata/financial
// posture, never prompts or completions — but operators must understand that even API-key
// authentication traffic traverses PRC infrastructure. For sovereign governance of
// DeepSeek models, Olivares AI recommends SELF-HOSTING the open-weight releases (V3/R1
// and successors, MIT-licensed) via Ollama or vLLM, governed by the existing model-access
// gate, content-firewall and residency module — eliminating PRC data-path dependency
// entirely.
//
// HONEST SCOPE. DeepSeek publishes NO admin/audit/usage/org REST API; verified again
// 2026-07-04, the only account-level API is GET /user/balance. Cost is estimated via the
// exported Meter helper from declared list pricing (the same pattern as
// connectors/mistral and connectors/fal). The model catalog endpoint is OpenAI-compatible
// (GET /models, {object:"list",data:[...]}), VERIFIED-SHAPE.
//
// READ-ONLY and minimal-data (docs/SECURITY-HARDENING.md-3): every call is a GET via the shared GET-only
// modelprovider client; it carries model identifiers, capabilities and balance posture
// only — never prompts, completions, or key values. It imports only the SDK and the Apache
// modelprovider contract, never the engine.
package deepseek

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.deepseek"

// Default configuration values.
const (
	defaultBaseURL     = "https://api.deepseek.com"
	defaultModelND     = "/models"
	defaultBalancePath = "/user/balance"

	// costTypeDeepSeek tags every DeepSeek CostSample so FinOps attributes DeepSeek
	// spend distinctly from other providers.
	costTypeDeepSeek = "deepseek"
)

// Finding subjects for the DeepSeek governance posture findings.
const (
	subjectSovereignty = "deepseek.sovereignty"
	subjectBalance     = "deepseek.account_balance"
)

// Source is the DeepSeek model/provider governance source connector. It satisfies
// sdk.SourceConnector (the sovereignty posture caveat as an observation) and
// modelprovider.CatalogProvider (the live model catalog).
type Source struct {
	client *modelprovider.Client

	credential string
	baseURL    string

	doer modelprovider.Doer // injected transport (tests); nil => default
	now  func() time.Time   // injectable clock (tests); nil => time.Now
}

// Compile-time proof Source satisfies both contracts.
var (
	_ sdk.SourceConnector           = (*Source)(nil)
	_ modelprovider.CatalogProvider = (*Source)(nil)
)

// New returns a DeepSeek source with default configuration.
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
		Title:      "DeepSeek (catalog + cost metering + sovereignty caveat)",
		Description: "Read-only DeepSeek governance: live model catalog (GET /models, OpenAI-compatible) + declared list pricing, account-balance posture (GET /user/balance), and cost metering around the inference path (Meter). " +
			"SOVEREIGNTY CAVEAT: the hosted API runs on PRC servers under Chinese data-access laws; this connector reads ONLY model metadata and balance availability, never prompts or completions. " +
			"DeepSeek publishes NO admin/audit/usage/org REST API; GET /user/balance is the only account-level surface. " +
			"For sovereign governance, self-host the open-weight models (V3/R1) via Ollama or vLLM.",
		ConfigFields: []sdk.ConfigField{
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "DeepSeek API key reference (read-only Bearer; never persisted). Empty = offline catalog only."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "DeepSeek API base URL."},
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
	s.credential = cfg.Get("api_key")
	s.client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthBearer, s.credential, nil)
	return nil
}

// Gather emits the DeepSeek governance posture. It is a batch source: it returns nil when
// done. With no credential it returns nil immediately (offline — nothing pulled). When
// credentialed, it ALWAYS emits the sovereignty posture finding documenting that the
// hosted API runs on PRC servers under Chinese data-access laws, then reads GET
// /user/balance (verified 2026-07-04) for account-balance availability. It emits NO cost
// samples — there is no usage API to read; cost is metered around the inference path via
// the exported Meter helper.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.credential == "" || s.client == nil {
		return nil // offline mode: nothing to pull
	}
	if err := sink.Emit(ctx, s.sovereigntyCaveat()); err != nil {
		return err
	}
	return s.gatherBalance(ctx, sink)
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

// sovereigntyCaveat is the sovereignty posture finding (ARCHITECTURE.md, the directory's
// honesty bar): the hosted DeepSeek API runs on PRC servers under Chinese data-access
// laws, and the platform exposes no admin/audit/usage API beyond the model catalog. This
// is emitted every credentialed gather so operators see the data-residency implication in
// their posture view.
func (s *Source) sovereigntyCaveat() model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityHigh,
		SubjectKind: subjectSovereignty,
		SubjectRef:  "hosted_api",
		Title:       "DeepSeek hosted API: data processed on PRC servers under Chinese data-access laws",
		DetailHash: redact.Hash(
			"deepseek hosted API data residency: PRC; National Intelligence Law / Data Security Law / Cybersecurity Law apply; " +
				"no admin/audit/usage API; recommend self-hosting open weights for sovereign governance"),
		OccurredAt: s.clock().UTC(),
	}
}

func (s *Source) gatherBalance(ctx context.Context, sink sdk.Sink) error {
	var resp balanceResponse
	if err := s.client.GetJSON(ctx, defaultBalancePath, nil, &resp); err != nil {
		if isUnavailable(err) {
			return sink.Emit(ctx, s.balanceUnavailableFinding())
		}
		return err
	}
	sev := model.SeverityInfo
	title := "DeepSeek account balance available: " + strconv.Itoa(len(resp.BalanceInfos)) + " currency bucket(s)"
	if !resp.IsAvailable {
		sev = model.SeverityLow
		title = "DeepSeek account balance exhausted / unavailable for inference"
	}
	return sink.Emit(ctx, model.FindingReport{
		Kind:        "posture",
		Severity:    sev,
		SubjectKind: subjectBalance,
		SubjectRef:  "account",
		Title:       title,
		DetailHash:  redact.Hash(balanceDetail(resp)),
		OccurredAt:  s.clock().UTC(),
	})
}

func (s *Source) balanceUnavailableFinding() model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectBalance,
		SubjectRef:  "account",
		Title:       "DeepSeek account balance unavailable (API key not entitled / endpoint unavailable)",
		DetailHash:  redact.Hash("deepseek account balance path=" + defaultBalancePath + " base=" + s.baseURL + " returned 403/404; only documented account-level surface is GET /user/balance"),
		OccurredAt:  s.clock().UTC(),
	}
}

func balanceDetail(resp balanceResponse) string {
	parts := []string{"is_available=" + strconv.FormatBool(resp.IsAvailable)}
	for _, b := range resp.BalanceInfos {
		parts = append(parts, "currency="+b.Currency+" total="+b.TotalBalance+" granted="+b.GrantedBalance+" topped_up="+b.ToppedUpBalance)
	}
	return strings.Join(parts, "|")
}

// Snapshot returns the DeepSeek catalog. With a credential it reads GET /models live;
// with no credential it returns the declared offline catalog. The Models API is read-only
// and carries no key/secret material. DeepSeek publishes NO inventory shape (no
// workspace/key/org API), so the catalog stays honest and never invents an empty
// inventory.
func (s *Source) Snapshot(ctx context.Context) (modelprovider.Catalog, error) {
	cat := modelprovider.Catalog{
		Provider: modelprovider.Provider{
			Ref: modelprovider.ProviderDeepSeek, Kind: modelprovider.KindHostedAPI,
			Title: "DeepSeek", BaseURL: s.baseURL,
		},
		CapturedAt: s.clock().UTC(),
	}
	if s.credential == "" || s.client == nil {
		cat.Models = declaredCatalogModels()
		return cat, nil
	}
	models, err := s.fetchModels(ctx)
	if err != nil {
		return modelprovider.Catalog{}, err
	}
	cat.Models = models
	return cat, nil
}

// fetchModels reads GET /models (no pagination — the API returns the full list in one
// data array, OpenAI-compatible) and builds the live catalog: live model ids + declared
// family pricing. CapabilitySource is "live" (the ids came from the provider API); a
// model with no declared family keeps nil pricing (never a guessed price).
func (s *Source) fetchModels(ctx context.Context) ([]modelprovider.Model, error) {
	var resp modelsResponse
	if err := s.client.GetJSON(ctx, defaultModelND, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]modelprovider.Model, 0, len(resp.Data))
	for _, c := range resp.Data {
		if c.ID == "" {
			continue
		}
		m := modelprovider.Model{
			ProviderRef:      modelprovider.ProviderDeepSeek,
			Ref:              c.ID,
			DisplayName:      displayNameFor(c.ID),
			CapabilitySource: "live",
			CreatedAt:        unixTime(c.Created),
		}
		if f, ok := familyFor(c.ID); ok {
			pc := f.pricing
			m.Pricing = &pc
			m.Capabilities = append([]modelprovider.Capability(nil), f.capabilities...)
			m.ContextWindow = f.context
			m.MaxOutputTokens = f.maxOutput
			m.Deprecated = f.deprecated
			m.Retirements = append([]modelprovider.ModelRetirement(nil), f.retirements...)
		}
		out = append(out, m)
	}
	return out, nil
}

// displayNameFor returns the human-readable display name for a DeepSeek model id. It
// consults the declared model list first; if no match is found, it returns the id itself.
func displayNameFor(modelID string) string {
	for _, d := range declaredModels {
		if d.id == modelID {
			return d.displayName
		}
	}
	return modelID
}

// unixTime converts a Unix-seconds timestamp to UTC, returning the zero time for a
// zero/absent value so a missing provider timestamp never aborts a read.
func unixTime(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

func isUnavailable(err error) bool {
	var apiErr *modelprovider.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == 403 || apiErr.Status == 404
	}
	msg := err.Error()
	return strings.Contains(msg, "status 404") || strings.Contains(msg, "status 403")
}
