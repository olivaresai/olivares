// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

// D02 attempt lifecycle — the two durable descriptors and their explicit codecs.
//
// Both entities are declared in their COMPLETE contract shape from this first cut,
// even though only the imported-hold subset is ever written here. The alternative —
// adding the settlement columns when settlement arrives — would need a second
// schema change on a table that by then holds money-bearing rows, and the engine's
// additive reconciliation cannot add a NOT NULL column to a populated table. The
// columns a later cut fills are therefore NULLABLE and start NULL; nothing in this
// file writes them.
//
// The JSON columns do NOT hold json.Marshal of the Go structs. They hold an
// explicit document whose integers are decimal STRINGS, whose optional objects are
// absent rather than zero-valued, and whose decoder rejects an unknown field. A
// driver that hands back float64 for a monetary integer cannot corrupt a value that
// was never written as a JSON number.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The two new owned entities.
const (
	// attemptKind is the metadata PARENT of one attempt. It carries no money: the
	// amounts live on the reservation children, and no read ever sums this table.
	attemptKind model.Kind = "finops.attempt"
	// lifecycleScopeKind is the durable activation boundary — exactly one row per
	// business tenant, metadata only. It is not a lease, a cluster generation or a
	// global switch.
	lifecycleScopeKind model.Kind = "finops.lifecycle_scope"
)

const (
	attemptTable        = "finops_attempt"
	lifecycleScopeTable = "finops_lifecycle_scope"
)

// finops.attempt columns.
const (
	colAttemptContractVersion = "contract_version"
	colAttemptRef             = "attempt_ref"
	colAttemptRequestRef      = "request_ref"
	colAttemptHandle          = "handle"
	colAttemptPhase           = "phase"
	colAttemptBindingDigest   = "binding_digest"
	colAttemptBinding         = "binding"
	colAttemptTargets         = "targets"
	colAttemptResvDigest      = "reservation_digest"
	colAttemptEffectDigest    = "effect_digest"
	colAttemptDispatchRef     = "dispatch_ref"
	colAttemptAccountingAt    = "accounting_at"
	colAttemptDispatchMarked  = "dispatch_marked_at"
	colAttemptReviewAfter     = "review_after"
	colAttemptSettledAt       = "settled_at"
	colAttemptOwnerRef        = "owner_ref"
	colAttemptOwnerEpoch      = "owner_epoch"
	colAttemptOutcome         = "outcome"
	colAttemptOutcomeDigest   = "outcome_digest"
	colAttemptSettlementDgst  = "settlement_digest"
	colAttemptSampleKey       = "sample_key"
	colAttemptSampleID        = "sample_id"
	colAttemptCostRecordID    = "cost_record_id"
	colAttemptPublication     = "publication_state"
	colAttemptResolutionEvid  = "resolution_evidence"
	colAttemptLegacyHandles   = "legacy_handles"
	colAttemptAccountingBasis = "accounting_basis"
)

// finops.lifecycle_scope columns.
const (
	colScopeState      = "state"
	colScopeFrontierAt = "frontier_at"
	colScopeActivated  = "activated_at"
	colScopeFrontier   = "frontier"
	colScopeActivation = "activation_evidence"
)

// finops.budget_reservation ADDITIVE lifecycle linkage (both nullable; a legacy
// row keeps NULL in both and is read by the legacy branch).
const (
	colResvAttemptRef       = "attempt_ref"
	colResvLifecycleVersion = "lifecycle_version"
)

// attemptContractVersion is the durable shape version of a parent row.
const attemptContractVersion int64 = 1

// lifecycleLinkageVersion marks a reservation child as belonging to the v1
// lifecycle. A row is legacy ONLY when BOTH linkage columns are NULL; an empty
// string, an unknown version or one column set without the other is malformed, not
// legacy.
const lifecycleLinkageVersion int64 = 1

// registerAttemptSchema declares the attempt parent and the lifecycle scope. It is
// called from RegisterSchema.
func registerAttemptSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  attemptKind,
		Table: attemptTable,
		Fields: []model.FieldSpec{
			{Name: colAttemptContractVersion, Kind: model.KindInt},
			{Name: colAttemptRef, Kind: model.KindText},
			{Name: colAttemptRequestRef, Kind: model.KindText},
			{Name: colAttemptHandle, Kind: model.KindUUID},
			{Name: colAttemptPhase, Kind: model.KindText},
			{Name: colAttemptBindingDigest, Kind: model.KindText},
			{Name: colAttemptBinding, Kind: model.KindJSON},
			{Name: colAttemptTargets, Kind: model.KindJSON},
			{Name: colAttemptResvDigest, Kind: model.KindText},
			// Everything below is a LATER phase's field. Declared now, NULL now: the
			// additive reconciliation can add a nullable column to a populated table
			// and cannot add a NOT NULL one, so declaring the complete shape here is
			// what keeps the settlement cut from needing a hand-authored migration
			// over money-bearing rows.
			{Name: colAttemptEffectDigest, Kind: model.KindText, Nullable: true},
			{Name: colAttemptDispatchRef, Kind: model.KindText, Nullable: true},
			{Name: colAttemptAccountingAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colAttemptDispatchMarked, Kind: model.KindTimestamp, Nullable: true},
			{Name: colAttemptReviewAfter, Kind: model.KindTimestamp},
			{Name: colAttemptSettledAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colAttemptOwnerRef, Kind: model.KindText},
			{Name: colAttemptOwnerEpoch, Kind: model.KindInt},
			{Name: colAttemptOutcome, Kind: model.KindJSON, Nullable: true},
			{Name: colAttemptOutcomeDigest, Kind: model.KindText, Nullable: true},
			{Name: colAttemptSettlementDgst, Kind: model.KindText, Nullable: true},
			{Name: colAttemptSampleKey, Kind: model.KindText, Nullable: true},
			{Name: colAttemptSampleID, Kind: model.KindUUID, Nullable: true},
			{Name: colAttemptCostRecordID, Kind: model.KindUUID, Nullable: true},
			{Name: colAttemptPublication, Kind: model.KindText},
			{Name: colAttemptResolutionEvid, Kind: model.KindJSON, Nullable: true},
			{Name: colAttemptLegacyHandles, Kind: model.KindJSON, Nullable: true},
			{Name: colAttemptAccountingBasis, Kind: model.KindJSON},
		},
		Indexes: []model.IndexSpec{
			{
				// The accounting identity. Tenant-leading.
				Name:    "finops_attempt_ref_uniq",
				Columns: []string{model.ColTenantID, colAttemptRef},
				Unique:  true,
			},
			{
				// One reservation handle is owned by exactly ONE parent. This index is
				// what makes a second import of the same handle a database conflict
				// rather than a second obligation.
				Name:    "finops_attempt_handle_uniq",
				Columns: []string{model.ColTenantID, colAttemptHandle},
				Unique:  true,
			},
			{
				Name:    "finops_attempt_due_idx",
				Columns: []string{model.ColTenantID, colAttemptPhase, colAttemptReviewAfter},
			},
		},
	}); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:  lifecycleScopeKind,
		Table: lifecycleScopeTable,
		Fields: []model.FieldSpec{
			{Name: colScopeState, Kind: model.KindText},
			{Name: colScopeFrontierAt, Kind: model.KindTimestamp},
			{Name: colScopeActivated, Kind: model.KindTimestamp, Nullable: true},
			{Name: colScopeFrontier, Kind: model.KindJSON},
			{Name: colScopeActivation, Kind: model.KindJSON, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// EXACTLY ONE per tenant. The uniqueness is the whole mechanism: a second
			// Begin cannot create a second frontier beside the first.
			Name:    "finops_lifecycle_scope_tenant_uniq",
			Columns: []string{model.ColTenantID},
			Unique:  true,
		}},
	})
}

// -----------------------------------------------------------------------------
// The explicit JSON codec
// -----------------------------------------------------------------------------
//
// One rule, applied without exception: an int64 is encoded as a decimal STRING and
// decoded by strconv, and an unknown field is an error. json.Number would be
// enough for the decoder, but it would not stop a future writer from emitting a
// JSON number that some driver later re-reads as a float64.

// jsonInt is an int64 that travels as a decimal string.
type jsonInt int64

func (v jsonInt) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatInt(int64(v), 10))
}

func (v *jsonInt) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("finops: integer field is not a decimal string: %w", err)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("finops: integer field %q is not a canonical int64", s)
	}
	// Round-trip check: "007", "+7" and " 7" all parse, and none of them is the
	// canonical form this codec writes. A value that does not re-render to the
	// bytes it arrived as was not written by this codec.
	if strconv.FormatInt(n, 10) != s {
		return fmt.Errorf("finops: integer field %q is not canonical decimal", s)
	}
	*v = jsonInt(n)
	return nil
}

// jsonEvidenceRef is the wire shape of an EvidenceRef.
type jsonEvidenceRef struct {
	Kind     string  `json:"kind"`
	Ref      string  `json:"ref"`
	Digest   string  `json:"digest"`
	AuditSeq jsonInt `json:"audit_seq"`
	Version  jsonInt `json:"version"`
}

func encodeEvidenceRefs(refs []EvidenceRef) []jsonEvidenceRef {
	// A nil slice and an empty slice both encode as [] and decode back to an empty
	// slice: presence of the FIELD is what carries meaning here, not the difference
	// between nil and len 0, and the canonical framing treats them identically too.
	out := make([]jsonEvidenceRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, jsonEvidenceRef{
			Kind: r.Kind, Ref: r.Ref, Digest: string(r.Digest),
			AuditSeq: jsonInt(r.AuditSeq), Version: jsonInt(r.Version),
		})
	}
	return out
}

func decodeEvidenceRefs(in []jsonEvidenceRef) ([]EvidenceRef, error) {
	out := make([]EvidenceRef, 0, len(in))
	for _, r := range in {
		ref := EvidenceRef{
			Kind: r.Kind, Ref: r.Ref, Digest: Digest(r.Digest),
			AuditSeq: int64(r.AuditSeq), Version: int64(r.Version),
		}
		if !ref.valid() {
			return nil, fmt.Errorf("finops: stored evidence reference is malformed")
		}
		out = append(out, ref)
	}
	return out, nil
}

type jsonAttributionFact struct {
	State             string            `json:"state"`
	Values            []string          `json:"values"`
	Evidence          []jsonEvidenceRef `json:"evidence"`
	NotApplicableRule string            `json:"not_applicable_rule"`
}

type jsonSubject struct {
	ActorRef            string            `json:"actor_ref"`
	ActorKind           string            `json:"actor_kind"`
	UserID              string            `json:"user_id"`
	CredentialID        string            `json:"credential_id"`
	AgentIdentity       string            `json:"agent_identity"`
	SessionIdentity     string            `json:"session_identity"`
	SessionWorkspaceID  string            `json:"session_workspace_id"`
	SessionRunRef       string            `json:"session_run_ref"`
	SessionFence        jsonInt           `json:"session_fence"`
	DelegationRefs      []jsonEvidenceRef `json:"delegation_refs"`
	UntrustedSessionRef string            `json:"untrusted_session_ref"`
}

type jsonDestination struct {
	ProfileRef         string  `json:"profile_ref"`
	ProfileRevision    string  `json:"profile_revision"`
	ProviderRef        string  `json:"provider_ref"`
	ModelRef           string  `json:"model_ref"`
	Action             string  `json:"action"`
	Protocol           string  `json:"protocol"`
	AdapterID          string  `json:"adapter_id"`
	AdapterVersion     string  `json:"adapter_version"`
	Surface            string  `json:"surface"`
	InferenceGeo       string  `json:"inference_geo"`
	CredentialAudience string  `json:"credential_audience"`
	AuthScheme         string  `json:"auth_scheme"`
	EndpointDigest     string  `json:"endpoint_digest"`
	TransportDigest    string  `json:"transport_digest"`
	PolicyID           string  `json:"policy_id"`
	PolicyVersion      jsonInt `json:"policy_version"`
	PolicySpecDigest   string  `json:"policy_spec_digest"`
	ProxyPolicyDigest  string  `json:"proxy_policy_digest"`
	PreparedDigest     string  `json:"prepared_digest"`
	PreparedBytes      jsonInt `json:"prepared_bytes"`
	MaxOutputTokens    jsonInt `json:"max_output_tokens"`
	MaxRequestBytes    jsonInt `json:"max_request_bytes"`
	MaxResponseBytes   jsonInt `json:"max_response_bytes"`
	TimeoutNanos       jsonInt `json:"timeout_nanos"`
}

type jsonEstimate struct {
	AmountMicroUSD   jsonInt           `json:"amount_micro_usd"`
	Method           string            `json:"method"`
	Revision         string            `json:"revision"`
	InputBound       *jsonInt          `json:"input_bound,omitempty"`
	OutputBound      *jsonInt          `json:"output_bound,omitempty"`
	RateRefs         []jsonEvidenceRef `json:"rate_refs"`
	PriceDigest      string            `json:"price_digest"`
	QualificationRef jsonEvidenceRef   `json:"qualification_ref"`
}

type jsonBinding struct {
	Status          string                         `json:"status"`
	RequestRef      string                         `json:"request_ref"`
	PredecessorRef  *string                        `json:"predecessor_ref,omitempty"`
	Subject         jsonSubject                    `json:"subject"`
	Entities        jsonEntities                   `json:"entities"`
	Destination     jsonDestination                `json:"destination"`
	Attribution     map[string]jsonAttributionFact `json:"attribution"`
	Estimate        jsonEstimate                   `json:"estimate"`
	ApplySeatLimits bool                           `json:"apply_seat_limits"`
	AuthorityRefs   []jsonEvidenceRef              `json:"authority_refs"`
}

type jsonEntities struct {
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`
	SessionID  string `json:"session_id"`
	AgentID    string `json:"agent_id"`
}

type jsonTarget struct {
	ChildID                string            `json:"child_id"`
	PolicyID               string            `json:"policy_id"`
	PolicyKind             string            `json:"policy_kind"`
	Dimension              string            `json:"dimension"`
	ScopeKey               string            `json:"scope_key"`
	Period                 string            `json:"period"`
	PolicyVersion          *jsonInt          `json:"policy_version,omitempty"`
	PolicySpecDigest       *string           `json:"policy_spec_digest,omitempty"`
	PeriodStart            string            `json:"period_start"`
	PeriodEnd              string            `json:"period_end"`
	HasPeriodBounds        bool              `json:"has_period_bounds"`
	LimitMicroUSD          *jsonInt          `json:"limit_micro_usd,omitempty"`
	StaticReservedMicroUSD *jsonInt          `json:"static_reserved_micro_usd,omitempty"`
	Action                 string            `json:"action"`
	Membership             []jsonEvidenceRef `json:"membership"`
}

type jsonAccountingBasis struct {
	Kind     string            `json:"kind"`
	Evidence []jsonEvidenceRef `json:"evidence"`
}

type jsonChildVersion struct {
	ID      string  `json:"id"`
	Version jsonInt `json:"version"`
}

type jsonImportRequest struct {
	Handle             string             `json:"handle"`
	AccountingAt       *string            `json:"accounting_at,omitempty"`
	AccountingEvidence []jsonEvidenceRef  `json:"accounting_evidence"`
	Children           []jsonChildVersion `json:"children"`
	OwnerRef           string             `json:"owner_ref"`
	ReviewAfter        string             `json:"review_after"`
	Evidence           []jsonEvidenceRef  `json:"evidence"`
}

// jsonImportSnapshot is the immutable original of one imported handle. The
// original child rows are stored as an ORDERED list of column/value string pairs
// rather than a free map: a model.Record round-tripped through encoding/json would
// come back with float64 amounts, which is precisely the corruption this codec
// exists to make impossible.
type jsonImportSnapshot struct {
	RequestDigest    string            `json:"request_digest"`
	GroupDigest      string            `json:"group_digest"`
	ImportDigest     string            `json:"import_digest"`
	OriginalRequest  jsonImportRequest `json:"original_request"`
	OriginalChildren []jsonChildRow    `json:"original_children"`
	FrontierRef      *jsonEvidenceRef  `json:"frontier_ref,omitempty"`
}

// jsonChildRow is one original reservation row in the CLOSED column set of the
// reservation schema. Text cells travel as strings, integer cells as decimal
// strings, and an absent nullable cell is absent rather than empty.
type jsonChildRow struct {
	ID         string `json:"id"`
	PolicyRef  string `json:"policy_ref"`
	PolicyKind string `json:"policy_kind"`
	// Dimension is the ONE nullable text cell of the historical reservation row, and
	// it is a POINTER for that reason: the schema allows NULL, and NULL is not the
	// present empty string. Collapsing them (which the first cut did) makes an
	// absence the import never knew indistinguishable from a value the operator
	// deliberately configured, under the same group digest.
	Dimension   *string `json:"dimension,omitempty"`
	ScopeKey    string  `json:"dim_key"`
	Period      string  `json:"period"`
	PeriodStart string  `json:"period_start"`
	Seq         jsonInt `json:"seq"`
	Amount      jsonInt `json:"amount_micro_usd"`
	Actual      jsonInt `json:"actual_micro_usd"`
	State       string  `json:"state"`
	Handle      string  `json:"handle"`
	ExpiresAt   string  `json:"expires_at"`
	SettledAt   *string `json:"settled_at,omitempty"`
	Version     jsonInt `json:"version"`
}

// jsonPendingGroup is one legacy handle group frozen in the frontier: its handle,
// the digest of its complete original rows and how many rows that digest covers.
// The rows themselves are NOT copied here — a second monetary ledger is exactly
// what the contract forbids — and the digest is what an import is checked against.
type jsonPendingGroup struct {
	Handle      string  `json:"handle"`
	GroupDigest string  `json:"group_digest"`
	ChildCount  jsonInt `json:"child_count"`
}

// jsonFrontier is the durable census.
type jsonFrontier struct {
	FrontierDigest           string                `json:"frontier_digest"`
	FrontierAt               string                `json:"frontier_at"`
	PendingGroups            []jsonPendingGroup    `json:"pending_groups"`
	PendingGroupCount        jsonInt               `json:"pending_group_count"`
	PendingGroupDigest       string                `json:"pending_group_digest"`
	HistoricalTerminalDigest string                `json:"historical_terminal_digest"`
	HistoricalTerminalCount  jsonInt               `json:"historical_terminal_count"`
	OriginalRequest          jsonActivationRequest `json:"original_request"`
	Evidence                 []jsonEvidenceRef     `json:"evidence"`
}

type jsonActivationRequest struct {
	ExpectedVersion jsonInt           `json:"expected_version"`
	Evidence        []jsonEvidenceRef `json:"evidence"`
}

// strictUnmarshal decodes exactly one JSON document into v.
//
// Four refusals, and the last three were added by the R8 return because the first
// was doing all the work alone:
//
//   - an UNKNOWN FIELD is a shape this binary cannot validate;
//   - a DUPLICATE KEY is two values for one fact. encoding/json silently keeps the
//     last, so a document could carry the amount a reader validates beside the one
//     a writer meant, and nothing would say so;
//   - a JSON `null` is refused ANYWHERE. This codec never emits one: an absent
//     optional value is OMITTED, which is how presence stays a fact rather than a
//     spelling. So a null in stored bytes is either corruption or another writer's
//     convention, and quietly decoding it into a Go zero value is exactly how a
//     nullable absence becomes a present empty string;
//   - TRAILING CONTENT. This used to be checked with Decoder.More(), and More()
//     does not answer that question: it asks whether another ELEMENT follows inside
//     an array or object being streamed, so at top level it is false whatever
//     trails the document. A stray `}` or a second document passed. The check is
//     now an explicit read to io.EOF.
func strictUnmarshal(data string, v any) error {
	if err := scanStrictJSON(data); err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("finops: stored document is not the declared shape: %w", err)
	}
	// EOF, actually asked for. Token returns io.EOF only when the input is spent.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("finops: stored document carries trailing content")
	}
	return nil
}

// scanStrictJSON walks the token stream once and enforces the three structural
// rules the decoder cannot: no duplicate key at any object level, no null literal
// anywhere, and a single document that ends at end of input.
//
// It is a token walk rather than a decode into map[string]any precisely because a
// map cannot represent the defect it is looking for: two entries with one key
// collapse into one the moment they are stored.
func scanStrictJSON(data string) error {
	dec := json.NewDecoder(strings.NewReader(data))
	dec.UseNumber()
	type frame struct {
		object    bool
		keys      map[string]bool
		expectKey bool
	}
	var stack []frame
	// value records that a complete value was just consumed, so an enclosing object
	// goes back to expecting a key.
	value := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].expectKey = true
		}
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("finops: stored document is not well-formed JSON: %w", err)
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				stack = append(stack, frame{object: true, keys: map[string]bool{}, expectKey: true})
			case '[':
				stack = append(stack, frame{})
			case '}', ']':
				if len(stack) == 0 {
					return fmt.Errorf("finops: stored document is not well-formed JSON")
				}
				stack = stack[:len(stack)-1]
				value()
			}
		case nil:
			// The null literal. See the note on strictUnmarshal: this codec never
			// writes one, so accepting it would import another writer's convention.
			return fmt.Errorf("finops: stored document carries a null; an absent value is omitted, never null")
		case string:
			if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].expectKey {
				if stack[n-1].keys[t] {
					return fmt.Errorf("finops: stored document repeats a key")
				}
				stack[n-1].keys[t] = true
				stack[n-1].expectKey = false
				continue
			}
			value()
		default:
			value()
		}
	}
	if len(stack) != 0 {
		return fmt.Errorf("finops: stored document ends inside a value")
	}
	return nil
}

// marshalBounded encodes v and refuses a document over limit, rather than storing
// a payload no reader is promised to be able to handle.
func marshalBounded(v any, limit int, what string) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("finops: encode %s: %w", what, err)
	}
	if len(b) > limit {
		return "", fmt.Errorf("finops: %s payload of %d bytes exceeds the %d byte bound", what, len(b), limit)
	}
	return string(b), nil
}

// -----------------------------------------------------------------------------
// Struct <-> document conversion
// -----------------------------------------------------------------------------

func encodeBinding(b AttemptBinding) jsonBinding {
	attr := make(map[string]jsonAttributionFact, len(attributionDimensions))
	for _, dim := range attributionDimensions {
		f, ok := b.Attribution[dim]
		if !ok {
			f = AttributionFact{State: factMissing}
		}
		values := f.Values
		if values == nil {
			values = []string{}
		}
		attr[dim] = jsonAttributionFact{
			State:             string(f.State),
			Values:            values,
			Evidence:          encodeEvidenceRefs(f.Evidence),
			NotApplicableRule: f.NotApplicableRule,
		}
	}
	out := jsonBinding{
		Status:     b.Status,
		RequestRef: string(b.RequestRef),
		Subject: jsonSubject{
			ActorRef: b.Subject.ActorRef, ActorKind: b.Subject.ActorKind,
			UserID: b.Subject.UserID.String(), CredentialID: b.Subject.CredentialID.String(),
			AgentIdentity: b.Subject.AgentIdentity, SessionIdentity: b.Subject.SessionIdentity,
			SessionWorkspaceID:  b.Subject.SessionWorkspaceID.String(),
			SessionRunRef:       b.Subject.SessionRunRef,
			SessionFence:        jsonInt(b.Subject.SessionFence),
			DelegationRefs:      encodeEvidenceRefs(b.Subject.DelegationRefs),
			UntrustedSessionRef: b.Subject.UntrustedSessionRef,
		},
		Entities: jsonEntities{
			ProviderID: b.Entities.ProviderID.String(), ModelID: b.Entities.ModelID.String(),
			SessionID: b.Entities.SessionID.String(), AgentID: b.Entities.AgentID.String(),
		},
		Destination: jsonDestination{
			ProfileRef: b.Destination.ProfileRef, ProfileRevision: b.Destination.ProfileRevision,
			ProviderRef: b.Destination.ProviderRef, ModelRef: b.Destination.ModelRef,
			Action: b.Destination.Action, Protocol: b.Destination.Protocol,
			AdapterID: b.Destination.AdapterID, AdapterVersion: b.Destination.AdapterVersion,
			Surface: b.Destination.Surface, InferenceGeo: b.Destination.InferenceGeo,
			CredentialAudience: b.Destination.CredentialAudience, AuthScheme: b.Destination.AuthScheme,
			EndpointDigest:    string(b.Destination.EndpointDigest),
			TransportDigest:   string(b.Destination.TransportDigest),
			PolicyID:          b.Destination.PolicyID.String(),
			PolicyVersion:     jsonInt(b.Destination.PolicyVersion),
			PolicySpecDigest:  string(b.Destination.PolicySpecDigest),
			ProxyPolicyDigest: string(b.Destination.ProxyPolicyDigest),
			PreparedDigest:    string(b.Destination.PreparedDigest),
			PreparedBytes:     jsonInt(b.Destination.PreparedBytes),
			MaxOutputTokens:   jsonInt(b.Destination.MaxOutputTokens),
			MaxRequestBytes:   jsonInt(b.Destination.MaxRequestBytes),
			MaxResponseBytes:  jsonInt(b.Destination.MaxResponseBytes),
			TimeoutNanos:      jsonInt(b.Destination.TimeoutNanos),
		},
		Attribution: attr,
		Estimate: jsonEstimate{
			AmountMicroUSD: jsonInt(b.Estimate.AmountMicroUSD),
			Method:         b.Estimate.Method,
			Revision:       b.Estimate.Revision,
			InputBound:     optJSONInt(b.Estimate.InputBound),
			OutputBound:    optJSONInt(b.Estimate.OutputBound),
			RateRefs:       encodeEvidenceRefs(b.Estimate.RateRefs),
			PriceDigest:    string(b.Estimate.PriceDigest),
			QualificationRef: jsonEvidenceRef{
				Kind: b.Estimate.QualificationRef.Kind, Ref: b.Estimate.QualificationRef.Ref,
				Digest:   string(b.Estimate.QualificationRef.Digest),
				AuditSeq: jsonInt(b.Estimate.QualificationRef.AuditSeq),
				Version:  jsonInt(b.Estimate.QualificationRef.Version),
			},
		},
		ApplySeatLimits: b.ApplySeatLimits,
		AuthorityRefs:   encodeEvidenceRefs(b.AuthorityRefs),
	}
	if b.PredecessorRef != nil {
		s := string(*b.PredecessorRef)
		out.PredecessorRef = &s
	}
	return out
}

func decodeBinding(in jsonBinding) (AttemptBinding, error) {
	out := AttemptBinding{
		Status:          in.Status,
		RequestRef:      AttemptRef(in.RequestRef),
		ApplySeatLimits: in.ApplySeatLimits,
	}
	switch in.Status {
	case bindingResolved, bindingLegacyUnbound:
	default:
		return AttemptBinding{}, fmt.Errorf("finops: stored binding status %q is not in the vocabulary", in.Status)
	}
	if in.PredecessorRef != nil {
		r := AttemptRef(*in.PredecessorRef)
		if !validAttemptRef(r) {
			return AttemptBinding{}, fmt.Errorf("finops: stored predecessor reference is malformed")
		}
		out.PredecessorRef = &r
	}
	out.Attribution = make(map[string]AttributionFact, len(in.Attribution))
	for dim, f := range in.Attribution {
		if !knownAttributionDimension(dim) {
			return AttemptBinding{}, fmt.Errorf("finops: stored binding names an unknown attribution dimension")
		}
		switch FactState(f.State) {
		case factKnown, factMissing, factNotApplicable:
		default:
			return AttemptBinding{}, fmt.Errorf("finops: stored attribution state %q is not in the vocabulary", f.State)
		}
		ev, err := decodeEvidenceRefs(f.Evidence)
		if err != nil {
			return AttemptBinding{}, err
		}
		out.Attribution[dim] = AttributionFact{
			State: FactState(f.State), Values: f.Values, Evidence: ev,
			NotApplicableRule: f.NotApplicableRule,
		}
	}
	// EVERY dimension must be present in the stored document. A shorter map is not
	// a smaller binding: it is a binding whose digest cannot be recomputed.
	if len(out.Attribution) != len(attributionDimensions) {
		return AttemptBinding{}, fmt.Errorf("finops: stored binding does not carry every attribution dimension")
	}
	del, err := decodeEvidenceRefs(in.Subject.DelegationRefs)
	if err != nil {
		return AttemptBinding{}, err
	}
	if len(del) > maxDelegationRefs {
		return AttemptBinding{}, fmt.Errorf("finops: stored delegation chain exceeds its bound")
	}
	out.Subject = AttemptSubject{
		ActorRef: in.Subject.ActorRef, ActorKind: in.Subject.ActorKind,
		UserID: model.ID(in.Subject.UserID), CredentialID: model.ID(in.Subject.CredentialID),
		AgentIdentity: in.Subject.AgentIdentity, SessionIdentity: in.Subject.SessionIdentity,
		SessionWorkspaceID:  model.ID(in.Subject.SessionWorkspaceID),
		SessionRunRef:       in.Subject.SessionRunRef,
		SessionFence:        int64(in.Subject.SessionFence),
		DelegationRefs:      del,
		UntrustedSessionRef: in.Subject.UntrustedSessionRef,
	}
	out.Entities = ResolvedEntityIDs{
		ProviderID: model.ID(in.Entities.ProviderID), ModelID: model.ID(in.Entities.ModelID),
		SessionID: model.ID(in.Entities.SessionID), AgentID: model.ID(in.Entities.AgentID),
	}
	out.Destination = AttemptDestination{
		ProfileRef: in.Destination.ProfileRef, ProfileRevision: in.Destination.ProfileRevision,
		ProviderRef: in.Destination.ProviderRef, ModelRef: in.Destination.ModelRef,
		Action: in.Destination.Action, Protocol: in.Destination.Protocol,
		AdapterID: in.Destination.AdapterID, AdapterVersion: in.Destination.AdapterVersion,
		Surface: in.Destination.Surface, InferenceGeo: in.Destination.InferenceGeo,
		CredentialAudience: in.Destination.CredentialAudience, AuthScheme: in.Destination.AuthScheme,
		EndpointDigest:    Digest(in.Destination.EndpointDigest),
		TransportDigest:   Digest(in.Destination.TransportDigest),
		PolicyID:          model.ID(in.Destination.PolicyID),
		PolicyVersion:     int64(in.Destination.PolicyVersion),
		PolicySpecDigest:  Digest(in.Destination.PolicySpecDigest),
		ProxyPolicyDigest: Digest(in.Destination.ProxyPolicyDigest),
		PreparedDigest:    Digest(in.Destination.PreparedDigest),
		PreparedBytes:     int64(in.Destination.PreparedBytes),
		MaxOutputTokens:   int64(in.Destination.MaxOutputTokens),
		MaxRequestBytes:   int64(in.Destination.MaxRequestBytes),
		MaxResponseBytes:  int64(in.Destination.MaxResponseBytes),
		TimeoutNanos:      int64(in.Destination.TimeoutNanos),
	}
	rate, err := decodeEvidenceRefs(in.Estimate.RateRefs)
	if err != nil {
		return AttemptBinding{}, err
	}
	qual := EvidenceRef{
		Kind: in.Estimate.QualificationRef.Kind, Ref: in.Estimate.QualificationRef.Ref,
		Digest:   Digest(in.Estimate.QualificationRef.Digest),
		AuditSeq: int64(in.Estimate.QualificationRef.AuditSeq),
		Version:  int64(in.Estimate.QualificationRef.Version),
	}
	out.Estimate = EstimateBasis{
		AmountMicroUSD:   int64(in.Estimate.AmountMicroUSD),
		Method:           in.Estimate.Method,
		Revision:         in.Estimate.Revision,
		InputBound:       fromJSONInt(in.Estimate.InputBound),
		OutputBound:      fromJSONInt(in.Estimate.OutputBound),
		RateRefs:         rate,
		PriceDigest:      Digest(in.Estimate.PriceDigest),
		QualificationRef: qual,
	}
	auth, err := decodeEvidenceRefs(in.AuthorityRefs)
	if err != nil {
		return AttemptBinding{}, err
	}
	out.AuthorityRefs = auth
	return out, nil
}

func knownAttributionDimension(d string) bool {
	for _, k := range attributionDimensions {
		if k == d {
			return true
		}
	}
	return false
}

func optJSONInt(v *int64) *jsonInt {
	if v == nil {
		return nil
	}
	j := jsonInt(*v)
	return &j
}

func fromJSONInt(v *jsonInt) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}

func encodeTargets(ts []TargetSnapshot) []jsonTarget {
	out := make([]jsonTarget, 0, len(ts))
	for _, t := range ts {
		jt := jsonTarget{
			ChildID: t.ChildID.String(), PolicyID: t.PolicyID.String(),
			PolicyKind: t.PolicyKind, Dimension: t.Dimension,
			ScopeKey: t.ScopeKey, Period: t.Period,
			PolicyVersion:          optJSONInt(t.PolicyVersion),
			PeriodStart:            t.PeriodStart.String(),
			PeriodEnd:              t.PeriodEnd.String(),
			HasPeriodBounds:        t.HasPeriodBounds,
			LimitMicroUSD:          optJSONInt(t.LimitMicroUSD),
			StaticReservedMicroUSD: optJSONInt(t.StaticReservedMicroUSD),
			Action:                 t.Action,
			Membership:             encodeEvidenceRefs(t.Membership),
		}
		if t.PolicySpecDigest != nil {
			d := string(*t.PolicySpecDigest)
			jt.PolicySpecDigest = &d
		}
		out = append(out, jt)
	}
	return out
}

func decodeTargets(in []jsonTarget) ([]TargetSnapshot, error) {
	if len(in) > maxTargetsPerAttempt {
		return nil, fmt.Errorf("finops: stored target set exceeds its bound")
	}
	out := make([]TargetSnapshot, 0, len(in))
	for _, jt := range in {
		start, err := model.ParseTimestamp(jt.PeriodStart)
		if err != nil {
			return nil, fmt.Errorf("finops: stored target period start is malformed")
		}
		end, err := model.ParseTimestamp(jt.PeriodEnd)
		if err != nil {
			return nil, fmt.Errorf("finops: stored target period end is malformed")
		}
		childID, err := model.ParseID(jt.ChildID)
		if err != nil {
			return nil, fmt.Errorf("finops: stored target child id is malformed")
		}
		policyID, err := model.ParseID(jt.PolicyID)
		if err != nil {
			return nil, fmt.Errorf("finops: stored target policy id is malformed")
		}
		mem, err := decodeEvidenceRefs(jt.Membership)
		if err != nil {
			return nil, err
		}
		t := TargetSnapshot{
			ChildID: childID, PolicyID: policyID,
			PolicyKind: jt.PolicyKind, Dimension: jt.Dimension,
			ScopeKey: jt.ScopeKey, Period: jt.Period,
			PolicyVersion: fromJSONInt(jt.PolicyVersion),
			PeriodStart:   start, PeriodEnd: end,
			HasPeriodBounds:        jt.HasPeriodBounds,
			LimitMicroUSD:          fromJSONInt(jt.LimitMicroUSD),
			StaticReservedMicroUSD: fromJSONInt(jt.StaticReservedMicroUSD),
			Action:                 jt.Action,
			Membership:             mem,
		}
		if jt.PolicySpecDigest != nil {
			d := Digest(*jt.PolicySpecDigest)
			if !validDigest(d) {
				return nil, fmt.Errorf("finops: stored target policy spec digest is malformed")
			}
			t.PolicySpecDigest = &d
		}
		out = append(out, t)
	}
	return out, nil
}

func encodeAccountingBasis(a AccountingBasis) jsonAccountingBasis {
	return jsonAccountingBasis{Kind: a.Kind, Evidence: encodeEvidenceRefs(a.Evidence)}
}

func decodeAccountingBasis(in jsonAccountingBasis) (AccountingBasis, error) {
	switch in.Kind {
	case basisAttemptAdmission, basisLegacyEvidenced, basisLegacyUnknown:
	default:
		return AccountingBasis{}, fmt.Errorf("finops: stored accounting basis %q is not in the vocabulary", in.Kind)
	}
	ev, err := decodeEvidenceRefs(in.Evidence)
	if err != nil {
		return AccountingBasis{}, err
	}
	return AccountingBasis{Kind: in.Kind, Evidence: ev}, nil
}

func encodeImportRequest(r ImportLegacyHoldRequest) jsonImportRequest {
	children := make([]jsonChildVersion, 0, len(r.Children))
	for _, c := range r.Children {
		children = append(children, jsonChildVersion{ID: c.ID.String(), Version: jsonInt(c.Version)})
	}
	out := jsonImportRequest{
		Handle:             r.Handle.String(),
		AccountingEvidence: encodeEvidenceRefs(r.AccountingEvidence),
		Children:           children,
		OwnerRef:           r.OwnerRef,
		ReviewAfter:        r.ReviewAfter.String(),
		Evidence:           encodeEvidenceRefs(r.Evidence),
	}
	if r.AccountingAt != nil {
		s := r.AccountingAt.String()
		out.AccountingAt = &s
	}
	return out
}

func decodeImportRequest(in jsonImportRequest) (ImportLegacyHoldRequest, error) {
	handle, err := model.ParseID(in.Handle)
	if err != nil {
		return ImportLegacyHoldRequest{}, fmt.Errorf("finops: stored import handle is malformed")
	}
	review, err := model.ParseTimestamp(in.ReviewAfter)
	if err != nil {
		return ImportLegacyHoldRequest{}, fmt.Errorf("finops: stored import review instant is malformed")
	}
	out := ImportLegacyHoldRequest{Handle: handle, OwnerRef: in.OwnerRef, ReviewAfter: review}
	if in.AccountingAt != nil {
		ts, perr := model.ParseTimestamp(*in.AccountingAt)
		if perr != nil {
			return ImportLegacyHoldRequest{}, fmt.Errorf("finops: stored import accounting instant is malformed")
		}
		out.AccountingAt = &ts
	}
	if out.AccountingEvidence, err = decodeEvidenceRefs(in.AccountingEvidence); err != nil {
		return ImportLegacyHoldRequest{}, err
	}
	if out.Evidence, err = decodeEvidenceRefs(in.Evidence); err != nil {
		return ImportLegacyHoldRequest{}, err
	}
	for _, c := range in.Children {
		id, cerr := model.ParseID(c.ID)
		if cerr != nil {
			return ImportLegacyHoldRequest{}, fmt.Errorf("finops: stored import child id is malformed")
		}
		out.Children = append(out.Children, LegacyChildVersion{ID: id, Version: int64(c.Version)})
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// The original reservation-row snapshot
// -----------------------------------------------------------------------------

// childRowSnapshot reads ONE original reservation row into the closed snapshot
// shape. Every cell is read entry-then-type-then-value: an absent cell, a cell of
// the wrong type and a cell holding "" are three different observations, and none
// of them is silently completed from the caller's own scope. A row that cannot be
// read exactly is refused — an import that guessed at a column would be recording
// a history that never happened.
func childRowSnapshot(r model.Record) (jsonChildRow, error) {
	text := func(col string) (string, error) {
		v, ok := textCell(r, col)
		if !ok {
			return "", fmt.Errorf("finops: reservation row cell %q is absent or not text", col)
		}
		return v, nil
	}
	num := func(col string) (int64, error) {
		v, ok := int64Cell(r, col)
		if !ok {
			return 0, fmt.Errorf("finops: reservation row cell %q is absent or not an integer", col)
		}
		return v, nil
	}
	var out jsonChildRow
	var err error
	if out.ID, err = text(model.ColID); err != nil {
		return jsonChildRow{}, err
	}
	if out.PolicyRef, err = text(colResvPolicyRef); err != nil {
		return jsonChildRow{}, err
	}
	if out.PolicyKind, err = text(colResvPolicyKind); err != nil {
		return jsonChildRow{}, err
	}
	// The nullable cell, read entry-then-type-then-value. Absent or NULL stays
	// ABSENT; a present string is preserved exactly, including the empty one; a
	// present cell of any other type is a row this codec will not guess at.
	if cell, present := r[colResvDimension]; present && cell != nil {
		text, isText := cell.(string)
		if !isText {
			return jsonChildRow{}, fmt.Errorf("finops: reservation row cell %q is present but not text", colResvDimension)
		}
		out.Dimension = &text
	}
	if out.ScopeKey, err = text(colResvScopeKey); err != nil {
		return jsonChildRow{}, err
	}
	if out.Period, err = text(colResvPeriod); err != nil {
		return jsonChildRow{}, err
	}
	if out.PeriodStart, err = text(colResvPeriodStart); err != nil {
		return jsonChildRow{}, err
	}
	seq, err := num(colResvSeq)
	if err != nil {
		return jsonChildRow{}, err
	}
	out.Seq = jsonInt(seq)
	amount, err := num(colResvAmount)
	if err != nil {
		return jsonChildRow{}, err
	}
	out.Amount = jsonInt(amount)
	actual, err := num(colResvActual)
	if err != nil {
		return jsonChildRow{}, err
	}
	out.Actual = jsonInt(actual)
	if out.State, err = text(colResvState); err != nil {
		return jsonChildRow{}, err
	}
	if out.Handle, err = text(colResvHandle); err != nil {
		return jsonChildRow{}, err
	}
	if out.ExpiresAt, err = text(colResvExpiresAt); err != nil {
		return jsonChildRow{}, err
	}
	if v, ok := textCell(r, colResvSettledAt); ok {
		s := v
		out.SettledAt = &s
	}
	version, err := num(model.ColVersion)
	if err != nil {
		return jsonChildRow{}, err
	}
	out.Version = jsonInt(version)
	return out, nil
}

// canon frames one original child row in declaration order.
func (c jsonChildRow) canon(w *canonWriter) {
	w.str(c.ID)
	w.str(c.PolicyRef)
	w.str(c.PolicyKind)
	// Presence first, then the value: an absent dimension and a present empty one
	// frame differently, so they cannot share a group digest.
	w.presence(c.Dimension != nil)
	if c.Dimension != nil {
		w.str(*c.Dimension)
	}
	w.str(c.ScopeKey)
	w.str(c.Period)
	w.str(c.PeriodStart)
	w.num(int64(c.Seq))
	w.num(int64(c.Amount))
	w.num(int64(c.Actual))
	w.str(c.State)
	w.str(c.Handle)
	w.str(c.ExpiresAt)
	w.presence(c.SettledAt != nil)
	if c.SettledAt != nil {
		w.str(*c.SettledAt)
	}
	w.num(int64(c.Version))
}

// sortChildRows orders a group's original rows by the contract's child order
// (policy_ref, scope_key, period_start, child id), so the group digest does not
// depend on the order the store happened to page them in.
func sortChildRows(rows []jsonChildRow) {
	less := func(a, b jsonChildRow) bool {
		if a.PolicyRef != b.PolicyRef {
			return a.PolicyRef < b.PolicyRef
		}
		if a.ScopeKey != b.ScopeKey {
			return a.ScopeKey < b.ScopeKey
		}
		if a.PeriodStart != b.PeriodStart {
			return a.PeriodStart < b.PeriodStart
		}
		return a.ID < b.ID
	}
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && less(rows[j], rows[j-1]); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

// groupDigest is the immutable digest of one handle's COMPLETE sorted original
// rows (contract keys.import_group).
func groupDigest(tenant model.TenantID, handle model.ID, rows []jsonChildRow) Digest {
	return canonDigest(domainImportGroup, func(w *canonWriter) {
		w.str(tenant.String())
		w.str(handle.String())
		w.count(len(rows))
		for _, r := range rows {
			r.canon(w)
		}
	})
}

// importRequestDigest is the immutable digest of the complete original request
// (contract keys.import_request).
func importRequestDigest(tenant model.TenantID, req ImportLegacyHoldRequest) Digest {
	return canonDigest(domainImportRequest, func(w *canonWriter) {
		w.str(tenant.String())
		req.canon(w)
	})
}

// importRequestBytes is the exact canonical stream of the original request, stored
// beside its digest so a replay can compare BYTES: a digest match with different
// bytes would be a collision, and this is how the two stay distinguishable.
func importRequestBytes(tenant model.TenantID, req ImportLegacyHoldRequest) []byte {
	return canonBytes(domainImportRequest, func(w *canonWriter) {
		w.str(tenant.String())
		req.canon(w)
	})
}

// importDigest binds the request, the group and the frontier reference captured
// under the lock (contract canonicalization.import_replay).
func importDigest(tenant model.TenantID, handle model.ID, request, group Digest, frontier *EvidenceRef) Digest {
	return canonDigest(domainImportSnapshot, func(w *canonWriter) {
		w.str(tenant.String())
		w.str(handle.String())
		w.str(string(request))
		w.str(string(group))
		w.presence(frontier != nil)
		if frontier != nil {
			frontier.canon(w)
		}
	})
}

func encodeImportSnapshot(s LegacyImportSnapshot, rows []jsonChildRow) jsonImportSnapshot {
	out := jsonImportSnapshot{
		RequestDigest:    string(s.RequestDigest),
		GroupDigest:      string(s.GroupDigest),
		ImportDigest:     string(s.ImportDigest),
		OriginalRequest:  encodeImportRequest(s.OriginalRequest),
		OriginalChildren: rows,
	}
	if s.FrontierRef != nil {
		fr := jsonEvidenceRef{
			Kind: s.FrontierRef.Kind, Ref: s.FrontierRef.Ref,
			Digest:   string(s.FrontierRef.Digest),
			AuditSeq: jsonInt(s.FrontierRef.AuditSeq), Version: jsonInt(s.FrontierRef.Version),
		}
		out.FrontierRef = &fr
	}
	return out
}

// decodeImportSnapshot rebuilds the immutable original. The original child rows
// come back as model.Record values carrying int64 cells — never float64 — because
// the document held them as decimal strings.
func decodeImportSnapshot(in jsonImportSnapshot) (LegacyImportSnapshot, []jsonChildRow, error) {
	for _, d := range []Digest{Digest(in.RequestDigest), Digest(in.GroupDigest), Digest(in.ImportDigest)} {
		if !validDigest(d) {
			return LegacyImportSnapshot{}, nil, fmt.Errorf("finops: stored import digest is malformed")
		}
	}
	req, err := decodeImportRequest(in.OriginalRequest)
	if err != nil {
		return LegacyImportSnapshot{}, nil, err
	}
	out := LegacyImportSnapshot{
		RequestDigest:   Digest(in.RequestDigest),
		GroupDigest:     Digest(in.GroupDigest),
		ImportDigest:    Digest(in.ImportDigest),
		OriginalRequest: req,
	}
	if in.FrontierRef != nil {
		fr := EvidenceRef{
			Kind: in.FrontierRef.Kind, Ref: in.FrontierRef.Ref,
			Digest:   Digest(in.FrontierRef.Digest),
			AuditSeq: int64(in.FrontierRef.AuditSeq), Version: int64(in.FrontierRef.Version),
		}
		if !fr.valid() {
			return LegacyImportSnapshot{}, nil, fmt.Errorf("finops: stored frontier reference is malformed")
		}
		out.FrontierRef = &fr
	}
	out.OriginalChildren = childRecordsOf(in.OriginalChildren)
	return out, in.OriginalChildren, nil
}

// -----------------------------------------------------------------------------
// The activation frontier document
// -----------------------------------------------------------------------------

// sortPendingGroups orders the census by handle, so the digest does not depend on
// the order the store paged the rows in.
func sortPendingGroups(g []jsonPendingGroup) {
	for i := 1; i < len(g); i++ {
		for j := i; j > 0 && g[j].Handle < g[j-1].Handle; j-- {
			g[j], g[j-1] = g[j-1], g[j]
		}
	}
}

// groupSetDigest is the digest of a complete sorted set of handle groups. It is
// used for both halves of the census — the pending set and the frozen terminal
// baseline — because they are the same kind of object and one function keeps them
// from drifting apart.
func groupSetDigest(domainTag string, tenant model.TenantID, groups []jsonPendingGroup) Digest {
	return canonDigest(domainScopeFrontier, func(w *canonWriter) {
		w.str(domainTag)
		w.str(tenant.String())
		w.count(len(groups))
		for _, g := range groups {
			w.str(g.Handle)
			w.str(g.GroupDigest)
			w.num(int64(g.ChildCount))
		}
	})
}

// The two group-set tags inside the frontier domain.
const (
	frontierTagPending  = "pending"
	frontierTagTerminal = "historical_terminal"
)

// frontierDigestOf binds the whole boundary: the tenant, the database instant the
// boundary was taken at, the complete pending set (count and digest), the frozen
// terminal baseline digest and the sorted digests of the quiescence/caller
// evidence presented for it.
func frontierDigestOf(
	tenant model.TenantID,
	at model.Timestamp,
	pendingCount int64,
	pending, terminal Digest,
	evidence []EvidenceRef,
) Digest {
	digests := evidenceDigests(evidence)
	return canonDigest(domainScopeFrontier, func(w *canonWriter) {
		w.str("frontier")
		w.str(tenant.String())
		w.ts(at)
		w.num(pendingCount)
		w.str(string(pending))
		w.str(string(terminal))
		w.count(len(digests))
		for _, d := range digests {
			w.str(d)
		}
	})
}

// scopeProposalDigest is the digest of the proposed scope transition presented to
// the verifier: the target state, the boundary being proposed and the request's
// own evidence. It exists so the verifier is shown WHAT is proposed rather than a
// bare authority question.
func scopeProposalDigest(tenant model.TenantID, state string, frontier Digest, req LifecycleActivationRequest) Digest {
	return canonDigest(domainScopeProposal, func(w *canonWriter) {
		w.str(tenant.String())
		w.str(state)
		w.str(string(frontier))
		req.canon(w)
	})
}

// activationRequestBytes is the exact canonical stream of the original Begin
// request, stored for replay comparison BEFORE the OCC. Server-assigned database
// instants are excluded from it: they are generated per call, so including them
// would make every replay look like a different request.
func activationRequestBytes(tenant model.TenantID, req LifecycleActivationRequest) []byte {
	return canonBytes(domainScopeProposal, func(w *canonWriter) {
		w.str(tenant.String())
		req.canon(w)
	})
}

func encodeActivationRequest(r LifecycleActivationRequest) jsonActivationRequest {
	return jsonActivationRequest{
		ExpectedVersion: jsonInt(r.ExpectedVersion),
		Evidence:        encodeEvidenceRefs(r.Evidence),
	}
}

func decodeActivationRequest(in jsonActivationRequest) (LifecycleActivationRequest, error) {
	ev, err := decodeEvidenceRefs(in.Evidence)
	if err != nil {
		return LifecycleActivationRequest{}, err
	}
	return LifecycleActivationRequest{ExpectedVersion: int64(in.ExpectedVersion), Evidence: ev}, nil
}

// childRecordsOf converts the closed snapshot rows back into records, the one
// place that mapping lives so the write path and the read path cannot drift.
func childRecordsOf(rows []jsonChildRow) []model.Record {
	out := make([]model.Record, 0, len(rows))
	for _, r := range rows {
		rec := model.Record{
			model.ColID:        r.ID,
			colResvPolicyRef:   r.PolicyRef,
			colResvPolicyKind:  r.PolicyKind,
			colResvScopeKey:    r.ScopeKey,
			colResvPeriod:      r.Period,
			colResvPeriodStart: r.PeriodStart,
			colResvSeq:         int64(r.Seq),
			colResvAmount:      int64(r.Amount),
			colResvActual:      int64(r.Actual),
			colResvState:       r.State,
			colResvHandle:      r.Handle,
			colResvExpiresAt:   r.ExpiresAt,
			model.ColVersion:   int64(r.Version),
		}
		// The absent dimension is left OUT of the record, not written as "": a
		// reader asking whether the cell is null must get the same answer it would
		// get from the row itself.
		if r.Dimension != nil {
			rec[colResvDimension] = *r.Dimension
		}
		if r.SettledAt != nil {
			rec[colResvSettledAt] = *r.SettledAt
		}
		out = append(out, rec)
	}
	return out
}
