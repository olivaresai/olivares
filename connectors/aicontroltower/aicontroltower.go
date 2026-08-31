// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package aicontroltower is the Olivares AI identity connector for the
// ServiceNow AI Control Tower AI-asset inventory. It reads the digital-asset
// tables (by default alm_ai_system_digital_asset and alm_mcp_digital_asset)
// over ServiceNow's standard Table API and exposes each asset row as an
// identitysource.Identity (PrincipalNHI) so module VI (governance) can
// reconcile the estate's AI inventory against ServiceNow's system of record.
//
// Primary-source facts, VERIFIED 2026-06-11: AI Control Tower's base product
// is GA since 2025-05-06 (Zurich release docs). The Knowledge-2026 enhancement
// wave (including the real-time kill switch via AI Gateway) targets GA around
// August 2026 — that is read-only AWARENESS only here: this connector NEVER
// touches enforcement, kill switches or any write surface (export/actuation is
// problem, not this connector's). The inventory rides the standard,
// stable Table API (GET /api/now/table/{table}); the attribute-level data
// dictionary of the digital-asset tables is only SEMI-VERIFIED (a community
// CMDB blog; the official data-dictionary docs page would not render), so rows
// are parsed TOLERANTLY as generic JSON objects and individual fields
// (display_name, name, asset_tag, number, install_status, life_cycle_stage,
// life_cycle_stage_status, managed) are lifted only when present as plain
// strings — sys_id is the one REQUIRED key (a row without it is skipped, it
// cannot be deduplicated). Honest consequence: attribute coverage may vary by
// instance/version; nothing is guessed.
//
// This read connector is intentionally SEPARATE from connectors/servicenow
// (the WRITE-side notify output): they share only the basic/bearer operator
// auth conventions, never code paths or direction of data flow.
//
// It is read-only and minimal-data (docs/SECURITY-HARDENING.md-3): every call is a GET (the
// shared httpx client is GET-only by construction) and only asset METADATA is
// read — never credential material. The operator credential (HTTP Basic
// username+password, or an OAuth bearer token) is declared Secret, held in
// memory, applied per request as the Authorization header, and never logged,
// persisted or surfaced in an error. With no instance URL or no credential for
// the configured auth mode the connector runs offline: Snapshot returns an
// empty Graph (Source and CapturedAt set, nil error) and Gather emits nothing.
// An asset inventory is reference data, not an access edge, so Gather is
// always a no-op (the pattern). It imports only the SDK, the Apache
// identitysource contract and the shared httpx read-only client — never the
// engine.
package aicontroltower

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.ai-control-tower"

// Default configuration values.
const (
	defaultTables   = "alm_ai_system_digital_asset,alm_mcp_digital_asset"
	defaultPageSize = 200
	defaultMaxPages = 50
	defaultTimeout  = 30 * time.Second
)

// Auth modes (the servicenow output connector's conventions).
const (
	authBasic  = "basic"
	authBearer = "bearer"
)

// The known digital-asset tables and the Identity.Kind each maps to. An
// operator-configured table outside this set maps to the generic "ai_asset".
const (
	tableAISystem = "alm_ai_system_digital_asset"
	tableMCP      = "alm_mcp_digital_asset"

	kindAISystemAsset = "ai_system_asset"
	kindMCPAsset      = "mcp_asset"
	kindAIAsset       = "ai_asset"
)

// retiredInstallStatuses are the ALM install_status values that mean the asset
// is out of service: 7 = Retired, 8 = Missing.
var retiredInstallStatuses = map[string]bool{"7": true, "8": true}

// Source is the AI Control Tower inventory connector. It satisfies
// sdk.SourceConnector (a no-op Gather) and identitysource.GraphProvider (the
// asset roster).
type Source struct {
	instanceURL string
	authMode    string
	username    string
	password    string // Secret; in memory only
	token       string // Secret; in memory only
	tables      []string
	pageSize    int
	maxPages    int
	timeout     time.Duration

	client *httpx.Client    // built in Open
	doer   httpx.Doer       // injected transport (tests); nil => http.DefaultClient
	now    func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns an AI Control Tower connector with default configuration.
func New() *Source {
	return &Source{
		authMode: authBasic,
		tables:   splitTables(defaultTables),
		pageSize: defaultPageSize,
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
		Title:       "ServiceNow AI Control Tower",
		Description: "Reads the AI Control Tower digital-asset inventory tables via the ServiceNow Table API (read-only metadata; never credentials, never enforcement/kill-switch surfaces); emits no observation stream, roster travels via identity Snapshot.",
		ConfigFields: []sdk.ConfigField{
			{Key: "instance_url", Type: sdk.FieldString, Required: true, Description: "ServiceNow instance base URL, e.g. https://acme.service-now.com. Empty = offline (empty graph)."},
			{Key: "auth_mode", Type: sdk.FieldString, Default: authBasic, Description: "Authentication mode: basic (username+password) or bearer (OAuth access token)."},
			{Key: "username", Type: sdk.FieldString, Description: "Read-only integration user (basic mode)."},
			{Key: "password", Type: sdk.FieldString, Secret: true, Description: "Integration user password (basic mode; in memory only, never logged). Empty = offline."},
			{Key: "token", Type: sdk.FieldString, Secret: true, Description: "OAuth bearer access token (bearer mode; in memory only, never logged). Empty = offline."},
			{Key: "tables", Type: sdk.FieldString, Default: defaultTables, Description: "Comma-separated digital-asset tables to read."},
			{Key: "page_size", Type: sdk.FieldInt, Default: strconv.Itoa(defaultPageSize), Description: "Rows per Table API page (sysparm_limit)."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per table."},
			{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Per-request HTTP timeout."},
		},
	}
}

// Open reads configuration and builds the read-only client. It never fails for
// a MISSING credential or instance URL (that is the offline mode); the only
// error is a malformed configuration (an unknown auth_mode).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.instanceURL = strings.TrimRight(strings.TrimSpace(cfg.Get("instance_url")), "/")
	if v := strings.ToLower(strings.TrimSpace(cfg.Get("auth_mode"))); v != "" {
		s.authMode = v
	}
	if s.authMode != authBasic && s.authMode != authBearer {
		return fmt.Errorf("aicontroltower: unknown auth_mode %q (want %q or %q)", s.authMode, authBasic, authBearer)
	}
	s.username = cfg.Get("username")
	s.password = cfg.Get("password")
	s.token = cfg.Get("token")
	if v := cfg.Get("tables"); strings.TrimSpace(v) != "" {
		s.tables = splitTables(v)
	}
	s.pageSize = cfg.GetInt("page_size", s.pageSize)
	if s.pageSize <= 0 {
		s.pageSize = defaultPageSize
	}
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	s.timeout = cfg.GetDuration("timeout", s.timeout)

	// The auth function guards on the secret, so an unconfigured connector sends
	// no Authorization header at all (and offline never reaches the wire anyway).
	var auth httpx.AuthFunc
	if s.authMode == authBearer {
		auth = httpx.Bearer(s.token)
	} else {
		basic := base64.StdEncoding.EncodeToString([]byte(s.username + ":" + s.password))
		auth = httpx.Header("Authorization", "Basic "+basic, s.password)
	}
	// transport(), not s.doer: with no injected doer httpx would fall back to
	// http.DefaultClient, which has NO timeout (review fix).
	s.client = httpx.New(s.instanceURL, s.transport(), auth, nil)
	return nil
}

// offline reports whether the connector lacks an instance URL or the credential
// its auth mode needs — in which case Snapshot returns the empty graph.
func (s *Source) offline() bool {
	if s.instanceURL == "" || s.client == nil {
		return true
	}
	if s.authMode == authBearer {
		return s.token == ""
	}
	return s.username == "" || s.password == ""
}

// Gather emits no observations: an AI-asset inventory is reference data exposed
// through Snapshot, not a flow fact. It returns nil immediately.
func (s *Source) Gather(context.Context, sdk.Sink) error { return nil }

// Close releases resources; the connector holds no long-lived connection.
func (s *Source) Close(context.Context) error { return nil }

// tableResponse is the Table API envelope. Rows are kept as generic objects:
// the digital-asset data dictionary is only semi-verified (see the package doc
// comment), so fields are lifted tolerantly rather than declared.
type tableResponse struct {
	Result []map[string]any `json:"result"`
}

// Snapshot reads every configured digital-asset table read-only and assembles
// the identity graph: one NHI per asset row. With no instance URL or credential
// it returns the offline (empty) graph and a nil error. It never returns
// credential material.
func (s *Source) Snapshot(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceAIControlTower, CapturedAt: s.clock().UTC()}
	if s.offline() {
		return g, nil
	}
	for _, table := range s.tables {
		if err := s.snapshotTable(ctx, table, &g); err != nil {
			return identitysource.Graph{}, err
		}
	}
	return g, nil
}

// snapshotTable pages one table via sysparm_limit/sysparm_offset and appends
// the mapped rows. sysparm_exclude_reference_link keeps reference fields as
// plain values. Paging stops on a short (or empty) page or at the safety bound.
func (s *Source) snapshotTable(ctx context.Context, table string, g *identitysource.Graph) error {
	path := "/api/now/table/" + url.PathEscape(table)
	for page := 0; page < s.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		query := url.Values{
			"sysparm_limit":                  {strconv.Itoa(s.pageSize)},
			"sysparm_offset":                 {strconv.Itoa(page * s.pageSize)},
			"sysparm_exclude_reference_link": {"true"},
		}
		var resp tableResponse
		if err := s.client.GetJSON(ctx, path, query, &resp); err != nil {
			return err
		}
		for _, row := range resp.Result {
			if id, ok := mapRow(table, row); ok {
				g.Identities = append(g.Identities, id)
			}
		}
		if len(resp.Result) < s.pageSize {
			break // short page => last page
		}
	}
	return nil
}

// mapRow maps one tolerant Table API row to an Identity. sys_id is the one
// required key (ServiceNow's natural, stable reference); every other field is
// lifted only when present as a plain string. ok=false skips the row.
func mapRow(table string, row map[string]any) (identitysource.Identity, bool) {
	sysID := str(row, "sys_id")
	if sysID == "" {
		return identitysource.Identity{}, false
	}
	installStatus := str(row, "install_status")
	stageStatus := str(row, "life_cycle_stage_status")
	return identitysource.Identity{
		Ref:         sysID,
		Type:        identitysource.PrincipalNHI,
		Kind:        tableKind(table),
		DisplayName: firstNonEmpty(str(row, "display_name"), str(row, "name"), str(row, "asset_tag"), str(row, "number"), sysID),
		Source:      identitysource.SourceAIControlTower,
		Disabled:    retiredInstallStatuses[installStatus] || retiredStage(stageStatus),
		Attributes: pruneAttrs(map[string]string{
			"table":        table,
			"state":        installStatus,
			"stage":        str(row, "life_cycle_stage"),
			"stage_status": stageStatus,
			// Only a plain "managed" field is lifted; managed_by is a reference to
			// another record (more than this row's metadata), deliberately skipped.
			"managed": str(row, "managed"),
		}),
	}, true
}

// tableKind maps a table name to the Identity.Kind for its rows.
func tableKind(table string) string {
	switch table {
	case tableAISystem:
		return kindAISystemAsset
	case tableMCP:
		return kindMCPAsset
	default:
		return kindAIAsset
	}
}

// retiredStage reports whether a life_cycle_stage_status marks the asset out of
// service ("Retired", "End of Life"), case-insensitively.
func retiredStage(status string) bool {
	l := strings.ToLower(status)
	return strings.Contains(l, "retired") || strings.Contains(l, "end of life")
}

// str lifts a plain string field from a tolerant row; any other type (a nested
// reference object, a number) yields "" — nothing is guessed.
func str(row map[string]any, key string) string {
	v, _ := row[key].(string)
	return v
}

// firstNonEmpty returns the first non-empty value.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// splitTables parses the comma-separated table list, trimming blanks.
func splitTables(v string) []string {
	var out []string
	for _, t := range strings.Split(v, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// transport returns the injected Doer or a per-request-timeout HTTP client,
// so the declared timeout actually bounds production requests (review fix).
func (s *Source) transport() httpx.Doer {
	if s.doer != nil {
		return s.doer
	}
	return &http.Client{Timeout: s.timeout}
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// pruneAttrs drops empty values and returns nil for an empty map, so emitted
// Attributes are diff-stable (the claude-wif convention).
func pruneAttrs(m map[string]string) map[string]string {
	for k, v := range m {
		if v == "" {
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
