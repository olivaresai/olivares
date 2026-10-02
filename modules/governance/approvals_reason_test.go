// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/store"
)

func TestApprovalReason64KiBRoundTripPreservesDecisionNoteLimit(t *testing.T) {
	for _, backend := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(backend), func(t *testing.T) {
			opts := harnessOpts{}
			if backend == store.EnginePostgres {
				pg := enginetest.IsolatedPostgres(t)
				opts = harnessOpts{engine: backend, dsn: pg.App, adminDSN: pg.Admin}
			}
			h := newHarnessWith(t, opts)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "approval-reason")
			_, requester := h.roleUser(admin, tenant, "requester@x.io", "editor")
			reason := "printf " + strings.Repeat("x", 65536-len("printf \nlast-argument")) + "\nlast-argument"
			body := map[string]any{"subject_kind": "session_run", "subject_ref": "run-long-command", "action": "sessions.provider.approval", "reason": reason}
			created := h.createApproval(requester, tenant, body)
			if created.code != http.StatusCreated || created.body["reason"] != reason {
				t.Fatalf("64 KiB reason create = %d; reason preserved = %v", created.code, created.body["reason"] == reason)
			}
			id := created.body["id"].(string)
			read := h.do("GET", govPath+"/approvals/"+id, admin, nil, tenantHdr(tenant))
			if read.code != http.StatusOK || read.body["reason"] != reason {
				t.Fatalf("stored reason read = %d; reason preserved = %v", read.code, read.body["reason"] == reason)
			}
			body["reason"] = reason + "x"
			if over := h.createApproval(requester, tenant, body); over.code != http.StatusBadRequest {
				t.Fatalf("reason over 64 KiB = %d, want 400", over.code)
			}
			decide := func(note string) resp {
				return h.do("POST", govPath+"/approvals/"+id+"/decisions", admin, map[string]any{"decision": "approve", "note": note}, tenantHdr(tenant))
			}
			if over := decide(strings.Repeat("n", 4097)); over.code != http.StatusBadRequest {
				t.Fatalf("decision note over 4096 bytes = %d, want 400", over.code)
			}
			if at := decide(strings.Repeat("n", 4096)); at.code != http.StatusOK || at.body["status"] != "approved" {
				t.Fatalf("4096-byte decision note = %d %s", at.code, at.raw)
			}
		})
	}
}
