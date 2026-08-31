// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudqueue

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/brokerobs"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Origin and resource kinds emitted by this connector. They are TOPOLOGY kinds
// (which bus exists, how it fans out) — disjoint from the aws connector's
// "aws.api" control-plane namespace, so the two never collide in the graph.
const (
	originAWSAccount = "aws.account"
	originGCPProject = "gcp.project"

	resSQSQueue        = "sqs.queue"
	resSNSTopic        = "sns.topic"
	resSNSSubscription = "sns.subscription"
	resEventBridgeBus  = "eventbridge.bus"
	resPubSubTopic     = "pubsub.topic"
	resPubSubSub       = "pubsub.subscription"
)

// SubjectKind values used in health findings, one per enabled service.
const (
	subjectSQS         = "aws.sqs"
	subjectSNS         = "aws.sns"
	subjectEventBridge = "aws.eventbridge"
	subjectPubSub      = "gcp.pubsub"
)

// topologyEdge builds one minimal-data topology edge through brokerobs (which
// scrubs every ref through redact before it becomes an EdgeObservation). A pure
// existence/containment edge (account⊳queue, project⊳topic) is ModeUnknown:
// presence is not an access. Confidence is attributed — we observed the resource
// directly via its list/describe API.
func topologyEdge(originKind, originRef, resKind, resRef string, sig model.SignalSource, at time.Time) model.EdgeObservation {
	return brokerobs.Observation{
		OriginKind:   originKind,
		OriginRef:    originRef,
		ResourceKind: resKind,
		ResourceRef:  resRef,
		Mode:         model.ModeUnknown,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
	}.Edge(sig)
}

// fanoutEdge builds a fan-out edge where one bus resource reads/feeds another (an
// SNS subscription delivers a topic's events to an endpoint; a Pub/Sub
// subscription reads its topic). Mode is the caller's classification: a
// subscription that READS its topic is ModeRead. Confidence stays attributed.
func fanoutEdge(originKind, originRef, resKind, resRef string, mode model.AccessMode, sig model.SignalSource, at time.Time) model.EdgeObservation {
	return brokerobs.Observation{
		OriginKind:   originKind,
		OriginRef:    originRef,
		ResourceKind: resKind,
		ResourceRef:  resRef,
		Mode:         mode,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
	}.Edge(sig)
}

// healthFinding reports an enabled service that could not be reached/listed. The
// error detail is HASHED (never embedded), so a signed URL or token in the error
// text is not persisted (minimal-data). A gap is a signal, not silence: the
// connector emits this and continues with the next service.
func healthFinding(subjectKind, subjectRef, title string, err error, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "health",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectKind,
		SubjectRef:  subjectRef,
		Title:       title,
		DetailHash:  redact.Hash(err.Error()),
		OccurredAt:  at,
	}
}

// emit forwards an observation, returning Emit's error so callers treat it as fatal
// to the current pass (the SDK contract).
func emit(ctx context.Context, sink sdk.Sink, obs model.Observation) error {
	return sink.Emit(ctx, obs)
}
