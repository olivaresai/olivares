// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

// The observation history of one catalog entity (first slice).
//
// This file is the Implementation behind one small Interface: listEntityObservations
// takes a tenant-pinned read Scope, the exact catalog (kind, id) pair and a validated
// page request, and returns one observationDTO per DISTINCT C1 receipt that names the
// entity, or refuses. Everything a caller has to know is stated here rather than
// discovered:
//
//   - Selection is done in SQL, before the page: a DistinctProjection of receipt_id
//     under (entity_kind = kind AND entity_id = id), ascending, keyset-anchored on the
//     receipt id. A receipt whose members name the entity twice is one value, so it is
//     one item and can never straddle a page boundary.
//   - The page is 1..observationPageMax receipts. Composing a full page costs at most
//     observationPageMaxRepositoryCalls repository reads (catalog lookup, projection,
//     then per receipt: one Get, one complete member List, one conflict List), all by
//     primary key or indexed prefix. There is no fallback to a row scan: a member
//     repository without store.DistinctProjector is a refusal, never a List.
//   - HasMore proves only that another distinct matching receipt id exists past the
//     page. It certifies nothing about that receipt's integrity; the page that composes
//     it decides.
//   - Every receipt of the page is validated against the C1 writer's own encoding
//     (provenance.go) before any of it is published. One invariant broken anywhere in
//     the page fails the WHOLE page with errObservationEvidence: no item is skipped, no
//     "unknown" is fabricated, and a store.ErrNotFound met while composing is
//     corruption of the page, not the route's 404.
//   - Only request-supplied data yields a client error, and that is decided by the
//     handler before this reader runs. Here, an absent catalog pair is
//     errObservationEntityAbsent and everything else is a server-side failure.
//
// It reads mc.Data (the request's tenant-pinned scope) and nothing else: no roster,
// no Auth, no System scope. registration_state "registered_snapshot" therefore means
// exactly "the receipt recorded a complete registration snapshot when it was
// received" — a historical fact about the observation, not an attestation that the
// source is registered, healthy or readable for the caller today.

const (
	// observationPageDefault and observationPageMax bound one history page. The
	// ceiling is fixed by the accepted design: it is what caps the per-page fan-out
	// below, and raising it is not the remedy if a measured page misses its budget.
	observationPageDefault = 25
	observationPageMax     = 25

	// c1MaxMembers is the most members one V1 receipt may carry (provenance.go
	// catalogMember refuses a fifth). observationMemberProbe asks for ONE more row
	// than that so a fifth row is seen and refused instead of silently truncated;
	// the group must also come back complete (no HasMore) to be believed.
	c1MaxMembers           = 4
	observationMemberProbe = c1MaxMembers + 1

	// observationPageMaxRepositoryCalls is the fixed ceiling of repository reads one
	// full page may issue: the catalog precondition, the distinct projection, and
	// three point/indexed reads per receipt. It is a named constant so a test can
	// hold the Implementation to it rather than to a number remembered in prose.
	observationPageMaxRepositoryCalls = 2 + observationPageMax*3
)

var (
	// errObservationEntityAbsent means the tenant's catalog has no entry for the
	// exact (kind, id) pair. It is the only reader outcome the route answers 404.
	errObservationEntityAbsent = errors.New("inventory: no catalog entry for that kind and id")
	// errObservationEvidence means a receipt selected for the page cannot be
	// proven consistent with the C1 writer's encoding. The route answers a generic
	// 500 for the whole page; the operator log carries the receipt and the reason.
	errObservationEvidence = errors.New("inventory: stored observation evidence cannot be projected")
	// errObservationProjectorUnavailable means the member repository does not offer
	// store.DistinctProjector. The reader fails closed rather than paging rows.
	errObservationProjectorUnavailable = errors.New("inventory: the member repository cannot project distinct receipts")
)

// observationQuery is the validated page request the handler hands the reader.
type observationQuery struct {
	// Limit is 1..observationPageMax.
	Limit int
	// After is the exclusive receipt-id anchor (canonical, nonzero) or "" for the
	// first page.
	After string
}

// observationPage is one composed page: Items are in ascending receipt-id order,
// Cursor is the last receipt id of the page and is set only when HasMore.
type observationPage struct {
	Items   []observationDTO
	Cursor  string
	HasMore bool
}

// evidenceError names the receipt and the invariant that failed. It answers
// errors.Is(err, errObservationEvidence) and DELIBERATELY has no Unwrap: a
// store.ErrNotFound on a receipt Get is evidence corruption and must never reach
// the route's error mapping as "not found".
//
// Its text is built ONLY from a canonical receipt id and one of the static
// evidenceReason sentences below. Nothing read from a row — no facts text, no
// instant text, no decoder or parser message (time.ParseError echoes its input)
// — is ever concatenated in, because the handler writes this text to the operator
// log and a corrupt stored value must not travel there either.
type evidenceError struct {
	receipt string
	reason  evidenceReason
}

func (e *evidenceError) Error() string {
	return fmt.Sprintf("%s: receipt %s: %s", errObservationEvidence.Error(), e.receipt, e.reason)
}

func (e *evidenceError) Is(target error) bool { return target == errObservationEvidence }

// evidenceReason is the closed vocabulary of invariants the reader can refuse on.
// It is a distinct type so that a formatted or concatenated string cannot be
// passed by accident: a plain string needs an explicit evidenceReason(...)
// conversion, which reads as the deliberate act it would be, and the canary
// control (TestC3ObservationsRefusalLogNamesInvariantNotValue) fails the moment a
// stored value travels through one.
type evidenceReason string

const (
	reasonReceiptIDNotCanonical  evidenceReason = "member row carries a non-canonical or zero receipt id"
	reasonReceiptMissing         evidenceReason = "receipt row is missing for a member that references it"
	reasonMemberRowsOverflow     evidenceReason = "more member rows than a V1 receipt can carry"
	reasonFactsDigest            evidenceReason = "stored facts do not digest to facts_hash"
	reasonFactsNotV1             evidenceReason = "facts are not an exact V1 projection"
	reasonFactsVersion           evidenceReason = "facts version is not 1"
	reasonEdgePayload            evidenceReason = "edge receipt without exactly an edge payload"
	reasonCostPayload            evidenceReason = "cost receipt without exactly a cost payload"
	reasonEventType              evidenceReason = "unsupported event type"
	reasonRegistrationState      evidenceReason = "registration_state contradicts the recorded snapshot"
	reasonFirstReception         evidenceReason = "first reception instant is unreadable or non-canonical"
	reasonLastReception          evidenceReason = "last reception instant is unreadable or non-canonical"
	reasonReceptionOrder         evidenceReason = "last reception precedes first reception"
	reasonDeliveries             evidenceReason = "delivery count below one"
	reasonMemberCount            evidenceReason = "member_count outside the V1 bound of 1..4"
	reasonMemberRowsMismatch     evidenceReason = "member rows differ from member_count"
	reasonMemberOrdinal          evidenceReason = "member ordinals are not exactly 0..member_count-1"
	reasonEntityIDNotCanonical   evidenceReason = "member row carries a non-canonical or zero entity id"
	reasonMemberNotV1            evidenceReason = "member facts are not an exact V1 member"
	reasonMemberColumns          evidenceReason = "member facts contradict the member's entity columns"
	reasonMemberOccurrence       evidenceReason = "member occurrence instant contradicts the receipt payload"
	reasonObservationKeyCompute  evidenceReason = "observation key cannot be recomputed"
	reasonMemberAttribution      evidenceReason = "member attribution columns contradict the registration snapshot"
	reasonNoMemberNamesTheEntity evidenceReason = "no member of the receipt names the selected entity"
)

// receiptUnnamed stands in for a receipt id that is NOT a canonical UUID, so the
// stored bytes never reach the log; a canonical id is safe to name.
const receiptUnnamed = "(non-canonical receipt id)"

func corrupt(receipt string, reason evidenceReason) error {
	return &evidenceError{receipt: receipt, reason: reason}
}

// canonicalNonzeroID accepts exactly the canonical lowercase hyphenated form of a
// nonzero UUID: what the store writes and what the projection returns. Braces,
// upper case, the URN prefix and the 32-hex form parse, but they are not the
// stored bytes and are refused here, both for request data (400 at the handler)
// and for stored data (corruption in the reader).
func canonicalNonzeroID(raw string) (model.ID, bool) {
	id, err := model.ParseID(raw)
	if err != nil || id.IsZero() || id.String() != raw {
		return "", false
	}
	return id, true
}

// parseObservationQuery reads ?limit and ?cursor. Each may appear at most once;
// limit must be an integer in canonical decimal form within 1..observationPageMax
// (default observationPageMax when absent); cursor, when present, must be a
// canonical nonzero UUID. Any other shape is a client error decided from the
// request alone, before a row is read.
func parseObservationQuery(values url.Values) (observationQuery, error) {
	q := observationQuery{Limit: observationPageDefault}
	if raw, present := values["limit"]; present {
		if len(raw) != 1 {
			return q, errors.New("limit must appear at most once")
		}
		n, err := strconv.Atoi(raw[0])
		if err != nil || strconv.Itoa(n) != raw[0] || n < 1 || n > observationPageMax {
			return q, fmt.Errorf("limit must be an integer between 1 and %d", observationPageMax)
		}
		q.Limit = n
	}
	if raw, present := values["cursor"]; present {
		if len(raw) != 1 {
			return q, errors.New("cursor must appear at most once")
		}
		id, ok := canonicalNonzeroID(raw[0])
		if !ok {
			return q, errors.New("invalid cursor")
		}
		q.After = id.String()
	}
	return q, nil
}

// listEntityObservations composes one page of the entity's observation history
// inside the caller's tenant-pinned read Scope. See the file comment for the whole
// contract; q must already be validated (the reader refuses a malformed one as a
// server-side error, never as a client one, because it did not come from the wire).
func listEntityObservations(ctx context.Context, sc store.Scope, kind string, entityID model.ID, q observationQuery) (observationPage, error) {
	if q.Limit < 1 || q.Limit > observationPageMax {
		return observationPage{}, fmt.Errorf("inventory: observation page limit %d outside 1..%d", q.Limit, observationPageMax)
	}
	if q.After != "" {
		if _, ok := canonicalNonzeroID(q.After); !ok {
			return observationPage{}, errors.New("inventory: observation page anchor is not a canonical receipt id")
		}
	}
	if entityID.IsZero() {
		return observationPage{}, errors.New("inventory: observation history needs a nonzero entity id")
	}

	// 1. Precondition: the exact catalog pair exists in THIS tenant (call 1).
	catalog, err := sc.Ext(catalogEntryKind)
	if err != nil {
		return observationPage{}, err
	}
	entries, _, err := catalog.List(ctx, model.Query{
		Filters: []model.Filter{eq(colEntityKind, kind), eq(colEntityID, entityID.String())},
		Limit:   1,
	})
	if err != nil {
		return observationPage{}, err
	}
	if len(entries) == 0 {
		return observationPage{}, errObservationEntityAbsent
	}

	// 2. Select DISTINCT receipt ids in SQL, filtered, anchored and limited (call 2).
	members, err := sc.Ext(observationMemberKind)
	if err != nil {
		return observationPage{}, err
	}
	projector, ok := members.(store.DistinctProjector)
	if !ok {
		return observationPage{}, errObservationProjectorUnavailable
	}
	projected, err := projector.ProjectDistinct(ctx, store.DistinctProjection{
		Column:  colReceiptID,
		Filters: []model.Filter{eq(colEntityKind, kind), eq(colEntityID, entityID.String())},
		After:   q.After,
		Limit:   q.Limit,
	})
	if err != nil {
		return observationPage{}, err
	}

	// 3. Compose exactly one item per distinct receipt (three calls each).
	receipts, err := sc.Ext(observationReceiptKind)
	if err != nil {
		return observationPage{}, err
	}
	conflicts, err := sc.Ext(observationConflictKind)
	if err != nil {
		return observationPage{}, err
	}
	page := observationPage{Items: make([]observationDTO, 0, len(projected.Values)), HasMore: projected.HasMore}
	for _, receiptID := range projected.Values {
		item, err := composeObservation(ctx, sc.Tenant(), receipts, members, conflicts, receiptID, kind, entityID)
		if err != nil {
			return observationPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if page.HasMore && len(page.Items) > 0 {
		page.Cursor = page.Items[len(page.Items)-1].ReceiptID
	}
	return page, nil
}

// composeObservation loads and validates ONE receipt with its complete member group
// and its conflict flag, and projects it onto the allowlist. Any invariant it cannot
// prove is an evidenceError for the caller to fail the page with.
func composeObservation(ctx context.Context, tenant model.TenantID, receipts, members, conflicts store.GenericRepo,
	receiptID, kind string, entityID model.ID) (observationDTO, error) {
	id, ok := canonicalNonzeroID(receiptID)
	if !ok {
		return observationDTO{}, corrupt(receiptUnnamed, reasonReceiptIDNotCanonical)
	}
	rec, err := receipts.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return observationDTO{}, corrupt(receiptID, reasonReceiptMissing)
	}
	if err != nil {
		return observationDTO{}, err
	}
	facts, first, last, err := validateReceipt(receiptID, rec)
	if err != nil {
		return observationDTO{}, err
	}

	rows, group, err := members.List(ctx, model.Query{
		Filters: []model.Filter{eq(colReceiptID, receiptID)},
		Limit:   observationMemberProbe,
	})
	if err != nil {
		return observationDTO{}, err
	}
	if group.HasMore {
		return observationDTO{}, corrupt(receiptID, reasonMemberRowsOverflow)
	}
	occurredAt, err := validateMemberGroup(tenant, receiptID, rec, facts, rows, kind, entityID)
	if err != nil {
		return observationDTO{}, err
	}

	variants, _, err := conflicts.List(ctx, model.Query{
		Filters: []model.Filter{eq(colReceiptID, receiptID)},
		Limit:   1,
	})
	if err != nil {
		return observationDTO{}, err
	}

	item := observationDTO{
		ReceiptID:             receiptID,
		EventType:             string(facts.Type),
		Registration:          observationRegistrationDTO{RegistrationState: facts.RegistrationState},
		SourceOccurredAt:      occurredAt,
		FirstReceivedAt:       first,
		LastReceivedAt:        last,
		Deliveries:            rec.Int(colDeliveries),
		ConflictingRedelivery: len(variants) > 0,
	}
	if facts.RegistrationState == registrationRegistered {
		// Only a COMPLETE snapshot publishes its components; "invalid" keeps its
		// partial identity to itself and "unattributed" has none.
		item.Registration.SourceID = facts.Registration.SourceID
		item.Registration.SourceRevision = facts.Registration.SourceRevision
		item.Registration.EnvironmentRef = facts.Registration.EnvironmentRef
	}
	return item, nil
}

// The three registration states the C1 writer records (provenance.go
// newInventoryFacts). They are compared, never invented: a state outside this set
// is corruption, not a fourth public value.
const (
	registrationRegistered   = "registered_snapshot"
	registrationUnattributed = "unattributed"
	registrationInvalid      = "invalid"
)

// validateReceipt proves the receipt row against the C1 encoding and returns the
// decoded facts plus the canonical first/last reception instants. Checked, in
// order: the stored facts text digests to facts_hash; the text decodes EXACTLY as
// inventoryFactsV1 (no unknown field, no trailing data); version is 1; the type is
// one of the two first-party observation types and carries only its own payload
// branch; registration_state agrees with the snapshot's presence and validity;
// the reception instants are canonical and ordered; deliveries is at least one;
// member_count is within 1..c1MaxMembers.
func validateReceipt(receiptID string, rec model.Record) (inventoryFactsV1, string, string, error) {
	var facts inventoryFactsV1
	text := rec.String(colFacts)
	if digest([]byte(text)) != rec.String(colFactsHash) {
		return facts, "", "", corrupt(receiptID, reasonFactsDigest)
	}
	if err := decodeExactly(text, &facts); err != nil {
		return facts, "", "", corrupt(receiptID, reasonFactsNotV1)
	}
	if facts.Version != 1 {
		return facts, "", "", corrupt(receiptID, reasonFactsVersion)
	}
	switch facts.Type {
	case event.TypeEdgeObserved:
		if facts.Edge == nil || facts.Cost != nil {
			return facts, "", "", corrupt(receiptID, reasonEdgePayload)
		}
	case event.TypeCostSampled:
		if facts.Cost == nil || facts.Edge != nil {
			return facts, "", "", corrupt(receiptID, reasonCostPayload)
		}
	default:
		return facts, "", "", corrupt(receiptID, reasonEventType)
	}
	want := registrationUnattributed
	if facts.Registration != nil {
		want = registrationInvalid
		if facts.Registration.Valid() {
			want = registrationRegistered
		}
	}
	if facts.RegistrationState != want {
		return facts, "", "", corrupt(receiptID, reasonRegistrationState)
	}
	first, err := canonicalInstant(rec.String(colFirstSeen))
	if err != nil {
		return facts, "", "", corrupt(receiptID, reasonFirstReception)
	}
	last, err := canonicalInstant(rec.String(colLastSeen))
	if err != nil {
		return facts, "", "", corrupt(receiptID, reasonLastReception)
	}
	if last.Before(first) {
		return facts, "", "", corrupt(receiptID, reasonReceptionOrder)
	}
	if rec.Int(colDeliveries) < 1 {
		return facts, "", "", corrupt(receiptID, reasonDeliveries)
	}
	if n := rec.Int(colMemberCount); n < 1 || n > c1MaxMembers {
		return facts, "", "", corrupt(receiptID, reasonMemberCount)
	}
	return facts, first.String(), last.String(), nil
}

// validateMemberGroup proves the receipt's COMPLETE member group and returns the
// source-declared occurrence instant the page publishes for this entity. Checked:
// the group has exactly member_count rows whose ordinals are 0..member_count-1
// without gap or repeat; every row's entity id is canonical and nonzero; every
// row's JSON decodes exactly as observationMember and agrees with the row's
// entity_kind/entity_id columns; every member's occurred_at equals the receipt
// payload's own instant (empty included), so two members naming the same entity
// cannot yield two public times; each row's source_id and observation_key are
// exactly what the C1 rule materializes for that registration and native
// reference; and at least one row names the selected entity exactly.
func validateMemberGroup(tenant model.TenantID, receiptID string, rec model.Record, facts inventoryFactsV1,
	rows []model.Record, kind string, entityID model.ID) (string, error) {
	n := int(rec.Int(colMemberCount))
	if len(rows) != n {
		return "", corrupt(receiptID, reasonMemberRowsMismatch)
	}
	payloadTime := ""
	if facts.Edge != nil {
		payloadTime = facts.Edge.OccurredAt
	} else if facts.Cost != nil {
		payloadTime = facts.Cost.OccurredAt
	}
	seen := make([]bool, n)
	matched := 0
	for _, row := range rows {
		ordinal := row.Int(colMemberOrdinal)
		if ordinal < 0 || ordinal >= int64(n) || seen[ordinal] {
			return "", corrupt(receiptID, reasonMemberOrdinal)
		}
		seen[ordinal] = true
		rowKind, rowEntity := row.String(colEntityKind), row.String(colEntityID)
		if _, ok := canonicalNonzeroID(rowEntity); !ok {
			return "", corrupt(receiptID, reasonEntityIDNotCanonical)
		}
		var member observationMember
		if err := decodeExactly(row.String(colFacts), &member); err != nil {
			return "", corrupt(receiptID, reasonMemberNotV1)
		}
		if member.Kind != rowKind || member.EntityID.String() != rowEntity {
			return "", corrupt(receiptID, reasonMemberColumns)
		}
		if member.OccurredAt != payloadTime {
			return "", corrupt(receiptID, reasonMemberOccurrence)
		}
		wantSource, wantKey := "", ""
		if facts.RegistrationState == registrationRegistered && member.Native.Ref != "" {
			wantSource = facts.Registration.SourceID
			key, err := observationKey(tenant, wantSource, member.Kind, member.Native)
			if err != nil {
				return "", corrupt(receiptID, reasonObservationKeyCompute)
			}
			wantKey = key
		}
		if row.String(colSourceID) != wantSource || row.String(colObservationKey) != wantKey {
			return "", corrupt(receiptID, reasonMemberAttribution)
		}
		if rowKind == kind && rowEntity == entityID.String() {
			matched++
		}
	}
	if matched == 0 {
		return "", corrupt(receiptID, reasonNoMemberNamesTheEntity)
	}
	return payloadTime, nil
}

// observationKey recomputes the stable source-qualified identity exactly as the
// C1 writer materializes it (provenance.go persistObservation): the digest of the
// JSON of {version 1, tenant, source id, kind, native reference}. It exists so the
// reader can hold a stored key to the writer's rule instead of trusting the column.
func observationKey(tenant model.TenantID, sourceID, kind string, native nativeReference) (string, error) {
	key, err := json.Marshal(struct {
		Version        int
		Tenant         model.TenantID
		SourceID, Kind string
		Native         nativeReference
	}{1, tenant, sourceID, kind, native})
	if err != nil {
		return "", err
	}
	return digest(key), nil
}

// decodeExactly decodes text into v as ONE JSON value with no unknown field and
// nothing after it. The stored projections are written from these very structs, so
// exactness costs a legitimate row nothing and refuses any other shape.
func decodeExactly(text string, v any) error {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

// canonicalInstant parses a stored instant and requires it to be the canonical
// text the store writes (fixed-width UTC), so an instant that would sort or hash
// differently from what the writer produced is refused rather than reformatted.
func canonicalInstant(raw string) (model.Timestamp, error) {
	ts, err := model.ParseTimestamp(raw)
	if err != nil {
		return model.Timestamp{}, err
	}
	if ts.String() != raw {
		return model.Timestamp{}, errors.New("non-canonical instant text")
	}
	return ts, nil
}
