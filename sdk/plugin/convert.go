// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package plugin

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	pb "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

// This file is the single boundary where the in-memory SDK types meet the
// generated wire types. Keeping every conversion here means the wire encoding
// is reviewed in one place and the rest of the glue speaks SDK types only.

func tsToPB(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func tsFromPB(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

var componentTypeToPB = map[sdk.ComponentType]pb.ComponentType{
	sdk.TypeSource:        pb.ComponentType_COMPONENT_TYPE_SOURCE,
	sdk.TypeOutput:        pb.ComponentType_COMPONENT_TYPE_OUTPUT,
	sdk.TypeContentSource: pb.ComponentType_COMPONENT_TYPE_CONTENT_SOURCE,
	sdk.TypeModule:        pb.ComponentType_COMPONENT_TYPE_MODULE,
}

var componentTypeFromPB = map[pb.ComponentType]sdk.ComponentType{
	pb.ComponentType_COMPONENT_TYPE_SOURCE:         sdk.TypeSource,
	pb.ComponentType_COMPONENT_TYPE_OUTPUT:         sdk.TypeOutput,
	pb.ComponentType_COMPONENT_TYPE_CONTENT_SOURCE: sdk.TypeContentSource,
	pb.ComponentType_COMPONENT_TYPE_MODULE:         sdk.TypeModule,
}

func descriptorToPB(d sdk.Descriptor) *pb.Descriptor {
	fields := make([]*pb.ConfigField, len(d.ConfigFields))
	for i, f := range d.ConfigFields {
		fields[i] = &pb.ConfigField{
			Key:         f.Key,
			Type:        string(f.Type),
			Required:    f.Required,
			Default:     f.Default,
			Secret:      f.Secret,
			Description: f.Description,
		}
	}
	return &pb.Descriptor{
		Name:         d.Name,
		Version:      d.Version,
		ApiVersion:   d.APIVersion,
		Type:         componentTypeToPB[d.Type],
		Title:        d.Title,
		Description:  d.Description,
		ConfigFields: fields,
		Surfaces:     d.Surfaces,
	}
}

func descriptorFromPB(d *pb.Descriptor) sdk.Descriptor {
	if d == nil {
		return sdk.Descriptor{}
	}
	fields := make([]sdk.ConfigField, len(d.GetConfigFields()))
	for i, f := range d.GetConfigFields() {
		fields[i] = sdk.ConfigField{
			Key:         f.GetKey(),
			Type:        sdk.ConfigFieldType(f.GetType()),
			Required:    f.GetRequired(),
			Default:     f.GetDefault(),
			Secret:      f.GetSecret(),
			Description: f.GetDescription(),
		}
	}
	return sdk.Descriptor{
		Name:         d.GetName(),
		Version:      d.GetVersion(),
		APIVersion:   d.GetApiVersion(),
		Type:         componentTypeFromPB[d.GetType()],
		Title:        d.GetTitle(),
		Description:  d.GetDescription(),
		ConfigFields: fields,
		Surfaces:     stringsFromPB(d.GetSurfaces()),
	}
}

func configToPB(c sdk.Config) *pb.Config {
	return &pb.Config{Settings: c.Settings}
}

func configFromPB(c *pb.Config) sdk.Config {
	if c == nil {
		return sdk.Config{Settings: map[string]string{}}
	}
	s := c.GetSettings()
	if s == nil {
		s = map[string]string{}
	}
	return sdk.Config{Settings: s}
}

func edgeToPB(o model.EdgeObservation) *pb.EdgeObservation {
	return &pb.EdgeObservation{
		OriginKind:   o.OriginKind,
		OriginRef:    o.OriginRef,
		ResourceKind: o.ResourceKind,
		ResourceRef:  o.ResourceRef,
		Mode:         string(o.Mode),
		Source:       string(o.Source),
		Confidence:   string(o.Confidence),
		ToolRef:      o.ToolRef,
		ObservedAt:   tsToPB(o.ObservedAt),
		Labels:       o.Labels,
	}
}

func edgeFromPB(o *pb.EdgeObservation) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   o.GetOriginKind(),
		OriginRef:    o.GetOriginRef(),
		ResourceKind: o.GetResourceKind(),
		ResourceRef:  o.GetResourceRef(),
		Mode:         model.AccessMode(o.GetMode()),
		Source:       model.SignalSource(o.GetSource()),
		Confidence:   model.Confidence(o.GetConfidence()),
		ToolRef:      o.GetToolRef(),
		ObservedAt:   tsFromPB(o.GetObservedAt()),
		Labels:       labelsFromPB(o.GetLabels()),
	}
}

// labelsFromPB normalizes an empty proto map to nil, so a labels-less
// observation round-trips to the model's "none" representation (nil), not an
// allocated empty map.
func labelsFromPB(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

// stringsFromPB normalizes an empty proto repeated field to nil — the slice
// analog of labelsFromPB. proto3 cannot distinguish "absent" from "empty
// list", and the model's "none" representation is nil (see the comment
// on model.FindingReport), so decoding empty -> nil keeps Go-side equality
// with the pre-wire value (S142).
func stringsFromPB(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func costToPB(o model.CostSample) *pb.CostSample {
	return &pb.CostSample{
		ProviderRef:            o.ProviderRef,
		ModelRef:               o.ModelRef,
		SessionRef:             o.SessionRef,
		InputTokens:            o.InputTokens,
		OutputTokens:           o.OutputTokens,
		CostMicroUsd:           o.CostMicroUSD,
		OccurredAt:             tsToPB(o.OccurredAt),
		CacheReadTokens:        o.CacheReadTokens,
		CacheCreation_1HTokens: o.CacheCreation1hTokens,
		CacheCreation_5MTokens: o.CacheCreation5mTokens,
		WorkspaceRef:           o.WorkspaceRef,
		ApiKeyRef:              o.APIKeyRef,
		Actor:                  o.Actor,
		ServiceTier:            o.ServiceTier,
		ContextWindow:          o.ContextWindow,
		InferenceGeo:           o.InferenceGeo,
		Speed:                  o.Speed,
		Gateway:                string(o.Gateway),
		Provenance:             string(o.Provenance),
		CostType:               o.CostType,
		Labels:                 o.Labels,
	}
}

func costFromPB(o *pb.CostSample) model.CostSample {
	return model.CostSample{
		ProviderRef:           o.GetProviderRef(),
		ModelRef:              o.GetModelRef(),
		SessionRef:            o.GetSessionRef(),
		InputTokens:           o.GetInputTokens(),
		OutputTokens:          o.GetOutputTokens(),
		CostMicroUSD:          o.GetCostMicroUsd(),
		OccurredAt:            tsFromPB(o.GetOccurredAt()),
		CacheReadTokens:       o.GetCacheReadTokens(),
		CacheCreation1hTokens: o.GetCacheCreation_1HTokens(),
		CacheCreation5mTokens: o.GetCacheCreation_5MTokens(),
		WorkspaceRef:          o.GetWorkspaceRef(),
		APIKeyRef:             o.GetApiKeyRef(),
		Actor:                 o.GetActor(),
		ServiceTier:           o.GetServiceTier(),
		ContextWindow:         o.GetContextWindow(),
		InferenceGeo:          o.GetInferenceGeo(),
		Speed:                 o.GetSpeed(),
		Gateway:               model.Gateway(o.GetGateway()),
		Provenance:            model.CostProvenance(o.GetProvenance()),
		CostType:              o.GetCostType(),
		Labels:                labelsFromPB(o.GetLabels()),
	}
}

func findingToPB(o model.FindingReport) *pb.FindingReport {
	return &pb.FindingReport{
		Kind:        o.Kind,
		Severity:    string(o.Severity),
		SubjectKind: o.SubjectKind,
		SubjectRef:  o.SubjectRef,
		Title:       o.Title,
		DetailHash:  o.DetailHash,
		OccurredAt:  tsToPB(o.OccurredAt),
		OwaspLlm:    o.OWASPLLM,
		OwaspAsi:    o.OWASPASI,
		Atlas:       o.ATLAS,
	}
}

func findingFromPB(o *pb.FindingReport) model.FindingReport {
	return model.FindingReport{
		Kind:        o.GetKind(),
		Severity:    model.Severity(o.GetSeverity()),
		SubjectKind: o.GetSubjectKind(),
		SubjectRef:  o.GetSubjectRef(),
		Title:       o.GetTitle(),
		DetailHash:  o.GetDetailHash(),
		OccurredAt:  tsFromPB(o.GetOccurredAt()),
		OWASPLLM:    stringsFromPB(o.GetOwaspLlm()),
		OWASPASI:    stringsFromPB(o.GetOwaspAsi()),
		ATLAS:       stringsFromPB(o.GetAtlas()),
	}
}

func metricToPB(o model.MetricSample) *pb.MetricSample {
	return &pb.MetricSample{
		Name:        o.Name,
		Value:       o.Value,
		Unit:        o.Unit,
		SubjectKind: o.SubjectKind,
		SubjectRef:  o.SubjectRef,
		OccurredAt:  tsToPB(o.OccurredAt),
		Dimensions:  o.Dimensions,
		Labels:      o.Labels,
		Additive:    o.Additive,
	}
}

func metricFromPB(o *pb.MetricSample) model.MetricSample {
	return model.MetricSample{
		Name:        o.GetName(),
		Value:       o.GetValue(),
		Unit:        o.GetUnit(),
		SubjectKind: o.GetSubjectKind(),
		SubjectRef:  o.GetSubjectRef(),
		OccurredAt:  tsFromPB(o.GetOccurredAt()),
		// Empty -> nil, like labelsFromPB: a dimension/label-less sample round-trips to
		// the model's "none" representation (nil), not an allocated empty map.
		Dimensions: labelsFromPB(o.GetDimensions()),
		Labels:     labelsFromPB(o.GetLabels()),
		Additive:   o.GetAdditive(),
	}
}

// observationToPB encodes a sealed Observation onto the wire oneof. Because the
// Observation interface is sealed in sdk/model, the default case is only
// reachable if a new observation type was added there without extending this
// switch — which the compiler does not catch, so we return an error rather than
// silently dropping the fact.
func observationToPB(obs model.Observation) (*pb.Observation, error) {
	// Accept value or pointer DTOs: the DTOs use value receivers, so a pointer
	// also satisfies Observation, and a connector may legitimately Emit either.
	switch o := obs.(type) {
	case model.EdgeObservation:
		return &pb.Observation{Payload: &pb.Observation_Edge{Edge: edgeToPB(o)}}, nil
	case *model.EdgeObservation:
		return &pb.Observation{Payload: &pb.Observation_Edge{Edge: edgeToPB(*o)}}, nil
	case model.CostSample:
		return &pb.Observation{Payload: &pb.Observation_Cost{Cost: costToPB(o)}}, nil
	case *model.CostSample:
		return &pb.Observation{Payload: &pb.Observation_Cost{Cost: costToPB(*o)}}, nil
	case model.FindingReport:
		return &pb.Observation{Payload: &pb.Observation_Finding{Finding: findingToPB(o)}}, nil
	case *model.FindingReport:
		return &pb.Observation{Payload: &pb.Observation_Finding{Finding: findingToPB(*o)}}, nil
	case model.MetricSample:
		return &pb.Observation{Payload: &pb.Observation_Metric{Metric: metricToPB(o)}}, nil
	case *model.MetricSample:
		return &pb.Observation{Payload: &pb.Observation_Metric{Metric: metricToPB(*o)}}, nil
	default:
		return nil, fmt.Errorf("plugin: cannot encode observation of type %q (%T): no wire mapping", obs.ObservationType(), obs)
	}
}

// observationFromPB decodes a wire Observation back to a sealed model.Observation.
// An empty/unknown oneof is a contract error.
func observationFromPB(o *pb.Observation) (model.Observation, error) {
	switch p := o.GetPayload().(type) {
	case *pb.Observation_Edge:
		return edgeFromPB(p.Edge), nil
	case *pb.Observation_Cost:
		return costFromPB(p.Cost), nil
	case *pb.Observation_Finding:
		return findingFromPB(p.Finding), nil
	case *pb.Observation_Metric:
		return metricFromPB(p.Metric), nil
	default:
		return nil, fmt.Errorf("plugin: received observation with no recognized payload: %T", o.GetPayload())
	}
}

func notificationToPB(n sdk.Notification) *pb.Notification {
	return &pb.Notification{
		Type:     n.Type,
		Title:    n.Title,
		Body:     n.Body,
		Severity: string(n.Severity),
		Tenant:   n.Tenant,
		Fields:   n.Fields,
		Time:     tsToPB(n.Time),
		Actions:  notificationActionsToPB(n.Actions),
	}
}

func notificationFromPB(n *pb.Notification) sdk.Notification {
	return sdk.Notification{
		Type:     n.GetType(),
		Title:    n.GetTitle(),
		Body:     n.GetBody(),
		Severity: model.Severity(n.GetSeverity()),
		Tenant:   n.GetTenant(),
		// Empty -> nil, like labelsFromPB: a fields-less notification decodes to
		// the same "none" representation (nil) it was encoded from (S142).
		Fields:  labelsFromPB(n.GetFields()),
		Time:    tsFromPB(n.GetTime()),
		Actions: notificationActionsFromPB(n.GetActions()),
	}
}

// notificationActionsToPB maps the interactive actions onto the wire, preserving
// the empty->nil posture (no actions encodes to nil, never an empty slice) so the
// round-trip is identity.
func notificationActionsToPB(in []sdk.NotificationAction) []*pb.NotificationAction {
	if len(in) == 0 {
		return nil
	}
	out := make([]*pb.NotificationAction, len(in))
	for i, a := range in {
		out[i] = &pb.NotificationAction{Label: a.Label, Id: a.ID, Value: a.Value, Style: a.Style}
	}
	return out
}

// notificationActionsFromPB is the inverse, also empty->nil so an actions-less
// notification decodes to the nil it was encoded from.
func notificationActionsFromPB(in []*pb.NotificationAction) []sdk.NotificationAction {
	if len(in) == 0 {
		return nil
	}
	out := make([]sdk.NotificationAction, len(in))
	for i, a := range in {
		out[i] = sdk.NotificationAction{Label: a.GetLabel(), ID: a.GetId(), Value: a.GetValue(), Style: a.GetStyle()}
	}
	return out
}

func contentDocRefToPB(ref sdk.DocRef) *pb.ContentDocRef {
	return &pb.ContentDocRef{
		DocId:       ref.DocID,
		Title:       ref.Title,
		ContentType: ref.ContentType,
		ModifiedAt:  tsToPB(ref.ModifiedAt),
	}
}

func contentDocRefFromPB(ref *pb.ContentDocRef) sdk.DocRef {
	if ref == nil {
		return sdk.DocRef{}
	}
	return sdk.DocRef{
		DocID:       ref.GetDocId(),
		Title:       ref.GetTitle(),
		ContentType: ref.GetContentType(),
		ModifiedAt:  tsFromPB(ref.GetModifiedAt()),
	}
}

func contentDocumentToPB(doc sdk.Document) *pb.ContentDocument {
	return &pb.ContentDocument{
		Source:         string(doc.Source),
		DocId:          doc.DocID,
		Title:          doc.Title,
		Body:           doc.Body,
		ContentType:    doc.ContentType,
		Acl:            doc.ACL,
		Classification: doc.Classification,
		SpaceRef:       doc.SpaceRef,
		ModifiedAt:     tsToPB(doc.ModifiedAt),
		Attributes:     doc.Attributes,
		ExternalLabels: doc.ExternalLabels,
	}
}

func contentDocumentFromPB(doc *pb.ContentDocument) sdk.Document {
	if doc == nil {
		return sdk.Document{}
	}
	return sdk.Document{
		Source:         sdk.SourceKind(doc.GetSource()),
		DocID:          doc.GetDocId(),
		Title:          doc.GetTitle(),
		Body:           bytesFromPB(doc.GetBody()),
		ContentType:    doc.GetContentType(),
		ACL:            stringsFromPB(doc.GetAcl()),
		Classification: doc.GetClassification(),
		SpaceRef:       doc.GetSpaceRef(),
		ModifiedAt:     tsFromPB(doc.GetModifiedAt()),
		Attributes:     labelsFromPB(doc.GetAttributes()),
		ExternalLabels: stringsFromPB(doc.GetExternalLabels()),
	}
}

func contentACLResultToPB(res sdk.ACLResult) *pb.ContentACLResult {
	return &pb.ContentACLResult{
		Acl:            res.ACL,
		ExternalLabels: res.ExternalLabels,
		Classification: res.Classification,
	}
}

func contentACLResultFromPB(res *pb.ContentACLResult) sdk.ACLResult {
	if res == nil {
		return sdk.ACLResult{}
	}
	return sdk.ACLResult{
		ACL:            stringsFromPB(res.GetAcl()),
		ExternalLabels: stringsFromPB(res.GetExternalLabels()),
		Classification: res.GetClassification(),
	}
}

func contentChangeToPB(change sdk.Change, page sdk.DeltaPage) *pb.ContentChange {
	return &pb.ContentChange{
		Kind:        string(change.ChangeKind),
		Ref:         contentDocRefToPB(change.DocRef),
		Cursor:      page.ResumeToken,
		NextToken:   page.NextToken,
		ResumeToken: page.ResumeToken,
		Expired:     page.Expired,
	}
}

func contentDeltaPageMetaToPB(page sdk.DeltaPage) *pb.ContentChange {
	return &pb.ContentChange{
		Cursor:      page.ResumeToken,
		NextToken:   page.NextToken,
		ResumeToken: page.ResumeToken,
		Expired:     page.Expired,
	}
}

func contentChangeFromPB(change *pb.ContentChange) (sdk.Change, bool) {
	if change == nil || change.GetKind() == "" || change.GetRef() == nil {
		return sdk.Change{}, false
	}
	return sdk.Change{
		DocRef:     contentDocRefFromPB(change.GetRef()),
		ChangeKind: sdk.ChangeKind(change.GetKind()),
	}, true
}

func applyContentDeltaMetaFromPB(page *sdk.DeltaPage, change *pb.ContentChange) {
	if change == nil {
		return
	}
	if change.GetNextToken() != "" {
		page.NextToken = change.GetNextToken()
	}
	if change.GetResumeToken() != "" {
		page.ResumeToken = change.GetResumeToken()
	} else if change.GetCursor() != "" {
		page.ResumeToken = change.GetCursor()
	}
	if change.GetExpired() {
		page.Expired = true
	}
}

func bytesFromPB(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}
