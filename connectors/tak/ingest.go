// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package tak

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// ingest.go lifts a parsed CoT event into the engine's governed vocabulary.
//
// Minimal-data doctrine (docs/SECURITY-HARDENING.md) applied to CoT, which is a position-reporting
// protocol and therefore the most PII-dense signal this product ingests:
//
//   - The lat/lon/hae of the <point> NEVER leave this connector. A coordinate is
//     the location of a person. We emit that an event was received, from which
//     emitter, of which CoT type — not where anybody is.
//   - The opaque <detail> span never leaves this connector; only its size and digest.
//   - The emitter's uid is hashed by default (cot_uid_mode=hash). A uid is "an
//     opaque string… to ensure global uniqueness" [GUIDE] — opaque to the schema,
//     but in practice a persistent device identity. An operator who needs the raw
//     uid on the access map opts in explicitly.
//
// What we emit is the ACCESS EDGE: "this emitter published into this CoT feed".
// That is the governable fact — it is what a source-scoping binding allows
// or forbids, and what the permitted-vs-observed diff is computed over.

// Resource kinds and finding kinds this connector emits.
const (
	resourceKindCoTFeed = "tak.cot.feed"
	subjectKindFeed     = "cot_feed"
	subjectKindServer   = "tak_server"

	findingCoTUnboundedError = "cot_unbounded_error"
	findingCoTDropTrack      = "cot_drop_track"
	findingCoTRejected       = "cot_event_rejected"
	findingCoTRateLimited    = "cot_rate_limited"
)

// uidHashPrefix domain-separates the emitter-uid hash from any other SHA-256 this
// product computes over a bare string, so a hashed uid can never be confused with,
// or rainbow-tabled against, a digest of the same bytes taken elsewhere.
const uidHashPrefix = "olivares.tak.cot-uid.v1\n"

// originRef renders a CoT emitter uid for the access map, honoring cot_uid_mode.
// The hashed form is stable (the same uid always maps to the same ref) so the
// access map still shows one node per emitter, without naming the bearer.
func (s *Source) originRef(uid string) string {
	if s.cfg.uidMode == uidModeRaw {
		return uid
	}
	sum := sha256.Sum256([]byte(uidHashPrefix + uid))
	return "cot-uid:" + hex.EncodeToString(sum[:16])
}

// confidenceFor grades the trust in a CoT-derived attribution.
//
// Base CoT carries no authentication: any host that can reach the listener may
// assert any uid. TAK Server's own transport security is TLS between the client
// and the SERVER (port 8089), which says nothing about an event this connector
// receives on its own plain UDP/TCP listener. Therefore every edge from a base
// CoT listener is APPROXIMATE. We do not have a code path that returns
// ConfidenceAttributed, and we will not add one until the listener itself
// terminates mTLS and binds the uid to the peer certificate.
func confidenceFor(string) model.Confidence { return model.ConfidenceApproximate }

// observe converts one accepted CoT event into the observations it warrants.
// transport is "udp" or "tcp", recorded as the tool reference so an operator can
// see which bearer an edge arrived on. at is the RECEIPT time on this connector's
// clock.
func (s *Source) observe(ev Event, transport string, at time.Time) []model.Observation {
	origin := s.originRef(ev.UID)
	out := make([]model.Observation, 0, 3)

	// The governed fact: an emitter published situational-awareness data into the
	// feed. It is a WRITE: the emitter contributes state, it does not read it.
	//
	// ObservedAt is the RECEIPT time, never the event's own `time` attribute.
	// Two reasons, and both matter. The SDK defines ObservedAt as "when the access
	// happened, in the connector's clock", and it is "the natural-key timestamp
	// consumers use to de-duplicate re-emitted edges". Base CoT is unauthenticated
	// — any host that can reach the listener asserts any uid and any timestamp — so
	// sourcing it from ev.Time would hand an unauthenticated peer the dedup and
	// ordering key of its own access edges (a far-future `time` pins the edge at the
	// head of every window; a replayed one collides with a past edge).
	//
	// The emitter's asserted `time`/`start`/`stale` are not lost: they are what
	// IsDropTrack() reads, and drop-track is surfaced as its own finding.
	out = append(out, model.EdgeObservation{
		OriginKind:   "identity",
		OriginRef:    origin,
		ResourceKind: resourceKindCoTFeed,
		ResourceRef:  s.cfg.feedRef,
		Mode:         model.ModeWrite,
		Source:       model.SignalCoT,
		Confidence:   confidenceFor(transport),
		ToolRef:      transport,
		ObservedAt:   at,
		Labels:       s.edgeLabels(ev),
	})

	// A drop-track is an explicit cancellation of previously published data
	// [GUIDE]. In a governance ledger that is a state change worth recording: it is
	// how a track disappears, and an operator investigating a vanished track needs
	// to distinguish "canceled by the emitter" from "we stopped receiving".
	if ev.IsDropTrack() {
		out = append(out, s.finding(
			findingCoTDropTrack, model.SeverityInfo, s.cfg.feedRef,
			"CoT emitter canceled a track (stale precedes start)",
			ev, transport, at,
		))
	}

	// An emitter that will not bound its own error is reporting a position it
	// cannot vouch for. CoT makes that explicit precisely so consumers can see it
	// [GUIDE]; surfacing it is the point of the protocol, not a nuisance.
	if ev.Point.CEUnbounded() || ev.Point.LEUnbounded() {
		out = append(out, s.finding(
			findingCoTUnboundedError, model.SeverityLow, s.cfg.feedRef,
			"CoT event declares an unbounded position error",
			ev, transport, at,
		))
	}
	return out
}

// edgeLabels carries the CoT type atoms as attribution dimensions. They are
// classification metadata (what KIND of thing reported), never PII: the type
// "a-h-G-E-V-A-T-t" says a hostile ground tank was reported, not who saw it.
func (s *Source) edgeLabels(ev Event) map[string]string {
	labels := map[string]string{"cot_type": ev.Type}
	if aff := ev.Affiliation(); aff != "" {
		labels["cot_affiliation"] = aff
	}
	return labels
}

// finding builds a minimal-data FindingReport about a CoT event. The DetailHash
// preimage deliberately excludes the coordinate: it binds the finding to the
// emitter, the type and the event's own detail digest, so two identical findings
// de-duplicate, and no position is recoverable from the ledger.
func (s *Source) finding(kind string, sev model.Severity, ref, title string, ev Event, transport string, at time.Time) model.FindingReport {
	preimage := kind + "|" + s.originRef(ev.UID) + "|" + ev.Type + "|" + transport + "|" + ev.DetailDigest
	return model.FindingReport{
		Kind:        kind,
		Severity:    sev,
		SubjectKind: subjectKindFeed,
		SubjectRef:  ref,
		Title:       title,
		DetailHash:  hashString(preimage),
		OccurredAt:  at,
	}
}

// rejectionFinding reports that the listener refused traffic. It names the reason
// class and the transport, never the offending bytes: a malformed event from a
// hostile peer is exactly the input we must not echo into the ledger.
func (s *Source) rejectionFinding(reason, transport string, count int, at time.Time) model.FindingReport {
	kind := findingCoTRejected
	sev := model.SeverityLow
	if reason == reasonRateLimited {
		kind = findingCoTRateLimited
		sev = model.SeverityMedium
	}
	return model.FindingReport{
		Kind:        kind,
		Severity:    sev,
		SubjectKind: subjectKindFeed,
		SubjectRef:  s.cfg.feedRef,
		Title:       "CoT listener refused " + strconv.Itoa(count) + " event(s): " + reason,
		DetailHash:  hashString(kind + "|" + s.cfg.feedRef + "|" + transport + "|" + reason),
		OccurredAt:  at,
	}
}

// emitEvent is the listener callback: parse-accepted event → observations → sink.
func (s *Source) emitEvent(ctx context.Context, sink sdk.Sink, ev Event, transport string) error {
	at := s.clock().UTC()
	for _, obs := range s.observe(ev, transport, at) {
		if err := sink.Emit(ctx, obs); err != nil {
			return fmt.Errorf("tak: emit CoT observation: %w", err)
		}
	}
	return nil
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
