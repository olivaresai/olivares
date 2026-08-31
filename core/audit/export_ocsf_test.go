// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
)

// TestOCSFLedgerExportCarriesIntegrity confirms the ledger exports as a valid OCSF
// v1.8.0 API Activity (6003) event AND that the tamper-evident integrity fields
// ride under the OCSF `unmapped` container unaltered (OBS-02/08): a SOC that
// ACCEPTS OCSF 1.8.0 can ingest and re-verify the chain. Amazon Security Lake is
// not that SOC — its custom sources cap at OCSF 1.3 in Parquet, so nothing this
// test pins reaches it as-is. A declared gap, not an oversight.
func TestOCSFLedgerExportCarriesIntegrity(t *testing.T) {
	if !audit.ValidFormat(audit.FormatOCSF) {
		t.Fatal("OCSF must be a valid ledger export format")
	}
	ev := signedEvent()
	out, err := audit.FormatEvent(ev, audit.FormatOCSF)
	if err != nil {
		t.Fatalf("OCSF format: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("OCSF not valid JSON: %v\n%s", err, out)
	}
	if doc["class_uid"].(float64) != 6003 || doc["category_uid"].(float64) != 6 {
		t.Errorf("class/category wrong: %v / %v", doc["class_uid"], doc["category_uid"])
	}
	// action "access_edge.upsert" -> Update (activity_id 3) -> type_uid 600303.
	if doc["activity_id"].(float64) != 3 || doc["type_uid"].(float64) != 600303 {
		t.Errorf("activity/type_uid wrong: %v / %v", doc["activity_id"], doc["type_uid"])
	}
	meta := doc["metadata"].(map[string]any)
	if meta["version"] != "1.8.0" {
		t.Errorf("metadata.version = %v, want 1.8.0", meta["version"])
	}
	um := doc["unmapped"].(map[string]any)
	if um["ai.olivares.audit.hash"] != hex.EncodeToString(ev.Hash) {
		t.Errorf("hash altered in OCSF export: %v", um["ai.olivares.audit.hash"])
	}
	if um["ai.olivares.audit.sig"] != base64.StdEncoding.EncodeToString(ev.Sig) {
		t.Errorf("sig altered: %v", um["ai.olivares.audit.sig"])
	}
	if um["ai.olivares.audit.seq"].(float64) != 42 {
		t.Errorf("seq = %v, want 42", um["ai.olivares.audit.seq"])
	}
	// the canonical hashed occurred_at text rides under unmapped (the OCSF
	// `time` field is the class's own epoch view), and the authoritative tenant
	// uses the one product-wide spelling.
	if um["ai.olivares.audit.occurred_at"] != "2023-11-14T22:13:20.000000000Z" {
		t.Errorf("occurred_at = %v, want the canonical layout text", um["ai.olivares.audit.occurred_at"])
	}
	if um["ai.olivares.tenant.id"] != "22222222-2222-7222-8222-222222222222" {
		t.Errorf("tenant = %v, want ai.olivares.tenant.id with the event's tenant", um["ai.olivares.tenant.id"])
	}
	// The OWASP AOS agent marker is present (added by the shared encoder).
	if um["actor.type_id"].(float64) != 99 {
		t.Errorf("AOS actor.type_id marker missing: %v", um)
	}
}
