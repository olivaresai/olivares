// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// THE DURABLE FINANCIAL EVIDENCE OF ONE ALERT.
//
// An alert row records a number today. What it cannot record is what kind of number
// it is, which policy produced it, or what was actually established when it fired.
// This file builds the versioned envelope that carries those facts, its content
// digest, and the reader that interprets one back — including the histories that
// have neither.
//
// Rules the envelope exists to hold, and that its reader must never soften:
//
//   - An unknown component does NOT carry a zero value. Absence is expressed as a
//     null amount with its closed cause, never as a number a consumer can add up.
//   - lower_bound + proven means the EVIDENCE of the amount is incomplete, not the
//     DECISION. Those are separate fields precisely so no reader has to infer one
//     from severity, title or a truncated flag.
//   - The digest is a content commitment over the evidence as evaluated. It does not
//     authenticate a producer, prove delivery, or let anyone reconstruct the policy
//     that was in force at some earlier instant.
//   - Money never round-trips through a JSON float: every amount is a canonical
//     decimal string, and an amount outside int64 keeps its exact wide decimal.

// alertEvidenceSchemaVersion is the envelope version. A reader that does not know a
// version reports unknown with a cause; it never guesses at the contents.
const alertEvidenceSchemaVersion = 1

// alertEvidenceDigestVersion identifies the preimage construction below.
const alertEvidenceDigestVersion = 1

// alertEvidenceDigestDomain prefixes the preimage so a hash of these bytes cannot
// collide with a hash of some other structure that happens to serialize the same.
const alertEvidenceDigestDomain = "olivares.finops.alert-evidence.v1"

// alertEvidenceEnvelope is the stored evidence. Field order is the serialization
// order: encoding/json writes struct fields in declaration order and this type
// contains NO maps, so its bytes are deterministic — which is what makes the digest
// reproducible without a bespoke canonicalizer.
type alertEvidenceEnvelope struct {
	SchemaVersion int    `json:"schema_version"`
	DigestVersion int    `json:"digest_version"`
	AlertID       string `json:"alert_id"`
	TenantID      string `json:"tenant_id"`
	BudgetID      string `json:"budget_id"`

	Policy alertEvidencePolicy `json:"policy"`
	Amount alertEvidenceAmount `json:"amount"`

	Components alertEvidenceComponents `json:"components"`
	Decision   alertEvidenceDecision   `json:"decision"`
	Context    alertEvidenceContext    `json:"context"`
	Legacy     alertEvidenceLegacy     `json:"legacy"`
}

// alertEvidencePolicy is the policy AS READ for this evaluation: its identity, its
// version and the financial fields that were actually used. Policy.Spec is not
// copied wholesale — an envelope is evidence, not a place to park configuration or
// anything an operator may later put in that map.
type alertEvidencePolicy struct {
	ID               string `json:"id"`
	Version          int64  `json:"version"`
	Name             string `json:"name"`
	Dimension        string `json:"dimension"`
	Key              string `json:"key"`
	Period           string `json:"period"`
	Currency         string `json:"currency"`
	Action           string `json:"action"`
	LimitMicroUSD    string `json:"limit_micro_usd"`
	ReservedMicroUSD string `json:"reserved_micro_usd,omitempty"`
	ConfigFault      string `json:"config_fault,omitempty"`
}

// alertEvidenceAmount is the classified effective consumption. Value is null when
// the class is unknown: there is no number, and none is invented.
type alertEvidenceAmount struct {
	Class    string   `json:"class"`
	Value    *string  `json:"value_micro_usd"`
	Currency string   `json:"currency"`
	Causes   []string `json:"causes,omitempty"`
}

// alertEvidenceComponents describes each input separately, with its own state. A
// component that was not established carries no value.
type alertEvidenceComponents struct {
	Cost    alertEvidenceComponent `json:"cost"`
	Static  alertEvidenceComponent `json:"static_reservation"`
	Dynamic alertEvidenceComponent `json:"dynamic_reservation"`
}

// alertEvidenceComponent is one input. Rows/Pages are READ DIAGNOSTICS: they say
// how much was seen, never how much was owed.
type alertEvidenceComponent struct {
	State  string   `json:"state"`
	Value  *string  `json:"value_micro_usd"`
	Causes []string `json:"causes,omitempty"`
	Rows   int      `json:"rows_read,omitempty"`
	Pages  int      `json:"pages_read,omitempty"`
}

// alertEvidenceDecision is the crossing itself, kept apart from the amount: a
// proven crossing under a lower bound is a real crossing.
type alertEvidenceDecision struct {
	Result string `json:"result"`
	// Threshold is the normalized decimal of the configured value, and Target is the
	// exact product with the limit — as a decimal when one terminates within the
	// helper's printable bound, and always as the exact rational num/den so no
	// reader has to trust a rounded figure.
	Threshold         string   `json:"threshold"`
	TargetMicroUSD    string   `json:"target_micro_usd,omitempty"`
	TargetNumerator   string   `json:"target_numerator"`
	TargetDenominator string   `json:"target_denominator"`
	LegacyPct         int      `json:"legacy_threshold_pct"`
	Causes            []string `json:"causes,omitempty"`
}

// alertEvidenceContext records what the read actually covered and when.
type alertEvidenceContext struct {
	WindowStart string `json:"window_start,omitempty"`
	WindowEnd   string `json:"window_end,omitempty"`
	// WindowBounds names the interval convention rather than leaving a reader to
	// assume one.
	WindowBounds  string `json:"window_bounds"`
	Provenance    string `json:"provenance_filter"`
	ScopeResolved bool   `json:"scope_resolved"`
	ScopeColumn   string `json:"scope_column,omitempty"`
	ScopeValue    string `json:"scope_value,omitempty"`
	EvaluatedAt   string `json:"evaluated_at"`
	SampleAt      string `json:"sample_occurred_at"`
	// ReadConsistency states exactly what the enumeration can and cannot promise. It
	// is not a claim of a transactional snapshot.
	ReadConsistency string `json:"read_consistency"`
}

// alertEvidenceLegacy classifies the number written into the historical column, so
// a reader can tell an exact amount from a bound and from a projection that is not
// available at all.
type alertEvidenceLegacy struct {
	ValueKind     string `json:"value_kind"`
	SpendMicroUSD int64  `json:"spend_micro_usd"`
	Note          string `json:"note,omitempty"`
}

// legacy value kinds, in a closed vocabulary.
const (
	legacyValueExact       = "exact"
	legacyValueLowerBound  = "lower_bound"
	legacyValueUnavailable = "unavailable"
	legacyValueUnverified  = "unverified"
)

// readConsistencyScopePaged is the only honest description of what the strict cost
// read provides: a bounded, self-consistent enumeration inside the caller's scope,
// not an instantaneous global snapshot.
const readConsistencyScopePaged = "scope_paged_enumeration"

// The two context conventions the writer states and the reader checks. They are
// constants so the check compares against the WRITER's own words rather than a
// second copy that can drift.
const (
	windowBoundsHalfOpen        = "half_open_start_inclusive_end_exclusive"
	provenanceEstimatedOnly     = "estimated_stream_billed_excluded"
	maxEvidenceComponentDiagRow = maxStrictCostRows
)

// buildAlertEvidence assembles the envelope for one PROVEN threshold of one
// evaluation, under a pre-assigned alert id. It returns the envelope, the legacy
// int64 projection to write into the historical column, and the digest.
//
// The id is pre-assigned by the caller (model.NewID) precisely so the evidence can
// commit to the row it belongs to BEFORE the single insert: a hash written by a
// second transaction would be evidence of nothing.
func buildAlertEvidence(alertID model.ID, tenant model.TenantID, eval budgetEvaluation, te thresholdEvaluation) (alertEvidenceEnvelope, int64, string, error) {
	env := alertEvidenceEnvelope{
		SchemaVersion: alertEvidenceSchemaVersion,
		DigestVersion: alertEvidenceDigestVersion,
		AlertID:       alertID.String(),
		TenantID:      tenant.String(),
		BudgetID:      eval.PolicyID.String(),
		Policy: alertEvidencePolicy{
			ID: eval.PolicyID.String(), Version: eval.PolicyVersion, Name: eval.PolicyName,
			Dimension: eval.Spec.Dimension, Key: eval.Spec.Key, Period: eval.Spec.Period,
			Currency: eval.Spec.Currency, Action: eval.Spec.Action,
			LimitMicroUSD: strconv.FormatInt(eval.Spec.LimitMicroUSD, 10),
			ConfigFault:   string(eval.Config),
		},
		Amount: alertEvidenceAmount{
			Class: string(eval.Amount.Class), Currency: eval.Spec.Currency,
			Causes: sortedCauses(eval.Amount.Causes),
		},
		Components: alertEvidenceComponents{
			Cost:    costComponentEvidence(eval.Cost),
			Static:  staticComponentEvidence(eval.Static),
			Dynamic: dynamicComponentEvidence(eval.Dynamic),
		},
		Context: alertEvidenceContext{
			WindowBounds:    windowBoundsHalfOpen,
			Provenance:      provenanceEstimatedOnly,
			ScopeResolved:   eval.Window.ScopeResolved,
			EvaluatedAt:     model.NewTimestamp(eval.EvaluatedAt).String(),
			SampleAt:        model.NewTimestamp(eval.SampleAt).String(),
			ReadConsistency: readConsistencyScopePaged,
		},
	}
	if eval.Static.Known {
		env.Policy.ReservedMicroUSD = strconv.FormatInt(eval.Static.MicroUSD, 10)
	}
	if decimal := eval.Amount.Decimal(); decimal != "" {
		env.Amount.Value = &decimal
	}
	if eval.HasPeriod {
		env.Context.WindowStart = model.NewTimestamp(eval.PeriodStart).String()
		env.Context.WindowEnd = model.NewTimestamp(eval.PeriodEnd).String()
	}
	if len(eval.Window.Filters) == 1 {
		env.Context.ScopeColumn = eval.Window.Filters[0].Column
		if v, ok := eval.Window.Filters[0].Value.(string); ok {
			env.Context.ScopeValue = v
		}
	}

	decision := alertEvidenceDecision{
		Result: string(te.Decision.Result), Threshold: te.Decision.Threshold,
		TargetMicroUSD: te.Decision.TargetDecimal, LegacyPct: te.LegacyPct,
		Causes: sortedCauses(te.Decision.Causes),
	}
	if te.Decision.Target != nil {
		decision.TargetNumerator = te.Decision.Target.Num().String()
		decision.TargetDenominator = te.Decision.Target.Denom().String()
	}
	env.Decision = decision

	legacy, kind, note := legacyProjection(eval.Amount)
	env.Legacy = alertEvidenceLegacy{ValueKind: kind, SpendMicroUSD: legacy, Note: note}

	digest, err := alertEvidenceDigest(env)
	if err != nil {
		return alertEvidenceEnvelope{}, 0, "", err
	}
	return env, legacy, digest, nil
}

// legacyProjection is the value the historical int64 column may carry, and its
// classification. A proven amount beyond int64 is projected as the explicit bound
// math.MaxInt64 — never as an exact total — and an unknown amount has no projection
// at all rather than a zero a consumer would add up.
func legacyProjection(amount effectiveAmount) (int64, string, string) {
	switch amount.Class {
	case amountExact:
		if v, ok := amount.RepresentableInt64(); ok {
			return v, legacyValueExact, ""
		}
		return math.MaxInt64, legacyValueLowerBound,
			"the exact amount is outside int64; the legacy column carries an explicit lower bound"
	case amountLowerBound:
		if v, ok := amount.RepresentableInt64(); ok {
			return v, legacyValueLowerBound, ""
		}
		return math.MaxInt64, legacyValueLowerBound,
			"the proven bound is outside int64; the legacy column carries an explicit lower bound"
	}
	return 0, legacyValueUnavailable,
		"no amount was established; the legacy zero is not a spend figure"
}

func costComponentEvidence(cost strictCostTotal) alertEvidenceComponent {
	c := alertEvidenceComponent{
		State: "unknown", Causes: sortedCauses(cost.Causes), Rows: cost.Rows, Pages: cost.Pages,
	}
	if cost.Complete && cost.Total != nil {
		c.State = "known"
		value := cost.Total.String()
		c.Value = &value
	}
	return c
}

func staticComponentEvidence(s staticComponent) alertEvidenceComponent {
	if !s.Known {
		return alertEvidenceComponent{State: "unknown", Causes: []string{string(s.Fault)}}
	}
	value := strconv.FormatInt(s.MicroUSD, 10)
	return alertEvidenceComponent{State: "known", Value: &value}
}

func dynamicComponentEvidence(d dynamicComponent) alertEvidenceComponent {
	c := alertEvidenceComponent{State: string(d.State)}
	if d.State == "" {
		c.State = string(dynamicIndeterminate)
	}
	if d.Cause != "" {
		c.Causes = []string{string(d.Cause)}
	}
	if d.known() {
		value := strconv.FormatInt(d.MicroUSD, 10)
		c.Value = &value
	}
	return c
}

// sortedCauses returns the causes as a sorted, de-duplicated string list, so the
// same evidence always serializes — and therefore hashes — identically.
func sortedCauses(causes []amountCause) []string {
	if len(causes) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(causes))
	out := make([]string, 0, len(causes))
	for _, c := range causes {
		s := string(c)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// alertEvidenceDigest is SHA-256 over the domain prefix and the envelope's own
// canonical bytes. WHAT IT COMMITS: the schema/digest versions, the alert, tenant
// and budget identity, the policy identity/version and the financial fields as
// read, the amount class/value/causes, every component's state/value/causes, the
// decision and its exact target, the context and the legacy classification — the
// whole envelope except the hash itself, which is stored beside it.
//
// WHAT IT DOES NOT DO: it does not authenticate a producer, does not sign anything,
// does not prove that any event was delivered, and does not make a later reader's
// recomputation an authorization.
func alertEvidenceDigest(env alertEvidenceEnvelope) (string, error) {
	body, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("finops: canonical evidence: %w", err)
	}
	h := sha256.New()
	h.Write([]byte(alertEvidenceDigestDomain))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// --- reading it back ---------------------------------------------------------

// evidenceReadState is what a stored alert's evidence could be established to be.
type evidenceReadState string

const (
	evidenceValid       evidenceReadState = "valid"
	evidenceUnknownRead evidenceReadState = "unknown"
)

// evidence read causes, closed.
const (
	evidenceCauseLegacyUnversioned = "legacy_unversioned"
	evidenceCauseUnsupported       = "unsupported_evidence_version"
	evidenceCauseMalformed         = "malformed_evidence"
	evidenceCauseHashMissing       = "evidence_hash_missing"
	evidenceCauseHashMismatch      = "evidence_hash_mismatch"
	evidenceCauseIdentityMismatch  = "evidence_identity_mismatch"
	// The four causes below are the A4.2 correction. A digest is a COMMITMENT TO
	// BYTES, not a statement that those bytes mean anything: an envelope whose
	// required fields are empty, whose closed enums carry unknown words, whose money
	// is not a canonical decimal or whose parts contradict each other hashes exactly
	// as well as a real one. Each names WHICH check refused, because "malformed" for
	// all of them would leave a reader unable to tell a truncated write from a
	// contradiction.
	evidenceCauseEnvelopeIncomplete = "evidence_envelope_incomplete"
	evidenceCauseEnumInvalid        = "evidence_enum_invalid"
	evidenceCauseDecimalNotCanon    = "evidence_decimal_not_canonical"
	evidenceCauseInconsistent       = "evidence_internally_inconsistent"
	// evidenceCauseRowMismatch: the envelope verifies, and the ROW beside it carries
	// a different number. The envelope commits to the legacy projection it authorized;
	// a row whose cells were changed afterwards is not described by it.
	evidenceCauseRowMismatch = "evidence_row_mismatch"
)

// interpretedEvidence is what the DTO exposes for one alert row.
type interpretedEvidence struct {
	State    evidenceReadState      `json:"state"`
	Cause    string                 `json:"cause,omitempty"`
	Digest   string                 `json:"evidence_hash,omitempty"`
	Envelope *alertEvidenceEnvelope `json:"envelope,omitempty"`
}

// interpretAlertEvidence reads the two stored cells for one alert row and decides
// what they establish. It never repairs a hash, never rebuilds an envelope from
// today's policy, and never lets an absent or broken envelope become certainty
// about the historical number beside it.
func interpretAlertEvidence(rec model.Record, tenant model.TenantID) interpretedEvidence {
	rawEnvelope, hasEnvelope := textCell(rec, colAlertEvidence)
	digest, hasDigest := textCell(rec, colAlertEvidenceHash)
	if !hasEnvelope && !hasDigest {
		// A row written before this contract existed. Its number stays visible as the
		// figure that was recorded then, and nothing is inferred about its exactness.
		return interpretedEvidence{State: evidenceUnknownRead, Cause: evidenceCauseLegacyUnversioned}
	}
	if !hasEnvelope || rawEnvelope == "" {
		return interpretedEvidence{State: evidenceUnknownRead, Cause: evidenceCauseMalformed, Digest: digest}
	}
	if !hasDigest || digest == "" {
		return interpretedEvidence{State: evidenceUnknownRead, Cause: evidenceCauseHashMissing}
	}
	var env alertEvidenceEnvelope
	if err := json.Unmarshal([]byte(rawEnvelope), &env); err != nil {
		return interpretedEvidence{State: evidenceUnknownRead, Cause: evidenceCauseMalformed, Digest: digest}
	}
	if env.SchemaVersion != alertEvidenceSchemaVersion || env.DigestVersion != alertEvidenceDigestVersion {
		return interpretedEvidence{State: evidenceUnknownRead, Cause: evidenceCauseUnsupported, Digest: digest}
	}
	if env.AlertID != rec.String(model.ColID) || env.TenantID != tenant.String() ||
		env.BudgetID != rec.String(colBudgetID) {
		// The envelope must be about THIS row, in THIS tenant. Otherwise it is
		// evidence of something else, whatever its hash says.
		return interpretedEvidence{State: evidenceUnknownRead, Cause: evidenceCauseIdentityMismatch, Digest: digest}
	}
	recomputed, err := alertEvidenceDigest(env)
	if err != nil || recomputed != digest {
		return interpretedEvidence{State: evidenceUnknownRead, Cause: evidenceCauseHashMismatch, Digest: digest}
	}
	// A4.2 CORRECTION. Everything above establishes that these BYTES are the bytes
	// that were committed under this identity. It establishes nothing about what they
	// SAY: an envelope with empty components, an unknown amount class, a decision that
	// was never proven or a money field that is not a canonical decimal hashes exactly
	// as well as a real one, because the digest is computed over whatever structure it
	// is given. The contract's invariants are checked here, and the row's own legacy
	// projections are bound to the content that authorized them.
	if cause, ok := validAlertEvidence(env); !ok {
		return interpretedEvidence{State: evidenceUnknownRead, Cause: cause, Digest: digest}
	}
	if !rowMatchesEvidence(rec, env) {
		return interpretedEvidence{State: evidenceUnknownRead, Cause: evidenceCauseRowMismatch, Digest: digest}
	}
	return interpretedEvidence{State: evidenceValid, Digest: digest, Envelope: &env}
}

// --- what a v1 envelope must SAY --------------------------------------------

// validAlertEvidence checks the closed invariants of a v1 envelope. It runs AFTER
// the digest, so it is not a tamper check: it is the difference between "these
// bytes were committed" and "these bytes describe an evaluation".
//
// It is deliberately arithmetic where it can be. The envelope carries its own
// components, its own amount and its own exact rational target, so three of the
// claims it makes can be RE-DERIVED rather than trusted:
//
//   - exact      = cost + static + dynamic, all three known.
//   - lower_bound = cost + static, with dynamic carrying the non-negative invariant.
//   - proven     ⇔ amount ≥ target, and target = threshold × limit.
//
// An envelope that fails one of those is not evidence of the crossing it claims,
// whatever its hash says. It never repairs anything and never consults today's
// policy: a refusal is reported as unknown with its cause, which is what a reader
// acts on.
func validAlertEvidence(env alertEvidenceEnvelope) (string, bool) {
	// Identity and policy identity. The row-level identity was already compared with
	// the record; this is the envelope's INTERNAL agreement about the same budget.
	if env.AlertID == "" || env.TenantID == "" || env.BudgetID == "" ||
		env.Policy.ID == "" || env.Policy.Dimension == "" || env.Policy.Period == "" ||
		env.Policy.Currency == "" || env.Policy.Action == "" || env.Policy.LimitMicroUSD == "" ||
		env.Amount.Currency == "" || env.Decision.Threshold == "" ||
		env.Context.EvaluatedAt == "" || env.Context.SampleAt == "" {
		return evidenceCauseEnvelopeIncomplete, false
	}
	if env.Policy.ID != env.BudgetID || env.Policy.Version < 0 {
		return evidenceCauseInconsistent, false
	}
	if env.Amount.Currency != env.Policy.Currency {
		return evidenceCauseInconsistent, false
	}
	if !validConfigFault(env.Policy.ConfigFault) {
		return evidenceCauseEnumInvalid, false
	}
	if env.Decision.Result == string(crossingProven) && configFaultPrecludesCrossing(env.Policy.ConfigFault) {
		// A fault that stopped the evaluation from establishing this figure cannot
		// coexist with the crossing it would have prevented. Belonging to the
		// vocabulary is not the same question as being possible here.
		return evidenceCauseInconsistent, false
	}

	// Context conventions are the writer's own constants, not free text.
	if env.Context.WindowBounds != windowBoundsHalfOpen ||
		env.Context.Provenance != provenanceEstimatedOnly ||
		env.Context.ReadConsistency != readConsistencyScopePaged {
		return evidenceCauseEnumInvalid, false
	}
	if !env.Context.ScopeResolved {
		// A complete strict cost read is impossible without a resolved scope, so a
		// stored crossing that says otherwise contradicts its own components.
		return evidenceCauseInconsistent, false
	}
	if _, err := model.ParseTimestamp(env.Context.EvaluatedAt); err != nil {
		return evidenceCauseEnvelopeIncomplete, false
	}
	if _, err := model.ParseTimestamp(env.Context.SampleAt); err != nil {
		return evidenceCauseEnvelopeIncomplete, false
	}
	if cause, ok := validEvidenceWindow(env.Context); !ok {
		return cause, false
	}
	if env.Context.ScopeColumn != "" && !supportedScopeColumn(env.Context.ScopeColumn) {
		return evidenceCauseEnumInvalid, false
	}
	if cause, ok := evidenceContextMatchesPolicy(env); !ok {
		return cause, false
	}

	// Money, as canonical decimals. A number that does not re-render to the exact
	// bytes stored is not the canonical form this contract promises.
	limit, ok := canonicalInt(env.Policy.LimitMicroUSD)
	if !ok {
		return evidenceCauseDecimalNotCanon, false
	}
	if env.Policy.ReservedMicroUSD != "" {
		if _, ok := canonicalInt(env.Policy.ReservedMicroUSD); !ok {
			return evidenceCauseDecimalNotCanon, false
		}
	}

	// The amount and its three components, each with its own closed state.
	amount, cause, ok := evidenceAmountValue(env.Amount)
	if !ok {
		return cause, false
	}
	cost, cause, ok := evidenceComponentValue(env.Components.Cost, componentCost)
	if !ok {
		return cause, false
	}
	static, cause, ok := evidenceComponentValue(env.Components.Static, componentStatic)
	if !ok {
		return cause, false
	}
	dynamic, cause, ok := evidenceComponentValue(env.Components.Dynamic, componentDynamic)
	if !ok {
		return cause, false
	}
	if cause, ok := evidenceCausesWellFormed(env); !ok {
		return cause, false
	}
	if cause, ok := evidenceReserveAgrees(env, static); !ok {
		return cause, false
	}

	// The classification algebra, re-derived. A stored row exists only for a PROVEN
	// crossing, and only an exact or bounded amount can prove one.
	sum := new(big.Int)
	switch env.Amount.Class {
	case string(amountExact):
		if cost == nil || static == nil || dynamic == nil {
			return evidenceCauseInconsistent, false
		}
		sum.Add(cost, static)
		sum.Add(sum, dynamic)
	case string(amountLowerBound):
		if cost == nil || static == nil || dynamic != nil ||
			env.Components.Dynamic.State != string(dynamicNonNegativeUnknown) {
			// A bound rests on the non-negative invariant of a dynamic obligation that
			// was NOT established. A known dynamic value would have made the amount
			// exact, and an indeterminate one proves nothing at all.
			return evidenceCauseInconsistent, false
		}
		sum.Add(cost, static)
	default:
		// unknown (or anything else) cannot be the amount of a recorded crossing.
		return evidenceCauseInconsistent, false
	}
	if amount == nil || sum.Cmp(amount) != 0 {
		return evidenceCauseInconsistent, false
	}

	// The decision, re-derived from the threshold, the limit and the amount.
	if env.Decision.Result != string(crossingProven) {
		// Only proven crossings are written. A row whose envelope claims not_reached or
		// unproven does not describe the row it is stored on.
		if !validCrossingResult(env.Decision.Result) {
			return evidenceCauseEnumInvalid, false
		}
		return evidenceCauseInconsistent, false
	}
	target, cause, ok := evidenceTarget(env.Decision, limit)
	if !ok {
		return cause, false
	}
	if new(big.Rat).SetInt(amount).Cmp(target) < 0 {
		// "Proven" means the figure reached the target. This is the crossing itself,
		// and it is recomputed rather than believed.
		return evidenceCauseInconsistent, false
	}
	if pct, pctOK := legacyThresholdPctFromText(env.Decision.Threshold); !pctOK || pct != env.Decision.LegacyPct {
		return evidenceCauseInconsistent, false
	}

	// The legacy projection the envelope authorized for the historical column.
	if kind, spend, ok := expectedLegacy(env.Amount.Class, amount); !ok {
		return evidenceCauseInconsistent, false
	} else if kind != env.Legacy.ValueKind || spend != env.Legacy.SpendMicroUSD {
		if !validLegacyValueKind(env.Legacy.ValueKind) {
			return evidenceCauseEnumInvalid, false
		}
		return evidenceCauseInconsistent, false
	}
	return "", true
}

// validEvidenceWindow checks the half-open window: either both ends are absent (an
// unbounded "total" period) or both parse and the interval is non-empty.
func validEvidenceWindow(c alertEvidenceContext) (string, bool) {
	if c.WindowStart == "" && c.WindowEnd == "" {
		return "", true
	}
	if c.WindowStart == "" || c.WindowEnd == "" {
		return evidenceCauseEnvelopeIncomplete, false
	}
	start, err := model.ParseTimestamp(c.WindowStart)
	if err != nil {
		return evidenceCauseEnvelopeIncomplete, false
	}
	end, err := model.ParseTimestamp(c.WindowEnd)
	if err != nil {
		return evidenceCauseEnvelopeIncomplete, false
	}
	if !end.Time().After(start.Time()) {
		return evidenceCauseInconsistent, false
	}
	return "", true
}

// evidenceAmountValue reads the classified amount: a closed class, and a canonical
// decimal present exactly when the class has a figure.
func evidenceAmountValue(a alertEvidenceAmount) (*big.Int, string, bool) {
	switch a.Class {
	case string(amountExact), string(amountLowerBound):
	case string(amountUnknown):
		if a.Value != nil {
			return nil, evidenceCauseInconsistent, false
		}
		return nil, "", true
	default:
		return nil, evidenceCauseEnumInvalid, false
	}
	if a.Value == nil {
		return nil, evidenceCauseEnvelopeIncomplete, false
	}
	v, ok := canonicalBigInt(*a.Value)
	if !ok {
		return nil, evidenceCauseDecimalNotCanon, false
	}
	return v, "", true
}

// evidenceComponentValue reads one component: its state must belong to that
// component's closed vocabulary, and a value is present exactly when the state is
// known. An unknown component carrying a number is the shape this contract exists to
// forbid — a zero a consumer would add up.
func evidenceComponentValue(c alertEvidenceComponent, which string) (*big.Int, string, bool) {
	known := false
	switch which {
	case componentDynamic:
		switch c.State {
		case string(dynamicKnown):
			known = true
		case string(dynamicNonNegativeUnknown), string(dynamicIndeterminate):
		default:
			return nil, evidenceCauseEnumInvalid, false
		}
	default:
		switch c.State {
		case "known":
			known = true
		case "unknown":
		default:
			return nil, evidenceCauseEnumInvalid, false
		}
	}
	if c.Rows < 0 || c.Pages < 0 || c.Rows > maxEvidenceComponentDiagRow {
		return nil, evidenceCauseInconsistent, false
	}
	if !known {
		if c.Value != nil {
			return nil, evidenceCauseInconsistent, false
		}
		return nil, "", true
	}
	if c.Value == nil {
		return nil, evidenceCauseEnvelopeIncomplete, false
	}
	v, ok := canonicalBigInt(*c.Value)
	if !ok {
		return nil, evidenceCauseDecimalNotCanon, false
	}
	if which != componentCost && (v.Sign() < 0 || !v.IsInt64()) {
		// A KNOWN reserve carries its producer's domain, and a canonical decimal is not
		// the whole of it. Both reserve components are non-negative int64 amounts where
		// they are established: the static one is a policy money cell that
		// staticFromSpec refuses when negative, and the dynamic one is a checked int64
		// sum over rows the ledger refuses when negative or unrepresentable. A negative
		// or wider-than-int64 reserve is a figure neither producer can have made.
		//
		// COST IS DELIBERATELY EXEMPT, and the exemption is the point: cost samples
		// carry legitimate credits, so a negative cost is ordinary, and the strict
		// reader sums WIDE on purpose so a total outside int64 keeps its exact decimal.
		// Constraining cost here would reject the very cases A4.1 exists to preserve.
		return nil, evidenceCauseInconsistent, false
	}
	return v, "", true
}

// evidenceCausesWellFormed checks every cause list the way sortedCauses produces
// them: words from the CLOSED v1 vocabulary, no empty string, no duplicate, sorted.
// A list that is none of those cannot have come from this writer, and its bytes would
// hash differently for the same facts.
//
// Membership is checked before order, and it is the half that was missing: a single
// unknown word passed every structural test, so the vocabulary the envelope commits to
// stopped being closed the moment anyone wrote a cause this contract never defined.
func evidenceCausesWellFormed(env alertEvidenceEnvelope) (string, bool) {
	for _, list := range [][]string{
		env.Amount.Causes, env.Components.Cost.Causes, env.Components.Static.Causes,
		env.Components.Dynamic.Causes, env.Decision.Causes,
	} {
		for i, c := range list {
			if c == "" {
				return evidenceCauseEnvelopeIncomplete, false
			}
			if !alertEvidenceCauseAllowed(c) {
				return evidenceCauseEnumInvalid, false
			}
			if i > 0 && list[i-1] >= c {
				return evidenceCauseInconsistent, false
			}
		}
	}
	return "", true
}

// componentCost/componentStatic/componentDynamic select which component is being
// read. They are named because the choice decides a DOMAIN, not a label: the two
// reserves are non-negative int64 figures, cost is signed and wide.
const (
	componentCost    = "cost"
	componentStatic  = "static"
	componentDynamic = "dynamic"
)

// evidenceReserveAgrees checks the one figure this envelope states TWICE: the
// effective reserve the policy was read with, and the static component of the amount.
//
// They are the same number by construction — buildAlertEvidence writes
// Policy.ReservedMicroUSD only when the static component is known, from the same
// staticComponent — so a stored envelope that gives two different answers describes a
// policy that never existed. Nothing here invents a zero for an absent reserve and
// nothing consults today's policy to fill one in: the absence itself has a meaning
// (the reserve was not established) and that meaning must match the component's state.
func evidenceReserveAgrees(env alertEvidenceEnvelope, static *big.Int) (string, bool) {
	if env.Components.Static.State != "known" {
		// Not established: the policy section must not carry an effective reserve
		// either, and the component's own causes already say why.
		if env.Policy.ReservedMicroUSD != "" {
			return evidenceCauseInconsistent, false
		}
		return "", true
	}
	if env.Policy.ReservedMicroUSD == "" || static == nil {
		return evidenceCauseInconsistent, false
	}
	reserved, ok := canonicalInt(env.Policy.ReservedMicroUSD)
	if !ok {
		return evidenceCauseDecimalNotCanon, false
	}
	if !static.IsInt64() || static.Int64() != reserved {
		return evidenceCauseInconsistent, false
	}
	return "", true
}

// configFaultPrecludesCrossing reports whether a configuration fault makes a PROVEN
// crossing impossible, so the reader can refuse an envelope that carries both.
//
// Every fault in today's vocabulary does. A malformed or null limit leaves the limit
// at zero, and evaluateThresholdCrossing refuses a non-positive limit before it
// compares anything; a malformed, null or negative static reserve leaves the static
// component unknown, and classifyEffectiveAmount then produces an unknown amount,
// which proves nothing. It is written as a mapping rather than "the fault must be
// empty" so a future fault that genuinely does not preclude a crossing can be added
// here deliberately instead of being let through by an over-broad rule.
func configFaultPrecludesCrossing(fault string) bool {
	switch budgetConfigFault(fault) {
	case configFaultLimitMalformed, configFaultLimitNull,
		configFaultStaticMalformed, configFaultStaticNull, configFaultStaticNegative:
		return true
	}
	return false
}

// evidenceContextMatchesPolicy re-derives the read context from the policy the
// envelope captured and the instant of the sample, with the SAME primitives the
// evaluator used: periodStart/periodEnd for the window and strictCostScope for the
// subject. Checking those fields in isolation — a parseable pair of instants, an
// increasing interval, a column that exists — accepts a window and a subject that
// belong to some other evaluation entirely.
func evidenceContextMatchesPolicy(env alertEvidenceEnvelope) (string, bool) {
	sample, err := model.ParseTimestamp(env.Context.SampleAt)
	if err != nil {
		return evidenceCauseEnvelopeIncomplete, false
	}
	// The period bucket of the SAMPLE, under the captured policy's period. An
	// unrecognised period is not rejected here: periodStart applies the evaluator's own
	// default to it, so the window stays derivable and this reader does not invent a
	// closed period vocabulary the policy model does not have.
	start, bounded := periodStart(env.Policy.Period, sample.Time())
	if !bounded {
		if env.Context.WindowStart != "" || env.Context.WindowEnd != "" {
			return evidenceCauseInconsistent, false
		}
	} else {
		gotStart, err := model.ParseTimestamp(env.Context.WindowStart)
		if err != nil {
			return evidenceCauseEnvelopeIncomplete, false
		}
		gotEnd, err := model.ParseTimestamp(env.Context.WindowEnd)
		if err != nil {
			return evidenceCauseEnvelopeIncomplete, false
		}
		if !gotStart.Time().Equal(start) || !gotEnd.Time().Equal(periodEnd(env.Policy.Period, start)) {
			return evidenceCauseInconsistent, false
		}
	}
	// The subject, expressed exactly as the evaluator expresses it.
	filters, resolved, _ := strictCostScope(budgetSpec{Dimension: env.Policy.Dimension, Key: env.Policy.Key})
	if !resolved {
		// The captured policy's own dimension cannot be expressed as a predicate, so a
		// complete strict read of it was impossible whatever the context claims.
		return evidenceCauseInconsistent, false
	}
	switch len(filters) {
	case 0:
		// A verified global scope: no predicate is the correct predicate, and the
		// context must not name one.
		if env.Context.ScopeColumn != "" || env.Context.ScopeValue != "" {
			return evidenceCauseInconsistent, false
		}
	case 1:
		want, isText := filters[0].Value.(string)
		if !isText || env.Context.ScopeColumn != filters[0].Column || env.Context.ScopeValue != want {
			return evidenceCauseInconsistent, false
		}
	default:
		return evidenceCauseInconsistent, false
	}
	return "", true
}

// alertEvidenceCauseAllowed reports whether a cause word belongs to the closed v1
// vocabulary: the amount causes of alert_amount_evidence.go and the configuration
// faults of alert_evaluation.go, which are the only two producers of the strings that
// reach an envelope.
//
// The whole vocabulary is accepted rather than a per-field subset ON PURPOSE. A
// per-field allowlist would be a second model of which producer may emit which cause,
// maintained by hand beside the first and free to drift from it; the property this
// check enforces is that the word is one THIS CONTRACT defines, and the state, sum,
// decision and reserve checks above already constrain what it may accompany.
//
// MAINTENANCE: a cause constant added to either vocabulary must be added here, or a
// legitimate envelope carrying it will be refused. That is the deny-closed direction
// and it fails loudly in the writer's own tests.
func alertEvidenceCauseAllowed(c string) bool {
	_, ok := alertEvidenceCauseVocabulary[c]
	return ok
}

var alertEvidenceCauseVocabulary = func() map[string]bool {
	out := make(map[string]bool)
	for _, c := range []amountCause{
		causeScopeTenantMismatch, causeScopeGroupUnresolved, causeScopeDimensionUnsupported,
		causeScopePredicateUnsupported, causeScopeUnresolved, causeWindowInvalid,
		causeCostReadFailed, causeCostScanTruncated, causeCostCursorMissing,
		causeCostCursorStalled, causeCostCursorCycle, causeCostRowMalformed,
		causeCostRowOutOfWindow, causeCostRowOtherTenant, causeCostRowTenantMalformed,
		causeCostRowOutOfScope, causeCostRowDimensionMalformed, causeCostRowProvenance,
		causeStaticUnknown, causeStaticNegative,
		causeDynamicUnknown, causeDynamicScanIncomplete, causeDynamicUnverified,
		causeDynamicNegative, causeDynamicStateUnclassified,
		causeThresholdNotFinite, causeThresholdOutOfRange, causeLimitNotPositive,
		causeAmountNotDecidable, causeBudgetCensusTruncated,
		causeThresholdIdentityUnrepresentable,
	} {
		out[string(c)] = true
	}
	for _, f := range []budgetConfigFault{
		configFaultLimitMalformed, configFaultLimitNull,
		configFaultStaticMalformed, configFaultStaticNull, configFaultStaticNegative,
	} {
		out[string(f)] = true
	}
	return out
}()

// evidenceTarget rebuilds the exact rational target and checks it against the
// threshold and limit the envelope itself carries. The stored numerator/denominator
// must be the lowest-terms form, and the printed decimal — when there is one — must
// be the exact decimal of that rational.
func evidenceTarget(d alertEvidenceDecision, limit int64) (*big.Rat, string, bool) {
	if d.TargetNumerator == "" || d.TargetDenominator == "" {
		return nil, evidenceCauseEnvelopeIncomplete, false
	}
	num, ok := canonicalBigInt(d.TargetNumerator)
	if !ok {
		return nil, evidenceCauseDecimalNotCanon, false
	}
	den, ok := canonicalBigInt(d.TargetDenominator)
	if !ok || den.Sign() <= 0 {
		return nil, evidenceCauseDecimalNotCanon, false
	}
	target := new(big.Rat).SetFrac(num, den)
	if target.Num().String() != d.TargetNumerator || target.Denom().String() != d.TargetDenominator {
		// Not lowest terms: two different pairs would describe the same target and
		// hash differently, so the canonical form is required.
		return nil, evidenceCauseDecimalNotCanon, false
	}
	thr, ok := canonicalThresholdText(d.Threshold)
	if !ok {
		return nil, evidenceCauseDecimalNotCanon, false
	}
	if limit <= 0 {
		// A crossing cannot be proven against a non-positive limit.
		return nil, evidenceCauseInconsistent, false
	}
	want := new(big.Rat).Mul(thr, new(big.Rat).SetInt64(limit))
	if want.Cmp(target) != 0 {
		return nil, evidenceCauseInconsistent, false
	}
	if d.TargetMicroUSD != "" {
		decimal, exact := ratExactDecimal(target, maxExactDecimalDigits)
		if !exact || decimal != d.TargetMicroUSD {
			return nil, evidenceCauseDecimalNotCanon, false
		}
	}
	return target, "", true
}

// expectedLegacy is legacyProjection re-derived from the stored class and figure, so
// the classification of the historical column is checked rather than believed.
func expectedLegacy(class string, amount *big.Int) (string, int64, bool) {
	if amount == nil {
		return "", 0, false
	}
	fits := amount.IsInt64()
	switch class {
	case string(amountExact):
		if fits {
			return legacyValueExact, amount.Int64(), true
		}
		return legacyValueLowerBound, math.MaxInt64, true
	case string(amountLowerBound):
		if fits {
			return legacyValueLowerBound, amount.Int64(), true
		}
		return legacyValueLowerBound, math.MaxInt64, true
	}
	return "", 0, false
}

// canonicalBigInt parses a decimal integer and requires it to be in the canonical
// form big.Int prints: no leading zeros, no "+", no "-0", no spaces.
func canonicalBigInt(s string) (*big.Int, bool) {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok || v.String() != s {
		return nil, false
	}
	return v, true
}

// canonicalInt is canonicalBigInt for a value that must also fit the int64 ledger
// column it describes.
func canonicalInt(s string) (int64, bool) {
	v, ok := canonicalBigInt(s)
	if !ok || !v.IsInt64() {
		return 0, false
	}
	return v.Int64(), true
}

// canonicalThresholdText re-parses the stored threshold through the writer's own
// formatting, so the stored text is the shortest round-trip representation the
// operator's value produces and nothing else.
func canonicalThresholdText(s string) (*big.Rat, bool) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return nil, false
	}
	if strconv.FormatFloat(f, 'g', -1, 64) != s {
		return nil, false
	}
	rat, text, cause := normalizedThreshold(f)
	if cause != "" || text != s {
		return nil, false
	}
	return rat, true
}

// legacyThresholdPctFromText re-derives the historical integer percent from the
// stored threshold text, so the identity column the row is keyed on is the one this
// threshold produces.
func legacyThresholdPctFromText(s string) (int, bool) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return legacyThresholdPct(f)
}

func validCrossingResult(s string) bool {
	switch s {
	case string(crossingProven), string(crossingNotReached), string(crossingUnproven):
		return true
	}
	return false
}

func validLegacyValueKind(s string) bool {
	switch s {
	case legacyValueExact, legacyValueLowerBound, legacyValueUnavailable, legacyValueUnverified:
		return true
	}
	return false
}

func validConfigFault(s string) bool {
	switch budgetConfigFault(s) {
	case configFaultNone, configFaultLimitMalformed, configFaultLimitNull,
		configFaultStaticMalformed, configFaultStaticNull, configFaultStaticNegative:
		return true
	}
	return false
}

// rowMatchesEvidence binds the ROW's own cells to the envelope that authorized them.
//
// This is the second half of the A4.2 correction, and the cheaper defect to
// reproduce: keep a perfectly valid envelope, change ONE historical cell of the row,
// and the reader used to return valid — so the DTO copied `legacy_value_kind=exact`
// from the envelope and served the changed number beside it. The envelope commits to
// exactly one legacy projection and to the policy fields the row denormalizes; a row
// that says something else is not described by it.
//
// Timestamps are compared as INSTANTS rather than as text: the two engines render a
// stored timestamp their own way, and a rendering difference is not a mismatch of
// fact. Nothing here is repaired: a disagreement is reported.
func rowMatchesEvidence(rec model.Record, env alertEvidenceEnvelope) bool {
	if spend, ok := int64Cell(rec, colAlertSpend); !ok || spend != env.Legacy.SpendMicroUSD {
		return false
	}
	limit, ok := canonicalInt(env.Policy.LimitMicroUSD)
	if !ok {
		return false
	}
	if got, ok := int64Cell(rec, colAlertLimit); !ok || got != limit {
		return false
	}
	if pct, ok := int64Cell(rec, colThresholdPct); !ok || pct != int64(env.Decision.LegacyPct) {
		return false
	}
	if !textCellEquals(rec, colPeriod, env.Policy.Period) ||
		!textCellEquals(rec, colDimension, env.Policy.Dimension) ||
		!textCellEquals(rec, colDimKey, env.Policy.Key) ||
		!textCellEquals(rec, colSeverity, string(severityForPct(env.Decision.LegacyPct))) {
		return false
	}
	if !timestampCellIs(rec, colTriggeredAt, env.Context.SampleAt) {
		return false
	}
	// The period bucket. An unbounded ("total") period has no window in the envelope
	// and the row carries the zero instant, which is checked as such rather than
	// skipped.
	want := env.Context.WindowStart
	if want == "" {
		want = model.NewTimestamp(time.Time{}).String()
	}
	return timestampCellIs(rec, colPeriodStart, want)
}

// timestampCellIs compares a timestamp cell with an expected timestamp text by the
// instant both denote. The cell must still BE a timestamp: an absent, null or
// non-string cell is not "equal to whatever was expected".
func timestampCellIs(rec model.Record, col, want string) bool {
	text, ok := textCell(rec, col)
	if !ok {
		return false
	}
	got, err := model.ParseTimestamp(text)
	if err != nil {
		return false
	}
	expect, err := model.ParseTimestamp(want)
	if err != nil {
		return false
	}
	return got.Time().Equal(expect.Time())
}

// textCell reads a text cell by type: a present non-string is not a string, and a
// missing cell is not an empty one.
func textCell(r model.Record, col string) (string, bool) {
	cell, present := r[col]
	if !present || cell == nil {
		return "", false
	}
	text, ok := cell.(string)
	return text, ok
}

// evidenceEvaluatedAt is a small convenience for the DTO: the instant the envelope
// says the evaluation ran, or the zero value when there is no valid envelope.
func (i interpretedEvidence) evaluatedAt() (time.Time, bool) {
	if i.State != evidenceValid || i.Envelope == nil {
		return time.Time{}, false
	}
	ts, err := model.ParseTimestamp(i.Envelope.Context.EvaluatedAt)
	if err != nil {
		return time.Time{}, false
	}
	return ts.Time(), true
}
