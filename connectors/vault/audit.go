// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Vault audit-log ingest. AuditSource is the OBSERVED counterpart of the
// roster Source in vault.go: where Source's Gather expands ACL policies into
// PERMITTED grants (model.SignalPolicy), AuditSource tails the file audit
// device's JSON log and emits the accesses that actually HAPPENED
// (SignalVaultAudit). Both deliberately share the same "entity:<name>" origin
// ref space and the same "vault.path" resource kind, so module III can diff
// permitted-vs-observed (ARCHITECTURE.md) without any mapping layer: an edge present
// on the policy side but never on the audit side is over-provisioned access; an
// audit edge with no policy edge is shadow access.
//
// It is read-only and minimal-data (docs/SECURITY-HARDENING.md-3): the log file is opened
// O_RDONLY via connectors/internal/logtail (never written), and the parser maps
// ONLY the cleartext principal/access metadata. Verified against the audit-log
// schema at developer.hashicorp.com/vault/docs/audit/schema on 2026-06-11
// (unchanged in the 2.x docs tree): the file audit device writes one JSON
// object per line; entries are type=="request"|"response" correlated by
// request.id (a response entry embeds the full request object, so no join is
// needed here); principal fields arrive CLEARTEXT (auth.entity_id,
// auth.display_name, auth.policies, request.client_id), as do the access
// fields (request.path, request.operation create/read/update/delete/list,
// request.mount_class "auth"|"secret", request.mount_type,
// request.namespace.id, time as RFC3339). The SENSITIVE fields
// (auth.client_token, auth.accessor, request.data string values) arrive HMAC'd
// by default ("hmac-sha256:…") — this parser NEVER lifts any of them into an
// observation: they are not even decoded from the wire shape.
//
// Only COMPLETED accesses are emitted: type=="response" entries with an
// empty/absent error. Request entries (the access has not happened yet) and
// failed responses (the access was denied or errored) are skipped — the
// observed side of the diff carries facts, not attempts.
//
// Unlike the credentialed roster connectors, there is no "offline" mode here:
// `path` is REQUIRED and Open fails without it (the pgaudit hard-fail
// precedent — a misconfigured audit source must never degrade into a silent
// no-op). The optional Vault token is used ONLY when resolve_entities is set,
// for a read-only GET /v1/identity/entity/id/{id} (the vault.go httpx pattern)
// that upgrades an entity UUID to its roster name.
package vault

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/connectors/internal/logtail"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// AuditName is the audit-ingest connector's globally unique identifier.
const AuditName = "olivares.vault-audit"

// SignalVaultAudit is the package-local SignalSource for the observed audit
// edges. It is an open string by the S02 §6 convention (see connectors/flux):
// a new provenance value needs no SDK release and the sdk/model enum stays
// untouched. It is distinct from model.SignalPolicy (the permitted side emitted
// by Source.Gather) by design — the diff needs both provenances.
const SignalVaultAudit model.SignalSource = "vault_audit"

// AuditSource tails a Vault file-audit-device JSON log and emits one
// EdgeObservation per completed access. It satisfies sdk.SourceConnector ONLY:
// the identity roster half already lives on Source (vault.go), and an audit
// trail carries no roster. The zero value is not usable; call NewAudit.
type AuditSource struct {
	path             string
	follow           bool
	resolveEntities  bool
	baseURL          string
	token            string
	namespace        string
	secretMountsOnly bool
	timeout          time.Duration

	client *httpx.Client // built in Open when resolve_entities; GET-only
	doer   httpx.Doer    // injected transport (tests); nil => default
}

// Compile-time proof that AuditSource satisfies the SDK contract (and only it).
var _ sdk.SourceConnector = (*AuditSource)(nil)

// NewAudit returns a Vault audit-log connector with default configuration.
func NewAudit() *AuditSource {
	return &AuditSource{
		baseURL:          defaultBaseURL,
		secretMountsOnly: true,
		timeout:          defaultTimeout,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *AuditSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        AuditName,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "HashiCorp Vault audit log",
		Description: "Tails the Vault file audit device (JSON lines) and emits observed identity→secret-path accesses; the observed counterpart of the vault policy grants. Never reads secret values.",
		ConfigFields: []sdk.ConfigField{
			{Key: "path", Type: sdk.FieldString, Required: true, Description: "Path to the Vault file audit device log (JSON lines). Required."},
			{Key: "follow", Type: sdk.FieldBool, Default: "false", Description: "Tail the log continuously (Gather blocks); false reads to EOF and returns."},
			{Key: "resolve_entities", Type: sdk.FieldBool, Default: "false", Description: "Resolve entity UUIDs to names via GET /v1/identity/entity/id/{id} (needs base_url/token), so refs converge with the roster."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Vault API base URL (only used when resolve_entities)."},
			{Key: "token", Type: sdk.FieldString, Secret: true, Description: "Vault token reference for entity resolution (X-Vault-Token; read-only; never persisted; only used when resolve_entities)."},
			{Key: "namespace", Type: sdk.FieldString, Description: "Vault Enterprise namespace (X-Vault-Namespace header; only used when resolve_entities)."},
			{Key: "secret_mounts_only", Type: sdk.FieldBool, Default: "true", Description: "Emit only secret-mount accesses (request.mount_class==\"secret\"; older logs without mount_class fall back to skipping sys/ and auth/ paths)."},
			{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Per-resolution-request timeout (advisory)."},
		},
	}
}

// Open reads and validates configuration. A missing path is a HARD configuration
// error (the pgaudit precedent): a misconfigured audit source must never become
// a silent no-op. The token is optional and only powers entity resolution; its
// absence is not an error (resolution simply stays off / falls back).
func (s *AuditSource) Open(_ context.Context, cfg sdk.Config) error {
	s.path = strings.TrimSpace(cfg.Get("path"))
	if s.path == "" {
		return errors.New("vault-audit: path is required")
	}
	s.follow = cfg.GetBool("follow", false)
	s.resolveEntities = cfg.GetBool("resolve_entities", false)
	if v := cfg.Get("base_url"); v != "" {
		s.baseURL = v
	}
	s.token = cfg.Get("token")
	s.namespace = cfg.Get("namespace")
	s.secretMountsOnly = cfg.GetBool("secret_mounts_only", true)
	s.timeout = cfg.GetDuration("timeout", s.timeout)

	if s.resolveEntities {
		var headers map[string]string
		if s.namespace != "" {
			headers = map[string]string{headerNamespace: s.namespace}
		}
		s.client = httpx.New(s.baseURL, s.doer, httpx.Header(headerToken, s.token, s.token), headers)
	}
	return nil
}

// Close releases resources; this connector holds none between Gather runs.
func (s *AuditSource) Close(context.Context) error { return nil }

// auditEntry is the slice of one audit-log line the connector reads. The HMAC'd
// sensitive fields (auth.client_token, auth.accessor, request.data) are
// DELIBERATELY absent: the parser cannot lift what it never decodes.
type auditEntry struct {
	Type  string `json:"type"`
	Time  string `json:"time"`
	Error string `json:"error"`
	Auth  struct {
		EntityID    string `json:"entity_id"`
		DisplayName string `json:"display_name"`
	} `json:"auth"`
	Request struct {
		Operation  string `json:"operation"`
		Path       string `json:"path"`
		MountClass string `json:"mount_class"`
	} `json:"request"`
}

// Gather tails the configured audit log and emits one EdgeObservation per
// completed access. With follow=false it reads to EOF and returns nil (batch);
// with follow=true it blocks tailing until ctx is done. Non-JSON lines are
// tolerated and skipped (an audit log can interleave device noise); malformed
// records never abort the ingest.
func (s *AuditSource) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.path == "" {
		return errors.New("vault-audit: not opened (path is required)")
	}
	// Entity-name resolution cache, per Gather run: one GET per unique id, and a
	// negative result is cached too so a deleted entity is not re-fetched per line.
	cache := map[string]string{}

	return logtail.Tail(ctx, s.path, logtail.Options{Follow: s.follow}, func(line []byte) error {
		var e auditEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil // tolerant: skip non-JSON lines
		}
		edge, ok := s.buildEdge(ctx, cache, e)
		if !ok {
			return nil
		}
		return sink.Emit(ctx, edge)
	})
}

// buildEdge maps one audit entry to an EdgeObservation, or ok=false when the
// entry is not an emittable completed access.
func (s *AuditSource) buildEdge(ctx context.Context, cache map[string]string, e auditEntry) (model.EdgeObservation, bool) {
	// Completed accesses only: a request entry is an attempt in flight, and a
	// response with an error is a denial/failure — neither is an observed access.
	if e.Type != "response" || e.Error != "" {
		return model.EdgeObservation{}, false
	}
	if e.Request.Path == "" {
		return model.EdgeObservation{}, false // no resource to anchor the edge
	}

	// Mount filter. mount_class is authoritative when present; older Vault logs
	// without it fall back to skipping the sys/ and auth/ system prefixes.
	if s.secretMountsOnly {
		if e.Request.MountClass != "" {
			if e.Request.MountClass != "secret" {
				return model.EdgeObservation{}, false
			}
		} else if strings.HasPrefix(e.Request.Path, "sys/") || strings.HasPrefix(e.Request.Path, "auth/") {
			return model.EdgeObservation{}, false
		}
	}

	mode, ok := operationMode(e.Request.Operation)
	if !ok {
		return model.EdgeObservation{}, false
	}

	ts, err := time.Parse(time.RFC3339, e.Time)
	if err != nil {
		return model.EdgeObservation{}, false // no usable natural-key timestamp
	}

	ref, conf, ok := s.origin(ctx, cache, e)
	if !ok {
		return model.EdgeObservation{}, false
	}

	return model.EdgeObservation{
		OriginKind:   "identity",
		OriginRef:    ref,
		ResourceKind: "vault.path",
		ResourceRef:  e.Request.Path,
		Mode:         mode,
		Source:       SignalVaultAudit,
		Confidence:   conf,
		ObservedAt:   ts,
	}, true
}

// origin derives the edge origin from the entry's cleartext principal fields.
//
//   - auth.entity_id set: resolve it to the entity NAME when resolve_entities is
//     on (cached per run) → "entity:<name>", Attributed. Unresolved (resolution
//     off, or the GET failed) → "entity:<id>", Approximate. vault.go's
//     fetchEntities falls back to the id as the name for NAMELESS entities, so
//     the unresolved ref converges with the roster for exactly those; for named
//     entities it is honestly approximate until resolution is enabled.
//   - entity_id empty but auth.display_name set (a root/orphan token with no
//     entity): "token:<display_name>", Approximate — a display name is not a
//     stable identity.
//   - neither: not attributable, skip.
func (s *AuditSource) origin(ctx context.Context, cache map[string]string, e auditEntry) (string, model.Confidence, bool) {
	switch {
	case e.Auth.EntityID != "":
		if s.resolveEntities {
			if name, ok := s.resolveEntity(ctx, cache, e.Auth.EntityID); ok {
				return "entity:" + name, model.ConfidenceAttributed, true
			}
		}
		return "entity:" + e.Auth.EntityID, model.ConfidenceApproximate, true
	case e.Auth.DisplayName != "":
		return "token:" + e.Auth.DisplayName, model.ConfidenceApproximate, true
	default:
		return "", "", false
	}
}

// resolveEntity maps an entity UUID to its name via a read-only
// GET /v1/identity/entity/id/{id}, cached per Gather run. Only a DEFINITIVE
// negative (404 — the entity is gone; 403 — unreadable for this token) is
// negative-cached as "": a transient failure (5xx, transport, timeout) is NOT
// cached, so the next line carrying the same id retries and the ingest
// self-heals — under follow=true a tail can outlive a Vault hiccup by hours,
// and a run-scoped negative cache would degrade every later line of that
// entity to approximate forever. A nameless entity resolves to its id — the
// same fallback vault.go's fetchEntities applies to the roster, so the refs
// converge. A failed GET is best-effort either way: the caller falls back to
// the approximate id ref rather than aborting the ingest.
func (s *AuditSource) resolveEntity(ctx context.Context, cache map[string]string, id string) (string, bool) {
	if name, seen := cache[id]; seen {
		return name, name != ""
	}
	if s.client == nil || s.token == "" {
		return "", false // resolution wired but unusable; fall back, do not call
	}
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	var resp entityResponse
	if err := s.client.GetJSON(ctx, "/v1/identity/entity/id/"+url.PathEscape(id), nil, &resp); err != nil {
		var se *httpx.StatusError
		if errors.As(err, &se) && (se.Status == http.StatusNotFound || se.Status == http.StatusForbidden) {
			cache[id] = "" // definitive negative: one GET, not one per line
		}
		return "", false
	}
	name := resp.Data.Name
	if name == "" {
		name = id // the vault.go nameless-entity fallback: ref converges with the roster
	}
	cache[id] = name
	return name, true
}

// operationMode maps an audit request.operation to an access mode: read|list
// are reads, create|update|delete|patch are writes; anything else (unknown or
// forward-compat operations) is skipped, never guessed.
func operationMode(op string) (model.AccessMode, bool) {
	switch op {
	case "read", "list":
		return model.ModeRead, true
	case "create", "update", "delete", "patch":
		return model.ModeWrite, true
	default:
		return model.ModeUnknown, false
	}
}
