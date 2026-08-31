// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package liveingest

import (
	"context"

	"github.com/olivaresai/olivares/modules/voice"
	"github.com/olivaresai/olivares/sdk/event"
)

// PublishVoiceTelemetry is the in-process voice probe: it publishes one allow-listed
// voice.Telemetry sample as a voice.telemetry.observed event for module XVI to fold
// into a session's metadata (modules/voice/sessions.go onTelemetry). It is the
// producer that fills the voice observe half — but ONLY from real telemetry. It
// fabricates nothing: with no realtime voice backend in this build, nothing calls
// this method, so the observe half stays honestly empty and visible,
// not falsely full. A future voice realtime dispatcher that produces turn
// metadata routes it here.
//
// The payload is a typed voice.Telemetry, so by construction it can carry no field
// outside the allow-list — never audio, transcript text or PII (the transcript
// locator is hashed by the consumer, never stored as text). The consumer
// (voice.parseTelemetry) re-enforces the allow-list with DisallowUnknownFields and
// drops a sample lacking session_ref/agent_ref; this method declines to emit such a
// droppable sample so a caller sees the producer is honest about it.
func (m *Module) PublishVoiceTelemetry(ctx context.Context, tenant string, t voice.Telemetry) error {
	if m.host == nil {
		return nil
	}
	if t.SessionRef == "" || t.AgentRef == "" {
		// voice.parseTelemetry would drop this whole sample (session_ref keys the row,
		// agent_ref is the NOT-NULL governance key). Do not publish a droppable event.
		if m.log != nil {
			m.log.Debug("liveingest: declining to publish voice telemetry without session_ref/agent_ref (the allow-list consumer would drop it)")
		}
		return nil
	}
	return m.host.Publish(ctx, event.Event{
		Type:    voice.TypeVoiceTelemetry,
		Tenant:  tenant,
		Source:  "module:" + Namespace,
		Payload: t,
	})
}
