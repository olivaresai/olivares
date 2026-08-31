// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"bytes"
	"encoding/json"

	"github.com/olivaresai/olivares/sdk/event"
)

// TypeVoiceTelemetry is the MODULE-OWNED custom event type an in-process voice
// producer publishes (sanctioned by sdk/event: a module may introduce its own Type
// with a JSON payload the publisher and consumer own). It is the deny-closed INGEST
// seam: the OpenAI Realtime SIP call observer publishes it only when that call plane
// is configured; otherwise the observe half is honestly empty. An OUT-OF-PROCESS
// plugin cannot use this Type (the ControlPlane gRPC proto carries no event RPC) —
// the producer must be in-process.
const TypeVoiceTelemetry event.Type = "voice.telemetry.observed"

// Telemetry is the ALLOW-LISTED metadata a probe may report for one voice/
// realtime turn or sample. It is the entire vocabulary the module will accept: there
// is, by construction, NO field for audio, transcript TEXT, ASR/TTS text, prompt/
// response content or PII. transcript_locator_ref is an EXTERNAL pointer (e.g. an
// object-store key) that is HASHED before storage, never the transcript itself.
type Telemetry struct {
	SessionRef           string `json:"session_ref"`
	AgentRef             string `json:"agent_ref"`
	ModelRef             string `json:"model_ref"`
	ProviderRef          string `json:"provider_ref"`
	LanguageCode         string `json:"language_code"` // BCP-47, e.g. "es-ES"
	Role                 string `json:"role"`          // "user" | "agent" — which side took the turn(s)
	TurnDelta            int64  `json:"turn_delta"`    // number of turns this event accounts for
	LatencyMS            int64  `json:"latency_ms"`    // a single latency sample
	DurationMS           int64  `json:"duration_ms"`   // cumulative session duration so far
	ClosedReason         string `json:"closed_reason"` // set on the final telemetry of a session
	TranscriptLocatorRef string `json:"transcript_locator_ref"`
	OccurredAt           string `json:"occurred_at"` // RFC3339; falls back to the module clock
}

// parseTelemetry strictly decodes a bus event's payload into Telemetry,
// REJECTING unknown keys. Re-marshaling the payload (which may arrive as a struct,
// a map or raw JSON) and decoding with DisallowUnknownFields means even a buggy or
// hostile probe that adds a "transcript_text"/"audio" key has its whole event
// dropped rather than partially stored — the allow-list is the guarantee.
func parseTelemetry(e event.Event) (Telemetry, bool) {
	if e.Type != TypeVoiceTelemetry || e.Payload == nil {
		return Telemetry{}, false
	}
	raw, err := json.Marshal(e.Payload)
	if err != nil {
		return Telemetry{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var vt Telemetry
	if dec.Decode(&vt) != nil {
		return Telemetry{}, false
	}
	// session_ref keys the row; agent_ref is the NOT-NULL governance/policy-matching
	// key. A sample lacking either cannot be governed or even inserted, so it is
	// dropped at the ingest boundary rather than partially stored.
	if vt.SessionRef == "" || vt.AgentRef == "" {
		return Telemetry{}, false
	}
	return vt, true
}
