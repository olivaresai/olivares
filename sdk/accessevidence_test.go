// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

func testEnvelope() sdk.AccessEvidenceEnvelope {
	return sdk.AccessEvidenceEnvelope{
		SchemaVersion:    sdk.AccessEvidenceSchemaVersion,
		ProducerInstance: "collector-a",
		SourceEventID:    "evt-1",
		EventType:        sdk.EventTypeActionObservation,
		AdapterVersion:   "1.0.0",
		OccurredAt:       sdk.FormatEvidenceTime(time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)),
	}
}

func testObservation() sdk.ActionObservationContent {
	return sdk.ActionObservationContent{
		SchemaVersion: sdk.AccessEvidenceSchemaVersion,
		Question: sdk.AccessQuestion{
			SchemaVersion:    sdk.AccessEvidenceSchemaVersion,
			ActorRef:         "agent-7",
			SourceInstance:   "pg-prod-1",
			ResourceKind:     "postgres.table",
			ResourceRef:      "public.customers",
			Action:           "SELECT",
			ActionVocabulary: "postgres.sql.v1",
			Context:          map[string]string{"role": "reader", "clock": "2026-09-06T10:00:00Z"},
		},
		Stage:      sdk.StageDispatched,
		Mediation:  sdk.MediationOlivaresPEP,
		Prevention: sdk.PreventionCanPrevent,
	}
}

// TestRecordDigestIsStableAndSensitive is the property idempotency rests on: the
// same fact digests the same way every time, and any change to the fact or to
// its identity changes the digest.
func TestRecordDigestIsStableAndSensitive(t *testing.T) {
	t.Parallel()
	base, err := sdk.RecordDigest(testEnvelope(), testObservation())
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	again, err := sdk.RecordDigest(testEnvelope(), testObservation())
	if err != nil {
		t.Fatalf("digest again: %v", err)
	}
	if base != again {
		t.Fatalf("the same record digested differently: %s vs %s", base, again)
	}

	mutations := map[string]func(*sdk.AccessEvidenceEnvelope, *sdk.ActionObservationContent){
		"a different producer": func(e *sdk.AccessEvidenceEnvelope, _ *sdk.ActionObservationContent) {
			e.ProducerInstance = "collector-b"
		},
		"a different source event": func(e *sdk.AccessEvidenceEnvelope, _ *sdk.ActionObservationContent) {
			e.SourceEventID = "evt-2"
		},
		"a different adapter version": func(e *sdk.AccessEvidenceEnvelope, _ *sdk.ActionObservationContent) {
			e.AdapterVersion = "1.0.1"
		},
		"a different occurrence instant": func(e *sdk.AccessEvidenceEnvelope, _ *sdk.ActionObservationContent) {
			e.OccurredAt = sdk.FormatEvidenceTime(time.Date(2026, 9, 6, 10, 0, 1, 0, time.UTC))
		},
		"a different stage": func(_ *sdk.AccessEvidenceEnvelope, o *sdk.ActionObservationContent) {
			o.Stage = sdk.StageEffectConfirmed
		},
		"a different prevention capability": func(_ *sdk.AccessEvidenceEnvelope, o *sdk.ActionObservationContent) {
			o.Prevention = sdk.PreventionUnknown
		},
		"one changed context fact": func(_ *sdk.AccessEvidenceEnvelope, o *sdk.ActionObservationContent) {
			o.Question.Context["role"] = "writer"
		},
		"a different resource on the same name": func(_ *sdk.AccessEvidenceEnvelope, o *sdk.ActionObservationContent) {
			o.Question.SourceInstance = "pg-prod-2"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			env, content := testEnvelope(), testObservation()
			mutate(&env, &content)
			got, err := sdk.RecordDigest(env, content)
			if err != nil {
				t.Fatalf("digest: %v", err)
			}
			if got == base {
				t.Fatalf("%s produced the same digest %s; a conflicting delivery would read as a duplicate", name, got)
			}
		})
	}
}

// TestRecordDigestRefusesAMismatchedFamily proves the digest cannot be computed
// for an envelope that labels the content as something it is not — the identity
// tuple would otherwise name a family the bytes are not from.
func TestRecordDigestRefusesAMismatchedFamily(t *testing.T) {
	t.Parallel()
	env := testEnvelope()
	env.EventType = sdk.EventTypePolicyArtifact
	if _, err := sdk.RecordDigest(env, testObservation()); err == nil {
		t.Fatal("a mislabelled envelope produced a digest")
	}
}

// TestQuestionDigestIsIndependentOfMapOrder pins the determinism the whole
// scheme needs: Go map iteration order is randomized, so a canonicalisation that
// depended on it would make the same question hash differently between runs.
func TestQuestionDigestIsIndependentOfMapOrder(t *testing.T) {
	t.Parallel()
	q1 := testObservation().Question
	q1.Context = map[string]string{"a": "1", "b": "2", "c": "3", "d": "4", "e": "5"}
	q2 := testObservation().Question
	q2.Context = map[string]string{"e": "5", "d": "4", "c": "3", "b": "2", "a": "1"}

	d1, err := q1.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	for i := 0; i < 32; i++ {
		d2, err := q2.Digest()
		if err != nil {
			t.Fatalf("digest: %v", err)
		}
		if d1 != d2 {
			t.Fatalf("the same question digested differently across map orders: %s vs %s", d1, d2)
		}
	}
}

// TestDigestsAreDomainSeparated proves a value hashed for one purpose cannot be
// replayed as another: the artifact digest of some bytes is never the question
// digest or record digest of anything.
func TestDigestsAreDomainSeparated(t *testing.T) {
	t.Parallel()
	body := []byte("permit(principal, action, resource);")
	artifact := sdk.ArtifactContentDigest(body)
	question, err := testObservation().Question.Digest()
	if err != nil {
		t.Fatalf("question digest: %v", err)
	}
	record, err := sdk.RecordDigest(testEnvelope(), testObservation())
	if err != nil {
		t.Fatalf("record digest: %v", err)
	}
	for _, pair := range [][2]string{{artifact, question}, {artifact, record}, {question, record}} {
		if pair[0] == pair[1] {
			t.Fatalf("two domains produced the same digest %s", pair[0])
		}
	}
	// The artifact digest is over the exact bytes: HTML-significant characters
	// hash as themselves, so a policy containing < or & is not silently escaped
	// into a different document.
	if sdk.ArtifactContentDigest([]byte(`when {a < b && c}`)) == sdk.ArtifactContentDigest([]byte(`when {a < b}`)) {
		t.Fatal("escaping changed the artifact preimage")
	}
}

// TestAccessQuestionValidityAndSubject covers the two predicates a consumer
// relies on: a question that names nobody and nothing is not evidence, and the
// effective subject is the delegated one when delegation happened.
func TestAccessQuestionValidityAndSubject(t *testing.T) {
	t.Parallel()
	q := testObservation().Question
	if !q.Valid() {
		t.Fatal("a question with an actor and an action reported invalid")
	}
	if got := q.EffectiveSubject(); got != "agent-7" {
		t.Fatalf("effective subject = %q, want the actor when no delegation happened", got)
	}
	q.SubjectRef = "user-9"
	if got := q.EffectiveSubject(); got != "user-9" {
		t.Fatalf("effective subject = %q, want the delegated subject", got)
	}
	q.ActorRef = "   "
	if q.Valid() {
		t.Fatal("a whitespace-only actor passed validation")
	}
}

// TestArtifactAvailabilityReconstructibility pins the axis the whole
// "reconstructible" claim rests on: only a RETAINED artifact carries its rules.
func TestArtifactAvailabilityReconstructibility(t *testing.T) {
	t.Parallel()
	if !sdk.AvailabilityRetained.LocallyReconstructible() {
		t.Error("a retained artifact must be locally reconstructible")
	}
	for _, a := range []sdk.ArtifactAvailability{
		sdk.AvailabilityExternalRetained, sdk.AvailabilityExternalRestricted, sdk.AvailabilityAbsent,
	} {
		if a.LocallyReconstructible() {
			t.Errorf("availability %q claimed local reconstructibility", a)
		}
	}
}

// TestAccessEvidenceVocabulariesRejectUnknownValues is the deny-closed check
// across every closed vocabulary: the empty value and an invented value are both
// refused, so an unknown never reads as permissive.
func TestAccessEvidenceVocabulariesRejectUnknownValues(t *testing.T) {
	t.Parallel()
	checks := map[string]func(string) bool{
		"artifact origin":       func(s string) bool { return sdk.ArtifactOrigin(s).Valid() },
		"artifact availability": func(s string) bool { return sdk.ArtifactAvailability(s).Valid() },
		"authority subject":     func(s string) bool { return sdk.AuthoritySubjectKind(s).Valid() },
		"authority transition":  func(s string) bool { return sdk.AuthorityTransitionType(s).Valid() },
		"snapshot completeness": func(s string) bool { return sdk.SnapshotCompleteness(s).Valid() },
		"observation stage":     func(s string) bool { return sdk.ObservationStage(s).Valid() },
		"mediation":             func(s string) bool { return sdk.ObservationMediation(s).Valid() },
		"prevention":            func(s string) bool { return sdk.PreventionCapability(s).Valid() },
		"effect confirmation":   func(s string) bool { return sdk.EffectConfirmation(s).Valid() },
		"decision purpose":      func(s string) bool { return sdk.DecisionPurpose(s).Valid() },
		"decision outcome":      func(s string) bool { return sdk.AccessDecisionOutcome(s).Valid() },
		"decision disposition":  func(s string) bool { return sdk.DecisionDisposition(s).Valid() },
		"replay completeness":   func(s string) bool { return sdk.ReplayCompleteness(s).Valid() },
	}
	for name, valid := range checks {
		for _, bad := range []string{"", "  ", "allow_everything", "ALLOW"} {
			if valid(bad) {
				t.Errorf("%s accepted %q", name, bad)
			}
		}
	}
}

// TestEvidenceTimeRoundTrip pins the canonical instant format producers must
// use. Fixed width matters: lexical order has to equal chronological order, and
// the digested text has to be the stored text.
func TestEvidenceTimeRoundTrip(t *testing.T) {
	t.Parallel()
	instant := time.Date(2026, 9, 6, 10, 0, 0, 123456789, time.UTC)
	text := sdk.FormatEvidenceTime(instant)
	if len(text) != len("2026-09-06T10:00:00.000000000Z") {
		t.Fatalf("canonical instant %q is not fixed width", text)
	}
	back, err := sdk.ParseEvidenceTime(text)
	if err != nil || !back.Equal(instant) {
		t.Fatalf("round trip = %v err %v, want %v", back, err, instant)
	}
	if _, err := sdk.ParseEvidenceTime("2026-09-06T10:00:00Z"); err == nil {
		t.Fatal("a non-canonical instant parsed; producers would emit digests nobody can reproduce")
	}
	// Lexical order equals chronological order.
	earlier := sdk.FormatEvidenceTime(instant.Add(-time.Nanosecond))
	if !(strings.Compare(earlier, text) < 0) {
		t.Fatalf("lexical order broke: %q is not < %q", earlier, text)
	}
}

// TestAccessEvidenceContentFamiliesAreDisjoint pins the property that lets four
// per-relation unique indexes behave as one global identity: no two families
// share an event type.
func TestAccessEvidenceContentFamiliesAreDisjoint(t *testing.T) {
	t.Parallel()
	contents := []sdk.AccessEvidenceContent{
		sdk.PolicyArtifactContent{}, sdk.AuthorityTransitionContent{},
		sdk.ActionObservationContent{}, sdk.AuthorizationDecisionContent{},
	}
	seen := make(map[string]bool, len(contents))
	for _, c := range contents {
		et := c.AccessEvidenceEventType()
		if et == "" {
			t.Fatalf("%T declares no event type", c)
		}
		if seen[et] {
			t.Fatalf("event type %q is claimed by two families", et)
		}
		seen[et] = true
	}
}
