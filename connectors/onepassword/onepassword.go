// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package onepassword is the Olivares AI connector for the 1Password Events
// Reporting API: secret-access ATTRIBUTION for the unified secret-manager
// inventory. It does two things, both read-only:
//
//   - Snapshot (identitysource.GraphProvider) inventories the 1Password account
//     itself as one governed secret_store NHI — the custodian row — by
//     introspecting the Events Reporting bearer token
//     (GET /api/v2/auth/introspect → account uuid + granted feature scopes).
//   - Gather (sdk.SourceConnector) reads the item-usage event feed
//     (POST /api/v2/itemusages) and emits one EdgeObservation per usage: WHICH
//     user identity touched WHICH vault item, when, and through which action
//     (fill, reveal, secure-copy, export, …). Every item usage is a READ of a
//     secret — none of the reported actions mutates the item — so Mode is
//     always ModeRead.
//
// Read-only and minimal-data (docs/SECURITY-HARDENING.md-3). The Events Reporting API is
// POST-based by design (the cursor rides the request body), so the shared
// GET-only httpx client cannot carry the feed; this package therefore has a
// small local POST helper over the same injectable Doer. The POST bodies carry
// ONLY cursor/window parameters ({"limit","start_time"} or {"cursor"}) — a read
// of an append-only event feed, never a mutation — so the read-only guarantee
// holds by construction: there is no code path that sends anything else. The
// connector never requests, stores or logs an item's secret VALUE (the API does
// not even expose it), and it deliberately drops the user's email and name from
// every observation: the origin reference is the raw user uuid only (minimal
// data; the roster sources own the uuid→person mapping). The bearer token is
// held in memory, applied per request, and never appears in an error or any
// emitted field. With no events_token the connector runs offline: Snapshot
// returns an empty graph (Source + CapturedAt set, nil error) and Gather
// returns nil.
//
// Gather is a BATCH source: each run opens a fresh ResetCursor window of
// `lookback` and walks the cursor until has_more=false (or max_pages); the
// engine re-polls via poll_seconds. Re-emission of events across overlapping
// windows is acceptable — the engine de-duplicates on the edge's natural key
// including ObservedAt (at-least-once delivery, S02).
//
// Primary-source facts VERIFIED 2026-06-11 against developer.1password.com
// (Events Reporting API reference, spec 1.4.1): bearer token from an Events
// Reporting integration (1Password Business); GET /api/v2/auth/introspect →
// {uuid, account_uuid, features:["auditevents","itemusages","signinattempts"]};
// POST /api/v2/itemusages with body EITHER ResetCursor
// {"limit":N,"start_time":RFC3339} OR Cursor {"cursor":"..."}; response
// {cursor, has_more, items:[{uuid, timestamp, used_version, vault_uuid,
// item_uuid, action, user:{uuid,name,email}, client:{app_name,...}}]}. Region
// base URLs: events.1password.com / events.1password.ca / events.1password.eu /
// events.ent.1password.com (config). The v1 endpoints are legacy; this package
// builds against v2 only. The 'Unified Access' audit pillar announced
// 2026-03-17 is still 'coming soon' as of the verification date — the Events
// Reporting API IS today's secret-access attribution surface.
package onepassword

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.onepassword"

// SignalOnePassword is the package-local SignalSource for the item-usage edges.
// It is an open string by the S02 §6 convention (see connectors/flux): a new
// provenance value needs no SDK release and the sdk/model enum stays untouched.
const SignalOnePassword model.SignalSource = "onepassword"

// Default configuration values.
const (
	defaultBaseURL  = "https://events.1password.com"
	defaultLookback = 24 * time.Hour
	defaultLimit    = 100
	defaultMaxPages = 50
	defaultTimeout  = 30 * time.Second
)

// maxBody bounds every response read; maxErrExcerpt bounds how much of an error
// body may ride an error string (never the token — it lives only in a header).
const (
	maxBody       = 16 << 20
	maxErrExcerpt = 2 << 10
)

// Source is the 1Password Events Reporting connector. It satisfies
// sdk.SourceConnector (the item-usage secret-access edges) and
// identitysource.GraphProvider (the account-as-custodian roster row).
type Source struct {
	token    string
	baseURL  string
	lookback time.Duration
	limit    int
	maxPages int
	timeout  time.Duration

	client *httpx.Client    // GET-only client for /api/v2/auth/introspect
	doer   httpx.Doer       // injected transport (tests); nil => http.DefaultClient
	now    func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns a 1Password connector with default configuration.
func New() *Source {
	return &Source{
		baseURL:  defaultBaseURL,
		lookback: defaultLookback,
		limit:    defaultLimit,
		maxPages: defaultMaxPages,
		timeout:  defaultTimeout,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "1Password Events Reporting",
		Description: "Reads 1Password item-usage events (who accessed which vault item) and inventories the account as a secret-store NHI. Read-only; never reads item values.",
		ConfigFields: []sdk.ConfigField{
			{Key: "events_token", Type: sdk.FieldString, Secret: true, Description: "Events Reporting bearer token reference (1Password Business; read-only; never persisted). Empty = offline (empty graph, no events)."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Events API base URL by region: events.1password.com / .ca / .eu / events.ent.1password.com."},
			{Key: "lookback", Type: sdk.FieldDuration, Default: "24h", Description: "ResetCursor window each Gather run reads (start_time = now - lookback)."},
			{Key: "limit", Type: sdk.FieldInt, Default: "100", Description: "Page size requested per itemusages call."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: "50", Description: "Pagination safety bound per Gather run."},
			{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Per-request timeout."},
		},
	}
}

// Open reads configuration and builds the introspect client. It never fails for
// a missing credential: with no events_token the connector runs offline
// (Snapshot empty, Gather emits nothing).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.token = cfg.Get("events_token")
	if v := strings.TrimRight(cfg.Get("base_url"), "/"); v != "" {
		s.baseURL = v
	}
	s.lookback = cfg.GetDuration("lookback", s.lookback)
	if s.lookback <= 0 {
		s.lookback = defaultLookback
	}
	s.limit = cfg.GetInt("limit", s.limit)
	if s.limit <= 0 {
		s.limit = defaultLimit
	}
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	s.timeout = cfg.GetDuration("timeout", s.timeout)

	// Bearer is guarded by the token: an unconfigured connector sends no auth
	// header at all (and, offline, makes no call in the first place).
	s.client = httpx.New(s.baseURL, s.doer, httpx.Bearer(s.token), nil)
	return nil
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// introspectResponse is the relevant slice of GET /api/v2/auth/introspect.
type introspectResponse struct {
	UUID        string   `json:"uuid"`
	AccountUUID string   `json:"account_uuid"`
	Features    []string `json:"features"`
}

// Snapshot inventories the 1Password account as ONE secret_store NHI (the
// Custodian pattern): the row says "this account is a governed
// secret custodian" and carries the token's granted feature scopes as
// non-sensitive metadata. With no token it returns the offline (empty) graph
// with Source and CapturedAt set, nil error. It never returns credential
// material.
func (s *Source) Snapshot(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceOnePassword, CapturedAt: s.clock().UTC()}
	if s.token == "" || s.client == nil {
		return g, nil // offline
	}
	var resp introspectResponse
	if err := s.client.GetJSON(ctx, "/api/v2/auth/introspect", nil, &resp); err != nil {
		return identitysource.Graph{}, err
	}
	g.Identities = append(g.Identities, identitysource.Identity{
		Ref:         "1password:" + resp.AccountUUID,
		Type:        identitysource.PrincipalNHI,
		Kind:        identitysource.KindSecretStore,
		DisplayName: "1Password account",
		Source:      identitysource.SourceOnePassword,
		Attributes: pruneAttrs(map[string]string{
			"features": strings.Join(resp.Features, ","),
		}),
	})
	return g, nil
}

// usagesRequest is the itemusages POST body. Exactly one form is sent per call:
// the ResetCursor form (Limit + StartTime) on the first page of a run, the
// Cursor form afterwards. These cursor/window parameters are the ONLY thing the
// connector ever writes on the wire — a feed read, never a mutation.
type usagesRequest struct {
	Limit     int    `json:"limit,omitempty"`
	StartTime string `json:"start_time,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
}

// usagesResponse is the relevant slice of the itemusages reply.
type usagesResponse struct {
	Cursor  string      `json:"cursor"`
	HasMore bool        `json:"has_more"`
	Items   []itemUsage `json:"items"`
}

// itemUsage is one item-usage event. Only the fields the connector reads are
// mapped; the user's email and name are decoded for completeness of the wire
// shape but are NEVER emitted (minimal data — the raw uuid is the origin ref).
type itemUsage struct {
	UUID      string `json:"uuid"`
	Timestamp string `json:"timestamp"`
	VaultUUID string `json:"vault_uuid"`
	ItemUUID  string `json:"item_uuid"`
	Action    string `json:"action"`
	User      struct {
		UUID  string `json:"uuid"`
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"user"`
}

// Gather reads the item-usage feed for the configured lookback window and emits
// one EdgeObservation per usage event. It is a batch run: it starts with a
// ResetCursor body ({"limit","start_time"}), follows the returned cursor while
// has_more is true (bounded by max_pages), and returns nil at the end of the
// feed; the engine re-polls. With no token it returns nil immediately
// (offline). Events lacking a user uuid or an item uuid are skipped (no
// attributable edge), as are events with an unparseable timestamp.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.token == "" {
		return nil // offline
	}
	body := usagesRequest{
		Limit:     s.limit,
		StartTime: s.clock().UTC().Add(-s.lookback).Format(time.RFC3339),
	}
	for page := 0; page < s.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp usagesResponse
		if err := s.postJSON(ctx, "/api/v2/itemusages", body, &resp); err != nil {
			return err
		}
		for _, it := range resp.Items {
			if it.User.UUID == "" || it.ItemUUID == "" {
				continue // not attributable to an identity + item
			}
			ts, err := time.Parse(time.RFC3339, it.Timestamp)
			if err != nil {
				continue // no usable natural-key timestamp
			}
			if err := sink.Emit(ctx, model.EdgeObservation{
				OriginKind: "identity",
				// The raw user uuid ONLY — never the email or display name
				// (minimal data; the roster sources resolve uuid→person).
				OriginRef:    it.User.UUID,
				ResourceKind: "onepassword.item",
				ResourceRef:  it.VaultUUID + "/" + it.ItemUUID,
				// Every reported item-usage action (fill, reveal, secure-copy,
				// export, …) discloses the secret to the user: a READ.
				Mode:       model.ModeRead,
				ToolRef:    it.Action,
				Source:     SignalOnePassword,
				Confidence: model.ConfidenceAttributed,
				ObservedAt: ts,
			}); err != nil {
				return err
			}
		}
		if !resp.HasMore {
			return nil
		}
		body = usagesRequest{Cursor: resp.Cursor}
	}
	return nil
}

// postJSON performs one authenticated POST to the events feed and decodes the
// JSON reply. It is the package-local complement to the GET-only httpx client
// (which cannot carry a POST by construction): the request body is always a
// usagesRequest — cursor/window parameters only — so this helper reads a feed,
// it never mutates anything. A non-2xx status becomes an error carrying the
// code and a bounded body excerpt; the bearer token rides only the header and
// never an error string.
func (s *Source) postJSON(ctx context.Context, path string, body usagesRequest, out any) error {
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("onepassword: encode %s request: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("onepassword: build request %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.transport().Do(req)
	if err != nil {
		return fmt.Errorf("onepassword: POST %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrExcerpt))
		return fmt.Errorf("onepassword: POST %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(excerpt)))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out); err != nil {
		return fmt.Errorf("onepassword: decode %s: %w", path, err)
	}
	return nil
}

// transport returns the injected Doer or the default HTTP client (the same stub
// drives the GET introspect and the POST feed in tests).
func (s *Source) transport() httpx.Doer {
	if s.doer != nil {
		return s.doer
	}
	return http.DefaultClient
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// pruneAttrs drops empty values and returns nil for an empty map, so emitted
// attribute maps are diff-stable (the claude-wif convention, kept local: a
// connector never imports another connector).
func pruneAttrs(attrs map[string]string) map[string]string {
	for k, v := range attrs {
		if v == "" {
			delete(attrs, k)
		}
	}
	if len(attrs) == 0 {
		return nil
	}
	return attrs
}
