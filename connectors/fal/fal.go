// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package fal is the Olivares AI connector for fal.ai — the media-inference platform
// (600+ image/audio/video models), read through its API-key + queue surface (§4 G5, §5). fal is API-KEY-ONLY (no OAuth), bills PAY-PER-OUTPUT, and exposes NO
// public usage/audit API — so the useful, honest connector is two things:
//
//   - GOVERNANCE BY KEY LIFECYCLE (the control point): inventory the API keys
//     (metadata only — never the secret) and emit a rotation-posture finding for any
//     key older than a threshold. Issuing/rotating a key is a MUTATION (out of scope
//     for a read-first source; HITL-gated) — the connector surfaces the POSTURE that
//     drives it, exactly as it surfaces other governance gaps.
//   - COST METERING AROUND THE QUEUE API: fal has no usage report, so cost is metered
//     per request from the queue STATUS metrics (inference_time) and the declared
//     per-output price catalog → model.CostSample (provenance=estimated). The exported
//     Meter helper lets the queue-driving caller (a gateway/runtime) price a completed
//     result with the exact output count; Gather meters any operator-configured
//     in-flight request ids from their status.
//
// DEEP GOVERNANCE IS SALES-GATED / UNVERIFIED (documented honestly):
// enterprise SOC2/SSO/private-endpoints and any fine-grained audit are sales-gated and
// could not be verified offline. Gather emits an explicit caveat finding so the posture
// view never implies coverage the platform does not expose; the key-management REST
// shape is UNVERIFIED-OFFLINE and degrades honestly on 403/404.
//
// READ-ONLY and minimal-data (docs/SECURITY-HARDENING.md-3): every call is a GET via the shared
// GET-only modelprovider client (auth scheme AuthFalKey → "Authorization: Key <cred>"),
// so the connector CANNOT submit a job or mutate a key; it carries key inventory
// METADATA and queue compute metrics — never a key value or the generated media. It
// imports only the SDK and the Apache modelprovider contract, never the engine.
package fal

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.fal"

// Default configuration values.
const (
	defaultKeysBaseURL  = "https://rest.fal.ai"
	defaultQueueBaseURL = "https://queue.fal.run"
	defaultKeysPath     = "/keys"
	defaultKeyMaxAge    = 90 * 24 * time.Hour // rotate keys older than 90 days
	defaultMaxPages     = 20
)

// Source is the fal.ai governance + metering source connector. It satisfies
// sdk.SourceConnector (key posture + metered cost as observations) and
// modelprovider.CatalogProvider (the model + key inventory catalog).
type Source struct {
	keysClient  *modelprovider.Client
	queueClient *modelprovider.Client

	credential   string
	keysBaseURL  string
	queueBaseURL string
	keysPath     string
	keyMaxAge    time.Duration
	maxPages     int

	manageKeys     bool
	model          string   // fal model id for the polled request ids
	requestIDs     []string // operator-configured in-flight request ids to meter
	fallbackPerSec float64  // operator fallback $/compute-second for models not in the catalog (0 = no guess)

	doer modelprovider.Doer // injected transport (tests); nil => default
	now  func() time.Time   // injectable clock (tests); nil => time.Now
}

// Compile-time proof Source satisfies both contracts.
var (
	_ sdk.SourceConnector           = (*Source)(nil)
	_ modelprovider.CatalogProvider = (*Source)(nil)
)

// New returns a fal source with default configuration.
func New() *Source {
	return &Source{
		keysBaseURL:  defaultKeysBaseURL,
		queueBaseURL: defaultQueueBaseURL,
		keysPath:     defaultKeysPath,
		keyMaxAge:    defaultKeyMaxAge,
		maxPages:     defaultMaxPages,
		manageKeys:   true,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "fal.ai (key governance + cost metering)",
		Description: "Read-only fal.ai governance: API-key inventory + rotation posture (the control point) and pay-per-output cost metering around the queue API. Deep governance (SOC2/SSO/private endpoints) is sales-gated and surfaced as an UNVERIFIED caveat.",
		ConfigFields: []sdk.ConfigField{
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "fal API key reference (the \"key_id:key_secret\" pair; read-only; never persisted). Empty = offline catalog only."},
			{Key: "keys_base_url", Type: sdk.FieldString, Default: defaultKeysBaseURL, Description: "fal key-management REST base URL (UNVERIFIED-OFFLINE; override per your tenant)."},
			{Key: "queue_base_url", Type: sdk.FieldString, Default: defaultQueueBaseURL, Description: "fal queue API base URL (queue.fal.run)."},
			{Key: "keys_path", Type: sdk.FieldString, Default: defaultKeysPath, Description: "Path of the key-management list endpoint (UNVERIFIED-OFFLINE)."},
			{Key: "manage_keys", Type: sdk.FieldBool, Default: "true", Description: "Inventory API keys and emit rotation-posture findings (the key-lifecycle control point)."},
			{Key: "key_max_age", Type: sdk.FieldDuration, Default: "2160h", Description: "Emit a rotation-posture finding for any key older than this (default 90 days)."},
			{Key: "model", Type: sdk.FieldString, Description: "fal model id (e.g. fal-ai/flux/dev) for the polled request ids."},
			{Key: "request_ids", Type: sdk.FieldString, Description: "Comma-separated in-flight queue request ids to poll + meter on each Gather (cost metering around the queue API)."},
			{Key: "fallback_second_usd", Type: sdk.FieldString, Default: "0", Description: "Fallback $/compute-second for models not in the declared catalog (0 = never guess a price)."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per gather."},
		},
	}
}

// Open reads configuration and builds the two read-only fal clients (key-management
// REST + queue), both using the fal "Authorization: Key <cred>" auth scheme. It never
// fails for a missing credential: with no api_key the connector runs in offline catalog
// mode (Snapshot returns the declared catalog; Gather emits nothing).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := strings.TrimRight(cfg.Get("keys_base_url"), "/"); v != "" {
		s.keysBaseURL = v
	}
	if v := strings.TrimRight(cfg.Get("queue_base_url"), "/"); v != "" {
		s.queueBaseURL = v
	}
	if v := strings.TrimSpace(cfg.Get("keys_path")); v != "" {
		s.keysPath = v
	}
	s.manageKeys = cfg.GetBool("manage_keys", s.manageKeys)
	s.keyMaxAge = cfg.GetDuration("key_max_age", s.keyMaxAge)
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	s.model = strings.TrimSpace(cfg.Get("model"))
	s.requestIDs = splitCSV(cfg.Get("request_ids"))
	s.fallbackPerSec = parseFloat(cfg.Get("fallback_second_usd"))
	s.credential = cfg.Get("api_key")

	s.keysClient = modelprovider.NewClient(s.keysBaseURL, s.doer, modelprovider.AuthFalKey, s.credential, nil)
	s.queueClient = modelprovider.NewClient(s.queueBaseURL, s.doer, modelprovider.AuthFalKey, s.credential, nil)
	return nil
}

// Gather emits the fal governance posture (key rotation + the sales-gated caveat) and
// meters any operator-configured in-flight queue request ids. It is a batch source: it
// returns nil when done. With no credential it returns nil immediately (offline →
// nothing pulled). A 403/404 on the UNVERIFIED key-management surface degrades to a
// posture finding rather than failing the run.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.credential == "" {
		return nil // offline mode: nothing to pull
	}
	if s.manageKeys {
		if err := s.gatherKeyPosture(ctx, sink); err != nil {
			return err
		}
	}
	if err := sink.Emit(ctx, s.salesGatedCaveat()); err != nil {
		return err
	}
	if len(s.requestIDs) > 0 {
		if err := s.gatherMeteredRequests(ctx, sink); err != nil {
			return err
		}
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

// dollarsToMicroUSD converts a major-unit (dollars) amount to integer micro-USD. A
// negative/NaN amount clamps to 0 (unknown), never a guessed cost (ARCHITECTURE.md).
func dollarsToMicroUSD(value float64) int64 {
	if value <= 0 || value != value { // value!=value is the NaN guard
		return 0
	}
	return int64(value*1_000_000 + 0.5)
}

// parseTime parses an RFC3339 timestamp, returning the zero time on any error.
func parseTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(v))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// parseFloat parses a non-negative float, returning 0 on any error (no guessed price).
func parseFloat(v string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}

// splitCSV splits a comma-separated list, trimming spaces and dropping empties.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// isUnavailable reports whether err is a 403/404 from the UNVERIFIED key-management
// surface, so the connector can degrade to an honest posture finding instead of
// failing the gather. It never matches a transport error (which the engine retries).
func isUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status 404") || strings.Contains(msg, "status 403")
}
