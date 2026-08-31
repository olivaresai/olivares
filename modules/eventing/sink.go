// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// This file adds the SIEM-sink extension to the delivery engine. It is
// STRICTLY ADDITIVE: a subscription whose sink_kind is empty is delivered exactly
// as before (the generic, HMAC-signed wireEvent webhook). Only a subscription that
// declares a sink_kind takes the new path, where the engine asks a SinkRenderer to
// re-shape the SAME captured event into the control tower's native dialect
// (OCSF/CEF/LEEF/…) and envelope (Splunk HEC, Sentinel DCR, Datadog, New Relic,
// generic HTTPS) with the sink's own auth header — then ships it over the SAME
// claim → deny-closed RBAC → retry/backoff/DLQ/replay → kill-switch machinery. No
// second delivery engine, no second payload path: just a different on-the-wire
// shaping of the event the engine already holds.

// SinkProfile is the resolved, opened sink configuration for one delivery attempt.
// Kind is empty for the unchanged generic-webhook path. The Cred is the OPENED sink
// credential (Splunk token / Datadog or New Relic key / Sentinel bearer) — distinct
// from the subscription's HMAC signing secret, sealed under its own AAD purpose. The
// engine still signs the rendered body with the HMAC secret (provenance survives
// even for token-authed sinks), so "signed" holds for every sink.
type SinkProfile struct {
	Kind     string
	Format   string
	Cred     string
	Opts     map[string]string
	Endpoint string
}

// SinkEvent is the captured event handed to a renderer. Payload is the typed bus
// payload as stored (a FindingReport JSON for finding.reported, the audit DTO for
// audit.recorded, etc.); the renderer decodes it per Type. It is already
// minimal-data — the renderer only re-shapes, never enriches.
type SinkEvent struct {
	ID      string
	Type    string
	Tenant  string
	Source  string
	Time    time.Time
	Seq     int64
	Payload []byte
}

// SinkRequest is what a renderer produces for one SIEM delivery: the engine still
// owns the transport (the SSRF-guarded client, the retry ladder, the DLQ/replay),
// so the renderer returns only the shaped URL, the sink-auth headers and the body.
// The engine adds the HMAC headers over Body and POSTs through its guarded
// client, re-checking the resolved IP at dial time.
type SinkRequest struct {
	URL     string
	Headers map[string]string
	Body    []byte
}

// SinkRenderer turns one captured event into a SIEM-shaped, sink-authed request.
// It is deny-closed: an unknown kind, an unrenderable format or a credential a
// sink requires-but-lacks returns an error, and the attempt is treated as a
// recoverable config failure (it consumes the retry ladder and dead-letters
// honestly) — NEVER sent unauthenticated or to the wrong place. The composition
// root wires the concrete renderer (it knows the SIEM dialects and lives at the
// modules layer so it may use core/audit's ledger encoder and the connector
// encoders); the eventing platform itself stays free of SIEM specifics.
type SinkRenderer interface {
	Render(ev SinkEvent, profile SinkProfile) (SinkRequest, error)
}

// errNoRenderer is returned (loudly, never silently) when a subscription declares a
// sink_kind but no SinkRenderer was wired — fail-closed, like every other un-wired
// seam in this module.
var errNoRenderer = errors.New("eventing: subscription declares a sink but no SinkRenderer is wired")

// sinkKinds is the closed set of sink kinds a subscription may declare. An empty
// kind is the unchanged generic-webhook path; any other value must be in this set
// (validated at authoring time) so a subscription can never be created for a sink
// the wired renderer cannot speak.
var sinkKinds = map[string]struct{}{
	"https":        {},
	"splunk_hec":   {},
	"sentinel_dcr": {},
	"datadog":      {},
	"newrelic":     {},
}

// validSinkKind reports whether k is a declarable sink kind ("" = generic webhook).
func validSinkKind(k string) bool {
	if k == "" {
		return true
	}
	_, ok := sinkKinds[k]
	return ok
}

// sinkFormats is the closed set of wire dialects a SIEM sink may request — the
// eventing-sink subset of the sdk/siemwire format catalog, DERIVED so this
// module can never disagree with the catalog, the ledger registry or the
// console about which tokens exist. Empty defaults to the surface default (OCSF
// for a SIEM sink — the AI-aware schema), and is irrelevant for the generic-
// webhook path (which always sends the wireEvent JSON). The subset deliberately
// lacks otlp_log_record: a sink POSTs one rendered body per event across ALL
// selected event types, and a bare LogRecord line is not an OTLP /v1/logs
// request — the bare projection stays a pull-export capability.
var sinkFormats = func() map[string]struct{} {
	tokens := siemwire.EventingSinkFormats().Tokens()
	out := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		out[string(t)] = struct{}{}
	}
	return out
}()

// validSinkFormat reports whether f is a declarable sink format ("" allowed = use
// the per-sink default).
func validSinkFormat(f string) bool {
	if f == "" {
		return true
	}
	_, ok := sinkFormats[f]
	return ok
}

// credRequiredFor reports whether a sink kind requires a sealed credential. The
// generic HTTPS sink authenticates via the engine HMAC, so it needs none; the
// vendor sinks each carry a token/key/bearer.
func credRequiredFor(kind string) bool {
	switch kind {
	case "splunk_hec", "sentinel_dcr", "datadog", "newrelic":
		return true
	default:
		return false
	}
}

// sinkRow is the resolved sink profile read from the 1:1 side table in the claim
// transaction. An empty kind means the subscription has no sink row (the unchanged
// generic webhook path).
type sinkRow struct {
	kind       string
	format     string
	opts       string // raw JSON of non-secret routing options
	sealedCred string
}

// resolveSinkProfile reads the OPTIONAL sink side row for a subscription in the
// caller's claim transaction. A subscription with no sink row resolves to the zero
// sinkRow (empty kind) — the generic webhook path. A store error propagates (the
// claim aborts and retries); store.ErrNotFound is impossible here because the read
// is a filtered List, not a Get.
func (m *Module) resolveSinkProfile(ctx context.Context, sc store.Scope, subID model.ID) (sinkRow, error) {
	repo, err := sc.Ext(subscriptionSinkKind)
	if err != nil {
		return sinkRow{}, err
	}
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colSinkSubRef, subID.String())},
		Limit:   1,
	})
	if err != nil {
		return sinkRow{}, err
	}
	if len(recs) == 0 {
		return sinkRow{}, nil
	}
	r := recs[0]
	return sinkRow{
		kind:       r.String(colSinkKind),
		format:     r.String(colSinkFormat),
		opts:       r.String(colSinkOpts),
		sealedCred: r.String(colSinkCred),
	}, nil
}

// renderSink shapes a SIEM-sink delivery: it opens the sink credential (deny-closed
// — a missing renderer parks, an unopenable credential consumes a retry slot and
// self-heals), parses the routing options, and asks the wired SinkRenderer to
// produce the tower's URL, headers and body. The HMAC signing and the POST are done
// by the caller (send), so a SIEM sink rides the SAME transport, retry ladder and
// SSRF guard as a generic webhook. It returns a non-empty (status, outcome) ONLY on
// a build/config failure (the event is then retried/dead-lettered, never sent
// wrong); on success status is "".
func (m *Module) renderSink(ctx context.Context, tenant model.TenantID, at attempt) (string, []byte, map[string]string, string, string) {
	if m.renderer == nil {
		// A sink subscription with no renderer wired: consume a retry slot (it
		// self-heals if the renderer is wired before the ladder exhausts, else it
		// dead-letters honestly into the DLQ for redelivery) — the same recoverable-
		// config-failure shape as a sealed-secret that will not open. Start already
		// warned loudly that SIEM subscriptions cannot deliver.
		return "", nil, nil, statusQueued, outcomeNoRenderer
	}
	cred := ""
	if at.sealedSinkCred != "" {
		c, err := m.openSinkCred(ctx, tenant, at.sealedSinkCred)
		if err != nil {
			// Recoverable: the sealer key may return (self-heals on the ladder), or
			// it dead-letters honestly — never sends unauthenticated.
			return "", nil, nil, statusQueued, outcomeSecretGone
		}
		cred = c
	}
	// Fail CLOSED on routing options we cannot read. The lenient decode returns an
	// empty map for a malformed blob and the renderer then applies its per-sink
	// defaults — which means a corrupted or truncated option silently redirects the
	// delivery to a DIFFERENT index, sourcetype or endpoint than the operator
	// configured, and the delivery is recorded as a success. On a governed egress
	// path that is worse than not delivering: the evidence goes somewhere nobody
	// chose and nothing says so.
	opts, optsErr := parseSinkOptsStrict(at.sinkOpts)
	if optsErr != nil {
		return "", nil, nil, statusQueued, outcomeRenderFailed
	}
	req, err := m.renderer.Render(SinkEvent{
		ID: at.eventID, Type: at.eventType, Tenant: tenant.String(), Source: at.source,
		Time: at.occurredAt, Seq: at.seq, Payload: at.payload,
	}, SinkProfile{
		Kind: at.sinkKind, Format: at.sinkFormat, Cred: cred,
		Opts: opts, Endpoint: at.endpoint,
	})
	if err != nil {
		// A render/config failure (unknown kind, missing routing option): consume
		// the ladder honestly so it surfaces in the DLQ, never a silent wrong send.
		return "", nil, nil, statusQueued, outcomeRenderFailed
	}
	// No syslog transport budget is checked here on purpose: every sink delivery
	// on this path is an HTTPS POST (dispatch enforces https), so the ArcSight
	// syslog-daemon 1024 and QRadar TCP 4096 limits do not apply — a "syslog"
	// sinkFormat here only describes the BODY encoding. The budget lives where the
	// transport really is syslog: connectors/syslog's max_payload_bytes.
	header := map[string]string{"Content-Type": "application/json"}
	for k, v := range req.Headers {
		header[k] = v
	}
	return req.URL, req.Body, header, "", ""
}

// parseSinkOptsStrict decodes the stored routing options and REPORTS a blob it
// cannot read, for the delivery path. A blank value is legal and means "no
// options"; anything else that fails to decode is a stored value that no longer
// says what it used to, and guessing defaults for it would send governed evidence
// to a destination the operator never configured.
func parseSinkOptsStrict(raw string) (map[string]string, error) {
	if raw == "" {
		return nil, nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("eventing: stored sink options are not readable: %w", err)
	}
	// A literal JSON null decodes WITHOUT error into a nil map, which is then
	// indistinguishable from "no options" and silently re-enables the per-sink
	// defaults this guard exists to prevent. A stored value that is present but
	// states nothing is not the same as an absent one.
	if out == nil {
		return nil, fmt.Errorf("eventing: stored sink options decoded to null, which states no routing at all")
	}
	return out, nil
}

// parseSinkOpts decodes the stored JSON routing options into a string map. A blank
// or malformed value yields an empty map.
//
// It stays lenient for the READ path only: an admin listing a subscription should
// still render the rest of the row rather than fail the page over one bad column.
// The delivery path uses parseSinkOptsStrict, because there the same leniency
// silently changes where the data goes.
func parseSinkOpts(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// sinkCredPurpose domain-separates the sealed SIEM credential from the HMAC signing
// secret: both go through the SAME tenant-bound SecretSealer, so a purpose tag on
// the plaintext (verified on open) stops a ciphertext minted for one slot from being
// usable in the other — a defense-in-depth boundary the single-AAD sealer interface
// cannot otherwise express.
const sinkCredPurpose = "olv.sink.cred.v1:"

// sealSinkCred seals a SIEM sink credential under the tenant key with the sink
// purpose tag. An empty credential seals to "" (the generic-HTTPS sink needs none).
func (m *Module) sealSinkCred(ctx context.Context, tenant model.TenantID, cleartext string) (string, error) {
	if cleartext == "" {
		return "", nil
	}
	return m.sealer.Seal(ctx, tenant, []byte(sinkCredPurpose+cleartext))
}

// openSinkCred opens a sealed SIEM sink credential, verifying the purpose tag
// (fail-closed: a value without the tag — e.g. an HMAC secret — is refused).
func (m *Module) openSinkCred(ctx context.Context, tenant model.TenantID, sealed string) (string, error) {
	pt, err := m.sealer.Open(ctx, tenant, sealed)
	if err != nil {
		return "", err
	}
	s, ok := stringsCutPrefix(string(pt), sinkCredPurpose)
	if !ok {
		return "", errors.New("eventing: sealed value is not a sink credential")
	}
	return s, nil
}

// stringsCutPrefix is strings.CutPrefix, inlined to keep the import set tight.
func stringsCutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return s, false
}

// marshalSinkOpts renders the routing options as a deterministic JSON string
// (json.Marshal emits map keys sorted), or "" when empty.
func marshalSinkOpts(opts map[string]string) string {
	if len(opts) == 0 {
		return ""
	}
	b, err := json.Marshal(opts)
	if err != nil {
		return ""
	}
	return string(b)
}

// sinkCredHint returns the non-secret display fingerprint of a sink credential
// (the same scheme as the HMAC secret hint), or "" when there is no credential.
func sinkCredHint(cred string) string {
	if cred == "" {
		return ""
	}
	return hashHex([]byte(cred))[:12]
}

// createSinkRow inserts the 1:1 sink side row for a subscription. sealedCred is the
// already-sealed credential ("" for the generic-HTTPS sink). Called only inside the
// subscription create transaction.
func (m *Module) createSinkRow(ctx context.Context, sc store.Scope, subID model.ID, in *subscriptionRequest, sealedCred string, proof WriterProof) error {
	repo, err := sc.Ext(subscriptionSinkKind)
	if err != nil {
		return err
	}
	rec := model.Record{
		colSinkSubRef: subID.String(),
		colSinkKind:   in.SinkKind,
		colSinkFormat: in.SinkFormat,
		colSinkCred:   sealedCred,
		colSinkOpts:   marshalSinkOpts(in.SinkOpts),
		colSinkHint:   sinkCredHint(in.SinkCred),
	}
	// Unit H: the profile decides the URL the renderer produces, so it is a destination
	// surface and carries the same proof as the endpoint. The proof is built by the handler BEFORE
	// the transaction (WriterProof.writerProof) — reading the fence here would wait on the
	// connection this transaction holds.
	if err := proof.StampInto(ctx, sc, rec); err != nil {
		return err
	}
	_, err = repo.Create(ctx, rec)
	return err
}

// upsertSinkRow creates-or-updates the sink side row for a subscription on update.
// newSealed is the re-sealed credential when the caller supplied a new one, or ""
// to keep the existing sealed credential (no rotation). It enforces the cred
// requirement at write time (a kind that needs a credential must end up with one),
// returning a validation error the handler maps to a 400.
func (m *Module) upsertSinkRow(ctx context.Context, sc store.Scope, subID model.ID, in *subscriptionRequest, newSealed string, proof WriterProof) error {
	repo, err := sc.Ext(subscriptionSinkKind)
	if err != nil {
		return err
	}
	existing, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colSinkSubRef, subID.String())}, Limit: 1})
	if err != nil {
		return err
	}
	sealedCred, hint := newSealed, sinkCredHint(in.SinkCred)
	if newSealed == "" && len(existing) == 1 {
		// No rotation requested: keep the credential already stored.
		sealedCred = existing[0].String(colSinkCred)
		hint = existing[0].String(colSinkHint)
	}
	if credRequiredFor(in.SinkKind) && sealedCred == "" {
		return validationError("sink_cred is required for the " + in.SinkKind + " sink")
	}
	if len(existing) == 1 {
		rec := existing[0]
		moved := rec.String(colSinkKind) != in.SinkKind ||
			rec.String(colSinkFormat) != in.SinkFormat ||
			rec.String(colSinkOpts) != marshalSinkOpts(in.SinkOpts) ||
			rec.String(colSinkCred) != sealedCred
		rec[colSinkKind] = in.SinkKind
		rec[colSinkFormat] = in.SinkFormat
		rec[colSinkCred] = sealedCred
		rec[colSinkOpts] = marshalSinkOpts(in.SinkOpts)
		rec[colSinkHint] = hint
		// Governed when the profile's EFFECTIVE destination moves. kind, format, opts and the
		// credential all feed the renderer, so any of them changing changes where the bytes go;
		// the display hint does not.
		//
		// THIS IS THE SECOND COPY of a rule whose first copy is the trigger's WHEN clause
		// (migrations/*/0006, 0005 on PostgreSQL). They agree by CONSTRUCTION rather than by care:
		// the trigger wraps every nullable column in COALESCE(..., ''), which is exactly how
		// Record.String reads a NULL column here.
		//
		// That was not always so, and the cost of the earlier version was measured: SQL's
		// IS DISTINCT FROM treats NULL as different from '', so a column going from unset to empty
		// fired the trigger while this code, seeing "" on both sides, stamped nothing — and the
		// write was refused for a mutation that moved no destination. Documenting the asymmetry was
		// not enough; normalising it is.
		if moved {
			if err := proof.StampInto(ctx, sc, rec); err != nil {
				return err
			}
		}
		_, err = repo.Update(ctx, rec)
		return err
	}
	rec := model.Record{
		colSinkSubRef: subID.String(),
		colSinkKind:   in.SinkKind,
		colSinkFormat: in.SinkFormat,
		colSinkCred:   sealedCred,
		colSinkOpts:   marshalSinkOpts(in.SinkOpts),
		colSinkHint:   hint,
	}
	if err := proof.StampInto(ctx, sc, rec); err != nil {
		return err
	}
	_, err = repo.Create(ctx, rec)
	return err
}

// clearSinkProfile drops the sink profile of a subscription that REMAINS ALIVE, by updating the row
// to an empty profile rather than deleting it.
//
// This replaces a physical DELETE, and the replacement is a correction an adversarial review of the
// writer fence produced. Deleting the profile "reverts the subscription to a generic webhook" — that
// is, it MOVES the destination from whatever URL the profile rendered to the base endpoint. A fence
// on INSERT and UPDATE never sees that step, so the one mutation that silently re-points a live
// destination was the one that stayed open. Representing it as an UPDATE puts it back inside the
// governed surface, where it belongs, and costs nothing: the delivery path already reads an empty
// sink kind as the generic-webhook case (dispatch.go), so the resulting row behaves exactly as the
// absent row did.
//
// The physical DELETE survives for the case where it is not a re-point at all — see
// deleteSinkRowWithSubscription.
func (m *Module) clearSinkProfile(ctx context.Context, sc store.Scope, subID model.ID, proof WriterProof) error {
	repo, err := sc.Ext(subscriptionSinkKind)
	if err != nil {
		return err
	}
	existing, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colSinkSubRef, subID.String())}, Limit: 1})
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return nil
	}
	rec := existing[0]
	if rec.String(colSinkKind) == "" {
		return nil // already the generic-webhook shape; nothing moves
	}
	rec[colSinkKind] = ""
	rec[colSinkFormat] = ""
	rec[colSinkCred] = ""
	rec[colSinkOpts] = ""
	rec[colSinkHint] = ""
	if err := proof.StampInto(ctx, sc, rec); err != nil {
		return err
	}
	_, err = repo.Update(ctx, rec)
	return err
}

// deleteSinkRowWithSubscription removes the sink side row when the SUBSCRIPTION ITSELF is being
// deleted (idempotent).
//
// This one is not a re-point and needs no proof: there is no destination left to move, because the
// row that carried the endpoint is going away in the same transaction. Keeping the two cases as two
// functions is the point — the caller that deletes a subscription and the caller that merely drops
// its profile were calling one function, and only one of them was safe.
func (m *Module) deleteSinkRowWithSubscription(ctx context.Context, sc store.Scope, subID model.ID) error {
	repo, err := sc.Ext(subscriptionSinkKind)
	if err != nil {
		return err
	}
	existing, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colSinkSubRef, subID.String())}, Limit: 1})
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return nil
	}
	return repo.Delete(ctx, model.ID(existing[0].String(model.ColID)))
}

// applySinkDTO sets the display sink fields on a DTO from a validated request (no
// store read needed — used right after a create/update writes the profile).
func applySinkDTO(dto *subscriptionDTO, in *subscriptionRequest) {
	dto.SinkKind = in.SinkKind
	dto.SinkFormat = in.SinkFormat
	dto.SinkOpts = in.SinkOpts
	dto.SinkCredHint = sinkCredHint(in.SinkCred)
}

// loadSinkDTO reads the sink side row for a subscription and merges its display
// fields into the DTO (never the credential — only the kind/format/opts/hint). A
// subscription with no sink row is left as a generic webhook.
func (m *Module) loadSinkDTO(ctx context.Context, sc store.Scope, dto *subscriptionDTO) error {
	repo, err := sc.Ext(subscriptionSinkKind)
	if err != nil {
		return err
	}
	recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colSinkSubRef, dto.ID)}, Limit: 1})
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return nil
	}
	r := recs[0]
	dto.SinkKind = r.String(colSinkKind)
	dto.SinkFormat = r.String(colSinkFormat)
	dto.SinkOpts = parseSinkOpts(r.String(colSinkOpts))
	dto.SinkCredHint = r.String(colSinkHint)
	return nil
}
