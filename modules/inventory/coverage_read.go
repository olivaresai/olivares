// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"net/http"
	"strconv"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

// collectionResultDTO is a closed projection. Scope hashes describe the versioned
// query, never global estate completeness or provider authorization.
type collectionResultDTO struct {
	RunID          string `json:"run_id"`
	SourceID       string `json:"source_id"`
	Revision       int64  `json:"source_revision"`
	Environment    string `json:"environment_ref"`
	Order          int64  `json:"run_order"`
	Contract       string `json:"scope_contract,omitempty"`
	Family         string `json:"family,omitempty"`
	Requested      string `json:"requested_scope,omitempty"`
	Fulfilled      string `json:"fulfilled_scope,omitempty"`
	Coverage       string `json:"coverage"`
	Reason         string `json:"reason"`
	Projection     string `json:"projection"`
	Admitted       int64  `json:"admitted_count"`
	Committed      int64  `json:"committed_count"`
	Expected       int64  `json:"expected_count"`
	HostStart      string `json:"host_started_at"`
	HostFinish     string `json:"host_finished_at,omitempty"`
	ProducerStart  string `json:"producer_started_at,omitempty"`
	ProducerFinish string `json:"producer_finished_at,omitempty"`
	Qualified      string `json:"qualified_at,omitempty"`
	Rejection      string `json:"rejection_reason,omitempty"`
}
type collectionDTO struct {
	Current       collectionResultDTO   `json:"current"`
	LastQualified *collectionSuccessDTO `json:"last_qualified_success,omitempty"`
}

func collectionResult(row model.Record) collectionResultDTO {
	return collectionResultDTO{
		RunID:          row.String(cRun),
		SourceID:       row.String(colSourceID),
		Revision:       row.Int(cRevision),
		Environment:    row.String(cEnvironment),
		Order:          row.Int(cOrder),
		Contract:       row.String(cContract),
		Family:         row.String(cFamily),
		Requested:      row.String(cRequested),
		Fulfilled:      row.String(cFulfilled),
		Coverage:       row.String(cState),
		Reason:         row.String(cReason),
		Projection:     row.String(cProjection),
		Admitted:       row.Int(cAdmitted),
		Committed:      row.Int(cCommitted),
		Expected:       row.Int(cExpected),
		HostStart:      row.String(cHostStart),
		HostFinish:     row.String(cHostFinish),
		ProducerStart:  row.String(cProducerStart),
		ProducerFinish: row.String(cProducerFinish),
		Qualified:      row.String(cQualified),
		Rejection:      row.String(cRejected),
	}
}

// handleCollections returns the collection coverage of one opened source registration,
// selected by ?source_id, ?source_revision and ?environment_ref: the current run's
// result, and the last qualified run when its scope has one. A registration with no
// report yet reads as unknown coverage with its projection pending.
func (m *Module) handleCollections(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := listResponse[collectionDTO]{Items: []collectionDTO{}}
	source := r.URL.Query().Get("source_id")
	environment := r.URL.Query().Get("environment_ref")
	revision, parseErr := strconv.ParseInt(r.URL.Query().Get("source_revision"), 10, 64)
	// The caller must select its CURRENT opened registration. This endpoint does
	// not resolve a mutable source roster or relabel historical evidence as current.
	if source == "" || len(source) > 128 || environment == "" || len(environment) > 128 || parseErr != nil || revision < 1 {
		writeJSON(w, http.StatusBadRequest, errorBody("source_id, source_revision and environment_ref are required"))
		return
	}
	key := coverageSourceKey(event.InventoryRun{Registration: event.SourceRegistration{SourceID: source, SourceRevision: revision, EnvironmentRef: environment}})
	q := model.Query{Filters: []model.Filter{eq(cSourceKey, key)}, Limit: 1}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(collectionSourceKind)
		if err != nil {
			return err
		}
		heads, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, head := range heads {
			row, err := collectionFind(r.Context(), sc, collectionRunKind, eq(cRun, head.String(cRun)))
			if err != nil {
				return err
			}
			if row == nil {
				return ErrCollectionRejected
			}
			item := collectionDTO{Current: collectionResult(row)}
			if key := row.String(cScopeKey); key != "" {
				scope, err := collectionFind(r.Context(), sc, collectionScopeKind, eq(cScopeKey, key))
				if err != nil {
					return err
				}
				if scope != nil {
					prior, err := collectionFind(r.Context(), sc, collectionRunKind, eq(cRun, scope.String(cLastQualified)))
					if err != nil {
						return err
					}
					if prior != nil {
						result := collectionSuccess(prior)
						item.LastQualified = &result
					}
				}
			}
			out.Items = append(out.Items, item)
		}
		if len(heads) == 0 {
			out.Items = append(out.Items, collectionDTO{Current: collectionResultDTO{SourceID: source, Revision: revision, Environment: environment, Coverage: "unknown", Reason: "missing_report", Projection: "pending"}})
		}
		out.Cursor = page.Cursor
		out.HasMore = page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// collectionSuccessDTO contains only evidence frozen at qualification. A later
// rejected replay is visible on Current and cannot rewrite this historic receipt.
type collectionSuccessDTO struct {
	RunID          string `json:"run_id"`
	SourceID       string `json:"source_id"`
	Revision       int64  `json:"source_revision"`
	Environment    string `json:"environment_ref"`
	Contract       string `json:"scope_contract"`
	Family         string `json:"family"`
	Requested      string `json:"requested_scope"`
	Fulfilled      string `json:"fulfilled_scope"`
	Expected       int64  `json:"expected_count"`
	Qualified      string `json:"qualified_at"`
	HostStart      string `json:"host_started_at"`
	HostFinish     string `json:"host_finished_at"`
	ProducerStart  string `json:"producer_started_at"`
	ProducerFinish string `json:"producer_finished_at"`
}

func collectionSuccess(row model.Record) collectionSuccessDTO {
	return collectionSuccessDTO{
		RunID:          row.String(cRun),
		SourceID:       row.String(colSourceID),
		Revision:       row.Int(cRevision),
		Environment:    row.String(cEnvironment),
		Contract:       row.String(cContract),
		Family:         row.String(cFamily),
		Requested:      row.String(cRequested),
		Fulfilled:      row.String(cFulfilled),
		Expected:       row.Int(cExpected),
		Qualified:      row.String(cQualified),
		HostStart:      row.String(cHostStart),
		HostFinish:     row.String(cHostFinish),
		ProducerStart:  row.String(cProducerStart),
		ProducerFinish: row.String(cProducerFinish),
	}
}
