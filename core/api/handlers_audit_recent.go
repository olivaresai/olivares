// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// auditRecentScan bounds how far back from the head one recent read looks.
const auditRecentScan = 2000

// auditRecentResponse is the newest events, newest first, and the head they were read at.
type auditRecentResponse struct {
	Items   JSONArray[AuditEventDTO] `json:"items"`
	HeadSeq int64                    `json:"head_seq"`
}

// handleAuditRecent returns the tenant's newest ledger events, newest first, leaving out
// the ledger's own audit.read events. It is the console's notification bell read, and,
// unlike the ledger reads, it does not append an audit.read: a bell that asks once a
// minute must not fill the ledger with its own looking. Searching, exporting and
// verifying the ledger stay recorded. ?limit is 1..50 (default 10); at most the last
// 2000 positions are examined.
func (s *Server) handleAuditRecent(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	tenant := mc.Tenant
	limit := int(queryInt64(r, "limit", 10))
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	out := auditRecentResponse{Items: []AuditEventDTO{}}
	err := s.st.View(r.Context(), tenant, func(sc store.Scope) error {
		head, err := auditHeadSeq(r.Context(), sc)
		if err != nil {
			return err
		}
		out.HeadSeq = head
		// The ledger walks forwards only: read windows below the head, newest window first.
		for hi := head; hi >= 1 && head-hi < auditRecentScan && len(out.Items) < limit; {
			lo := max(1, hi-auditScanPageSize+1)
			var window []model.AuditEvent
			werr := sc.Audit().Walk(r.Context(), lo, func(ev model.AuditEvent) error {
				if ev.Seq > hi {
					return errStopWalk
				}
				window = append(window, ev)
				return nil
			})
			if werr != nil && !errors.Is(werr, errStopWalk) {
				return werr
			}
			for i := len(window) - 1; i >= 0 && len(out.Items) < limit; i-- {
				if window[i].Action != "audit.read" {
					out.Items = append(out.Items, toAuditDTO(window[i]))
				}
			}
			hi = lo - 1
		}
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
