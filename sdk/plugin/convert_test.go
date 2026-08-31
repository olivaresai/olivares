// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package plugin

import (
	"maps"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	pb "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

func TestDescriptorRoundTrip(t *testing.T) {
	in := sdk.Descriptor{
		Name:        "olivares.example",
		Version:     "1.2.3",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Example",
		Description: "demo",
		ConfigFields: []sdk.ConfigField{
			{Key: "dsn", Type: sdk.FieldString, Required: true, Secret: true, Description: "data source"},
			{Key: "workers", Type: sdk.FieldInt, Default: "4"},
		},
		Surfaces: []string{"knowledge.document", "observation.edge"},
	}
	out := descriptorFromPB(descriptorToPB(in))
	if out.Name != in.Name || out.Version != in.Version || out.APIVersion != in.APIVersion {
		t.Errorf("scalar fields lost: %+v", out)
	}
	if out.Type != sdk.TypeSource {
		t.Errorf("type lost: %q", out.Type)
	}
	if len(out.ConfigFields) != 2 || out.ConfigFields[0].Key != "dsn" || !out.ConfigFields[0].Secret {
		t.Errorf("config fields lost: %+v", out.ConfigFields)
	}
	if out.ConfigFields[1].Type != sdk.FieldInt || out.ConfigFields[1].Default != "4" {
		t.Errorf("config field type/default lost: %+v", out.ConfigFields[1])
	}
	if !reflect.DeepEqual(out.Surfaces, in.Surfaces) {
		t.Errorf("surfaces lost: %+v", out.Surfaces)
	}
}

func TestComponentTypeMappingExhaustive(t *testing.T) {
	for _, ct := range []sdk.ComponentType{sdk.TypeSource, sdk.TypeOutput, sdk.TypeContentSource, sdk.TypeModule} {
		got := componentTypeFromPB[componentTypeToPB[ct]]
		if got != ct {
			t.Errorf("component type %q did not round-trip (got %q)", ct, got)
		}
	}
}

func TestObservationRoundTrip(t *testing.T) {
	when := time.Date(2026, 6, 2, 9, 30, 0, 0, time.UTC)
	cases := []model.Observation{
		model.EdgeObservation{
			OriginKind: "agent", OriginRef: "claude-1", ResourceKind: "postgres.table",
			ResourceRef: "public.customers", Mode: model.ModeReadWrite, Source: model.SignalPGAudit,
			Confidence: model.ConfidenceAttributed, ToolRef: "sql.exec", ObservedAt: when,
		},
		model.CostSample{
			ProviderRef: "anthropic", ModelRef: "claude", SessionRef: "s1",
			InputTokens: 100, OutputTokens: 50, CostMicroUSD: 1234, OccurredAt: when,
			CacheReadTokens: 60, CacheCreation1hTokens: 10, CacheCreation5mTokens: 30,
			WorkspaceRef: "wrkspc_01", APIKeyRef: "apikey_01", Actor: "dev@example.com", ServiceTier: "priority",
			ContextWindow: "200k-1M", InferenceGeo: "us", Speed: "fast",
			Gateway: model.GatewayBedrockMantle, Provenance: model.ProvenanceBilled, CostType: "tokens",
			Labels: map[string]string{"team": "payments", "project": "atlas"},
		},
		model.FindingReport{
			Kind: "guardrail", Severity: model.SeverityHigh, SubjectKind: "agent",
			SubjectRef: "claude-1", Title: "prompt injection", DetailHash: "abc123", OccurredAt: when,
			OWASPLLM: []string{"LLM01:2025"}, OWASPASI: []string{"ASI01"}, ATLAS: []string{"AML.T0051.001"},
		},
		model.MetricSample{
			Name: "claude_code.lines_of_code.count", Value: 1234, Unit: "lines", Additive: true,
			SubjectKind: "developer", SubjectRef: "dev@example.com", OccurredAt: when,
			Dimensions: map[string]string{"type": "added", "model": "claude-opus-4-8"},
			Labels:     map[string]string{"team": "payments"},
		},
	}
	for _, in := range cases {
		msg, err := observationToPB(in)
		if err != nil {
			t.Fatalf("observationToPB(%T): %v", in, err)
		}
		out, err := observationFromPB(msg)
		if err != nil {
			t.Fatalf("observationFromPB(%T): %v", in, err)
		}
		if out.ObservationType() != in.ObservationType() {
			t.Errorf("type changed: %q -> %q", in.ObservationType(), out.ObservationType())
		}
		switch want := in.(type) {
		case model.EdgeObservation:
			got := out.(model.EdgeObservation)
			if got.ResourceRef != want.ResourceRef || got.Mode != want.Mode || !got.ObservedAt.Equal(want.ObservedAt) {
				t.Errorf("edge round-trip mismatch: %+v vs %+v", got, want)
			}
			if !maps.Equal(got.Labels, want.Labels) {
				t.Errorf("edge labels round-trip mismatch: %v vs %v", got.Labels, want.Labels)
			}
		case model.CostSample:
			got := out.(model.CostSample)
			// Field-exact: a missed mapping in costToPB/costFromPB silently zeroes a
			// field on the wire, so the whole struct must round-trip identically.
			// (reflect.DeepEqual since: Labels makes the struct incomparable.)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("cost round-trip mismatch:\n got  %+v\n want %+v", got, want)
			}
		case model.FindingReport:
			got := out.(model.FindingReport)
			// Field-exact for the same reason as CostSample: a missed mapping in
			// findingToPB/findingFromPB silently drops a field on the wire — exactly
			// the framework-ref defect S142 closed. (reflect.DeepEqual since
			// S142: the taxonomy slices make the struct incomparable.)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("finding round-trip mismatch:\n got  %+v\n want %+v", got, want)
			}
		case model.MetricSample:
			got := out.(model.MetricSample)
			// Field-exact: a missed mapping in metricToPB/metricFromPB silently
			// zeroes a field on the wire. reflect.DeepEqual since the Dimensions/Labels
			// maps make the struct incomparable.
			if !reflect.DeepEqual(got, want) {
				t.Errorf("metric round-trip mismatch:\n got  %+v\n want %+v", got, want)
			}
		}
	}
}

func TestObservationFromPBRejectsEmpty(t *testing.T) {
	if _, err := observationFromPB(&pb.Observation{}); err == nil {
		t.Error("decoding an empty observation oneof must error, not silently succeed")
	}
}

// TestObservationToPBAcceptsPointer guards the regression where a *DTO satisfied
// the sealed interface but hit the default branch and failed to encode.
func TestObservationToPBAcceptsPointer(t *testing.T) {
	cases := []model.Observation{
		&model.EdgeObservation{ResourceRef: "p", Mode: model.ModeWrite},
		&model.CostSample{CostMicroUSD: 9},
		&model.FindingReport{Title: "f", Severity: model.SeverityLow},
		&model.MetricSample{Name: "claude_code.commit.count", Value: 7},
	}
	for _, in := range cases {
		msg, err := observationToPB(in)
		if err != nil {
			t.Fatalf("pointer observation %T should encode, got: %v", in, err)
		}
		out, err := observationFromPB(msg)
		if err != nil {
			t.Fatalf("decode %T: %v", in, err)
		}
		if out.ObservationType() != in.ObservationType() {
			t.Errorf("pointer %T encoded as %q", in, out.ObservationType())
		}
	}
}

func TestTimestampNilSafety(t *testing.T) {
	// A zero time encodes to a nil Timestamp (compact), and a nil Timestamp
	// decodes back to the zero time — never a panic.
	if tsToPB(time.Time{}) != nil {
		t.Error("zero time should encode to nil timestamp")
	}
	if !tsFromPB(nil).IsZero() {
		t.Error("nil timestamp should decode to zero time")
	}
}

func TestConfigFromPBNilSafety(t *testing.T) {
	c := configFromPB(nil)
	if c.Settings == nil {
		t.Error("configFromPB(nil) must yield a non-nil settings map")
	}
}

// TestFindingFrameworkRefsNormalization pins the nil-vs-empty posture of the
// Taxonomy slices (S142): proto3 cannot distinguish "absent" from
// "empty list", and the model's "none" representation is nil, so BOTH a nil
// and an allocated-but-empty slice must decode back to nil — never to an
// allocated empty slice that breaks Go-side equality with the pre-wire value.
func TestFindingFrameworkRefsNormalization(t *testing.T) {
	cases := []struct {
		name string
		in   model.FindingReport
	}{
		{"nil slices", model.FindingReport{Kind: "guardrail", Title: "f"}},
		{"empty slices", model.FindingReport{
			Kind: "guardrail", Title: "f",
			OWASPLLM: []string{}, OWASPASI: []string{}, ATLAS: []string{},
		}},
	}
	for _, tc := range cases {
		out := findingFromPB(findingToPB(tc.in))
		if out.OWASPLLM != nil || out.OWASPASI != nil || out.ATLAS != nil {
			t.Errorf("%s: framework refs must decode to nil, got llm=%#v asi=%#v atlas=%#v",
				tc.name, out.OWASPLLM, out.OWASPASI, out.ATLAS)
		}
	}
	// Populated sets survive byte-identically (order preserved: the producer is
	// responsible for determinism, the wire must not reorder).
	in := model.FindingReport{
		Kind:     "redteam",
		OWASPLLM: []string{"LLM01:2025", "LLM02:2025"},
		OWASPASI: []string{"ASI01"},
		ATLAS:    []string{"AML.T0051.001"},
	}
	out := findingFromPB(findingToPB(in))
	if !reflect.DeepEqual(out.OWASPLLM, in.OWASPLLM) ||
		!reflect.DeepEqual(out.OWASPASI, in.OWASPASI) ||
		!reflect.DeepEqual(out.ATLAS, in.ATLAS) {
		t.Errorf("framework refs round-trip mismatch:\n got  %+v\n want %+v", out, in)
	}
}

// TestCostSpeedRoundTrip guards the wire mapping of the speed dimension
// (S142: previously silently dropped). Empty stays empty — "not reported" is
// the zero value on both sides, no normalization needed for a scalar.
func TestCostSpeedRoundTrip(t *testing.T) {
	if out := costFromPB(costToPB(model.CostSample{Speed: "fast"})); out.Speed != "fast" {
		t.Errorf("speed lost on the wire: %q", out.Speed)
	}
	if out := costFromPB(costToPB(model.CostSample{})); out.Speed != "" {
		t.Errorf("unset speed must round-trip empty, got %q", out.Speed)
	}
}

// TestNotificationFieldsNormalization pins notificationFromPB to the same
// empty-map-to-nil posture labelsFromPB gives observation labels (S142 made
// this consistent): a fields-less notification round-trips to nil Fields, and
// a populated map survives intact.
func TestNotificationFieldsNormalization(t *testing.T) {
	when := time.Date(2026, 6, 11, 8, 0, 0, 0, time.UTC)
	for name, fields := range map[string]map[string]string{
		"nil fields":   nil,
		"empty fields": {},
	} {
		in := sdk.Notification{Type: "finding", Title: "t", Tenant: "t1", Fields: fields, Time: when}
		if out := notificationFromPB(notificationToPB(in)); out.Fields != nil {
			t.Errorf("%s: must decode to nil Fields, got %#v", name, out.Fields)
		}
	}
	in := sdk.Notification{
		Type: "finding", Title: "t", Body: "b", Severity: model.SeverityHigh, Tenant: "t1",
		Fields: map[string]string{"owasp_llm": "LLM01:2025", "atlas": "AML.T0051.001"},
		Time:   when,
	}
	out := notificationFromPB(notificationToPB(in))
	if !maps.Equal(out.Fields, in.Fields) {
		t.Errorf("populated fields round-trip mismatch: %v vs %v", out.Fields, in.Fields)
	}
	if out.Type != in.Type || out.Severity != in.Severity || !out.Time.Equal(in.Time) {
		t.Errorf("notification scalars lost: %+v", out)
	}
}

// TestNotificationActionsRoundTrip pins the interactive Actions to the same
// empty->nil posture as Fields and proves a populated set survives the wire intact
// — the guarantee that an out-of-process output renders the same approve/deny
// buttons as the in-process one.
func TestNotificationActionsRoundTrip(t *testing.T) {
	// No actions decodes back to nil, never an empty slice.
	if out := notificationFromPB(notificationToPB(sdk.Notification{Type: "finding"})); out.Actions != nil {
		t.Errorf("actions-less notification must decode to nil Actions, got %#v", out.Actions)
	}
	in := sdk.Notification{
		Type: "approval.requested", Title: "Approval needed",
		Actions: []sdk.NotificationAction{
			{Label: "Approve", ID: "olivares_approve", Value: "approve:appr_123", Style: "primary"},
			{Label: "Deny", ID: "olivares_deny", Value: "deny:appr_123", Style: "danger"},
		},
	}
	out := notificationFromPB(notificationToPB(in))
	if len(out.Actions) != len(in.Actions) {
		t.Fatalf("actions count = %d, want %d", len(out.Actions), len(in.Actions))
	}
	for i, a := range in.Actions {
		if out.Actions[i] != a {
			t.Errorf("action[%d] round-trip mismatch: %+v vs %+v", i, out.Actions[i], a)
		}
	}
}

func TestContentSourceMessageRoundTrips(t *testing.T) {
	when := time.Date(2026, 7, 9, 13, 45, 30, 123000000, time.FixedZone("CEST", 2*60*60))
	ref := sdk.DocRef{
		DocID:       "doc-123",
		Title:       "Launch plan",
		ContentType: "text/markdown",
		ModifiedAt:  when,
	}
	refOut := contentDocRefFromPB(contentDocRefToPB(ref))
	if refOut.DocID != ref.DocID || refOut.Title != ref.Title || refOut.ContentType != ref.ContentType || !refOut.ModifiedAt.Equal(ref.ModifiedAt) {
		t.Errorf("doc ref round-trip mismatch:\n got  %+v\n want %+v", refOut, ref)
	}

	doc := sdk.Document{
		Source:         sdk.SourceKind("confluence"),
		DocID:          ref.DocID,
		Title:          ref.Title,
		Body:           []byte("# Launch\nbody"),
		ContentType:    ref.ContentType,
		ACL:            []string{"group:eng", "space:AI"},
		Classification: "internal",
		SpaceRef:       "space:AI",
		ModifiedAt:     when,
		Attributes:     map[string]string{"url_path": "/wiki/launch", "author_label": "Platform"},
		ExternalLabels: []string{"purview:confidential"},
	}
	docOut := contentDocumentFromPB(contentDocumentToPB(doc))
	docWant := doc
	docWant.ModifiedAt = docOut.ModifiedAt
	if !reflect.DeepEqual(docOut, docWant) || !docOut.ModifiedAt.Equal(doc.ModifiedAt) {
		t.Errorf("document round-trip mismatch:\n got  %+v\n want %+v", docOut, doc)
	}

	acl := sdk.ACLResult{ACL: []string{"group:eng"}, ExternalLabels: []string{"uc:pii"}, Classification: "confidential"}
	aclOut := contentACLResultFromPB(contentACLResultToPB(acl))
	if !reflect.DeepEqual(aclOut, acl) {
		t.Errorf("acl result round-trip mismatch:\n got  %+v\n want %+v", aclOut, acl)
	}
}

func TestContentSourceNilNormalization(t *testing.T) {
	for name, acl := range map[string][]string{
		"nil acl":   nil,
		"empty acl": {},
	} {
		out := contentACLResultFromPB(contentACLResultToPB(sdk.ACLResult{ACL: acl, ExternalLabels: []string{}}))
		if out.ACL != nil || out.ExternalLabels != nil {
			t.Errorf("%s: empty acl/external labels must decode to nil, got acl=%#v labels=%#v", name, out.ACL, out.ExternalLabels)
		}
	}
	docOut := contentDocumentFromPB(contentDocumentToPB(sdk.Document{
		ACL:            []string{},
		Attributes:     map[string]string{},
		ExternalLabels: []string{},
	}))
	if docOut.ACL != nil || docOut.Attributes != nil || docOut.ExternalLabels != nil {
		t.Errorf("document empty collections must decode to nil, got acl=%#v attrs=%#v labels=%#v", docOut.ACL, docOut.Attributes, docOut.ExternalLabels)
	}
}

func TestContentChangeRoundTrip(t *testing.T) {
	ref := sdk.DocRef{DocID: "doc-1", Title: "Doc"}
	page := sdk.DeltaPage{NextToken: "page-2", ResumeToken: "resume-9"}
	for _, kind := range []sdk.ChangeKind{sdk.ChangeContent, sdk.ChangeMetadata, sdk.ChangeACL, sdk.ChangeDeleted} {
		in := sdk.Change{DocRef: ref, ChangeKind: kind}
		out, ok := contentChangeFromPB(contentChangeToPB(in, page))
		if !ok {
			t.Fatalf("change kind %q decoded as metadata-only", kind)
		}
		if !reflect.DeepEqual(out, in) {
			t.Errorf("change round-trip mismatch:\n got  %+v\n want %+v", out, in)
		}
	}

	var outPage sdk.DeltaPage
	applyContentDeltaMetaFromPB(&outPage, contentDeltaPageMetaToPB(sdk.DeltaPage{
		NextToken: "next", ResumeToken: "resume", Expired: true,
	}))
	if outPage.NextToken != "next" || outPage.ResumeToken != "resume" || !outPage.Expired {
		t.Errorf("delta page metadata lost: %+v", outPage)
	}
}
