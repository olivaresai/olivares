// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package siemforward

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/eventing"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// TestAuditSIEMFormatsDropRawPrivacyMeta attacks the ledger-to-SIEM bridge with
// raw prompt, completion and retrieved-content values in AuditEvent.Meta. Meta is
// deliberately an on-box chain input; every external dialect must omit it while
// preserving the safe payload fingerprint used for correlation.
func TestAuditSIEMFormatsDropRawPrivacyMeta(t *testing.T) {
	const (
		promptCanary    = "alice.s373@example.com PROMPT-SIEM-CANARY"
		responseCanary  = "SSN 078-05-1120 RESPONSE-SIEM-CANARY"
		retrievalCanary = "PRIVATE-RETRIEVED-SIEM-CANARY"
	)
	payloadHash := sha256.Sum256([]byte(promptCanary + responseCanary + retrievalCanary))
	digest := hex.EncodeToString(payloadHash[:])
	ev := model.AuditEvent{
		ID: "led-privacy", TenantID: "ten-privacy", Seq: 19, OccurredAt: model.NewTimestamp(when),
		Actor: "user:u1", ActorKind: "user", Action: "inference.proxy.recorded",
		TargetKind: "inference.proxy.call", TargetID: "call-privacy",
		MetaCommitment: fixtureCommitment,
		PayloadHash:    payloadHash[:],
		PrevHash:       widen(32, 0x01),
		Hash:           widen(32, 0x02),
		Sig:            widen(64, 0x03),
		Meta: map[string]any{
			"prompt": promptCanary, "completion": responseCanary, "retrieved_content": retrievalCanary,
		},
	}
	wire, err := json.Marshal(auditWireFrom(ev))
	if err != nil {
		t.Fatal(err)
	}
	assertSIEMPrivacy(t, "audit wire", string(wire), digest, promptCanary, responseCanary, retrievalCanary)

	renderer := NewRenderer()
	// Every DECLARABLE dialect, derived from the catalog's eventing subset so a
	// new token cannot ship without facing this attack. (otlp_log_record is
	// pull-only; its privacy property is the pull encoder's, covered in
	// core/audit.)
	formats := make([]string, 0, len(siemwire.EventingSinkFormats().Tokens()))
	for _, f := range siemwire.EventingSinkFormats().Tokens() {
		formats = append(formats, string(f))
	}
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			req, err := renderer.Render(eventing.SinkEvent{
				ID: "led-privacy", Type: "audit.recorded", Tenant: "ten-privacy",
				Source: "olivares.audit", Time: when, Seq: 19, Payload: wire,
			}, eventing.SinkProfile{
				Kind: "https", Format: format, Endpoint: "https://collector.invalid/events",
			})
			if err != nil {
				t.Fatal(err)
			}
			assertSIEMPrivacy(t, format, string(req.Body), digest, promptCanary, responseCanary, retrievalCanary)
		})
	}
}

func assertSIEMPrivacy(t *testing.T, sink, body, wantHash string, canaries ...string) {
	t.Helper()
	if !strings.Contains(body, wantHash) {
		t.Fatalf("%s dropped safe payload fingerprint %s: %s", sink, wantHash, body)
	}
	for _, canary := range canaries {
		if strings.Contains(body, canary) {
			t.Fatalf("%s leaked raw privacy canary %q: %s", sink, canary, body)
		}
	}
}
