// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/olivaresai/olivares/sdk/model"
)

// observations.go turns one governed hook call into the facts the platform already knows
// how to fold.
//
// It emits NO CostSample. A hook call has no cost, and the live view's token/cost totals
// are real figures — synthesizing a sample to get a row created would be fabricating data.
// The door that does fit is the session-origin EDGE: modules/sessions folds an edge whose
// OriginKind is "session" into the live row, advancing the current tool, resource and mode
// (live.go onEdge). That is precisely what a tool call is.

// originSession is the value the sessions module keys its live fold on. Any other value
// routes the edge to inventory instead and the session never appears.
const originSession = "session"

// signalCodexHook names what actually produced these facts. sdk/model has no constant for
// an agent hook (the nearest, SignalPolicy, means a DECLARED grant, which this is not), and
// sdk/ is not this session's territory — so the value is declared here rather than
// borrowing SignalOTEL, which would be a lie in a field an auditor reads. SignalSource is
// a free string end to end: it is stored as text, is not part of the AccessEdge natural
// key (tenant, origin_kind, origin_id, resource_id, mode) and nothing switches on it.
// Promoting it to a named SDK constant is pack SG-01-Codex-c.
const signalCodexHook = model.SignalSource("codex_hook")

// Label keys the edge carries so the live view can tell the engines apart. Labels are the
// SDK's declared attribution channel and are explicitly NOT part of any dedup key,
// which is exactly the right property for an attribution dimension.
const (
	LabelEngine  = "engine"
	LabelPosture = "posture"
)

// EngineCodex is this connector's engine key. It is lowercase because SG-00 makes the
// provider lowercase canonical, and the same string is the alias provider.
const EngineCodex = "codex"

// Posture values a Codex session can honestly claim. They exist because a governed Codex
// session and a merely observed one are DIFFERENT facts, and /sessions painting them
// identically would assert a control that in one case does not exist.
const (
	// PostureEnforced — the call reached the governed decider AND the event is one whose
	// deny actually prevents the act (PreToolUse, PermissionRequest).
	PostureEnforced = "enforced"
	// PostureObserved — the fact was seen and recorded, but a deny on this event cannot
	// prevent it. PostToolUse blocks further processing of a tool that ALREADY RAN.
	PostureObserved = "observed"
)

// PostureFor states the honest posture of one governed call.
func PostureFor(event string, dec Decision) string {
	if dec.Enforced && CanImpede(event) {
		return PostureEnforced
	}
	return PostureObserved
}

// EdgeFor builds the access edge for a tool call. It returns false for the hook events
// that are not a resource access (SessionStart, Stop, …): inventing an edge for them would
// inflate the session's tool-call count with things that are not tool calls.
//
// A DENIED call still produces an edge. Recording the attempt is the point — an operator
// needs to see what a session TRIED to do, not only what it managed to do.
func EdgeFor(req Request, dec Decision) (model.EdgeObservation, bool) {
	if dec.SessionSID == "" || req.Tool == "" || req.ResourceKind == "" {
		return model.EdgeObservation{}, false
	}
	return model.EdgeObservation{
		OriginKind:   originSession,
		OriginRef:    dec.SessionSID,
		ResourceKind: req.ResourceKind,
		ResourceRef:  req.ResourceRef,
		Mode:         model.AccessMode(req.Mode),
		Source:       signalCodexHook,
		// Attributed, not approximate: the session id came from Codex's own hook payload
		// for this exact call, not from a heuristic join.
		Confidence: model.ConfidenceAttributed,
		ToolRef:    req.Tool,
		ObservedAt: req.At,
		Labels: map[string]string{
			LabelEngine:  EngineCodex,
			LabelPosture: PostureFor(req.Event, dec),
		},
	}, true
}

// LifecycleFinding records a session-lifecycle moment as a session-scoped finding — the
// second door the sessions module folds (live.go onFinding, keyed on SubjectKind
// "session"). It is what makes the start→…→close cycle visible for a Codex session.
func LifecycleFinding(req Request, dec Decision) (model.FindingReport, bool) {
	if dec.SessionSID == "" {
		return model.FindingReport{}, false
	}
	var title string
	switch req.Event {
	case EventSessionStart:
		title = fmt.Sprintf("codex session started (%s, %s)", PostureFor(req.Event, dec), orUnknown(req.Model))
	case EventSessionEnd:
		title = "codex session ended"
	default:
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		SubjectKind: "session",
		SubjectRef:  dec.SessionSID,
		Kind:        "session.lifecycle",
		Severity:    model.SeverityInfo,
		Title:       title,
		DetailHash:  detailHash(req.Event, EngineCodex, req.ExternalSessionID, req.PermissionMode),
		OccurredAt:  req.At,
	}, true
}

// DenyFinding records a governed refusal against the session. A deny that exists only in
// the agent's transcript is not evidence; this is what puts it on the session's timeline.
func DenyFinding(req Request, dec Decision) (model.FindingReport, bool) {
	if dec.SessionSID == "" || dec.Verdict == VerdictAllow {
		return model.FindingReport{}, false
	}
	posture := PostureFor(req.Event, dec)
	// An ENFORCED deny prevented something: the session attempted an act policy forbids
	// and was stopped. An observed one is a weaker fact and is graded as such — grading
	// both the same would make the enforced ones impossible to find.
	sev := model.SeverityMedium
	if posture == PostureEnforced {
		sev = model.SeverityHigh
	}
	return model.FindingReport{
		SubjectKind: "session",
		SubjectRef:  dec.SessionSID,
		Kind:        "session.policy.deny",
		Severity:    sev,
		Title:       fmt.Sprintf("codex %s denied (%s): %s", req.Event, posture, orUnknown(req.Tool)),
		DetailHash:  detailHash(req.Event, req.Tool, req.ResourceRef, req.Mode, dec.Reason),
		OccurredAt:  req.At,
	}, true
}

// detailHash is the minimal-data channel: the SDK contract carries a hash of the detail,
// never the detail. Fields are length-prefixed so ("ab","c") and ("a","bc") cannot collide.
func detailHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s|", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
