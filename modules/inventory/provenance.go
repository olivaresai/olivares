// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// ErrObservationReceiptConflict means the conflicting projection was persisted,
// but did not replace the original receipt, its members, or the legacy catalog.
var ErrObservationReceiptConflict = errors.New("inventory: event ID has conflicting projected facts")

// nativeReference names only context supplied by the observation. Empty namespace
// or parent remains unknown; an editable source label is never a namespace.
type nativeReference struct {
	Namespace string `json:"namespace"`
	Parent    string `json:"parent"`
	Ref       string `json:"ref"`
}

type projectedEdge struct {
	OriginKind, OriginRef, ResourceKind, ResourceRef, ToolRef string
	Mode                                                      sdkmodel.AccessMode
	Signal                                                    sdkmodel.SignalSource
	Confidence                                                sdkmodel.Confidence
	OccurredAt                                                string
}

type projectedCost struct {
	ProviderRef, ModelRef, Gateway, OccurredAt string
}

// inventoryFactsV1 is deliberately a projection: no labels, money, prompts or
// connector config. A change only in an omitted field is not a C1 fact conflict.
// Changing this shape/meaning requires a new version, not rehashing old receipts.
type inventoryFactsV1 struct {
	Version            int                       `json:"version"`
	Type               event.Type                `json:"type"`
	SourceLabel        string                    `json:"source_label"`
	RegistrationState  string                    `json:"registration_state"`
	Registration       *event.SourceRegistration `json:"registration"`
	EnvelopeOccurredAt string                    `json:"envelope_occurred_at"`
	Edge               *projectedEdge            `json:"edge,omitempty"`
	Cost               *projectedCost            `json:"cost,omitempty"`
}

type observationMember struct {
	Kind                    string          `json:"kind"`
	EntityID                model.ID        `json:"catalog_entity_id"`
	Native                  nativeReference `json:"native"`
	Name, Ref, Signal, Host string
	OccurredAt              string `json:"occurred_at"`
}

type provenanceProjection struct {
	members []observationMember
}

func instant(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return model.NewTimestamp(t).String()
}

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func newInventoryFacts(e event.Event, edge *sdkmodel.EdgeObservation, cost *sdkmodel.CostSample) inventoryFactsV1 {
	f := inventoryFactsV1{Version: 1, Type: e.Type, SourceLabel: e.Source,
		Registration: e.SourceRegistration.Clone(), RegistrationState: "unattributed",
		EnvelopeOccurredAt: instant(e.Time)}
	if f.Registration != nil {
		f.RegistrationState = "invalid"
		if f.Registration.Valid() {
			f.RegistrationState = "registered_snapshot"
		}
	}
	if edge != nil {
		f.Edge = &projectedEdge{OriginKind: edge.OriginKind, OriginRef: edge.OriginRef,
			ResourceKind: edge.ResourceKind, ResourceRef: edge.ResourceRef, ToolRef: edge.ToolRef,
			Mode: edge.Mode, Signal: edge.Source, Confidence: edge.Confidence, OccurredAt: instant(edge.ObservedAt)}
	}
	if cost != nil {
		f.Cost = &projectedCost{cost.ProviderRef, cost.ModelRef, string(cost.Gateway), instant(cost.OccurredAt)}
	}
	return f
}

// persistObservation commits one receipt and ALL its members with the existing
// materialization. No mutable roster lookup or additional authority is involved.
// Registered snapshots rely on the existing configured-host/authorized-bridge
// trust boundary, not on universal attestation of arbitrary module publishers.
func (m *Module) persistObservation(ctx context.Context, e event.Event, facts inventoryFactsV1,
	materialize func(store.Scope, time.Time, *provenanceProjection) error) error {
	tenant, ok := tenantOf(e.Tenant)
	if !ok {
		return nil
	}
	encoded, err := json.Marshal(facts)
	if err != nil {
		return err
	}
	factsHash := digest(encoded)
	// Missing event IDs receive a storage identity, never a claimed replay identity.
	keyInput := []string{"event", e.ID}
	if e.ID == "" {
		keyInput = []string{"unidentified", model.NewID().String()}
	}
	keyBytes, _ := json.Marshal(keyInput)
	key := digest(keyBytes)
	at := m.clock.Now().Time()
	conflict := false
	err = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(observationReceiptKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colReceiptKey, key)}, Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) != 0 {
			receipt := rows[0]
			if receipt.String(colFactsHash) != factsHash || receipt.String(colEventID) != e.ID {
				conflict = true
				return persistReceiptConflict(ctx, sc, receipt, factsHash, string(encoded), at)
			}
			if err := m.replayCatalog(ctx, sc, receipt, at); err != nil {
				return err
			}
			receipt[colDeliveries] = receipt.Int(colDeliveries) + 1
			receipt[colLastSeen] = instant(at)
			_, err = repo.Update(ctx, receipt)
			return err
		}
		// Create first to reserve the event ID. A concurrent unique conflict is an
		// error for the WHOLE transaction; never swallow it as a complete receipt.
		receipt, err := repo.Create(ctx, model.Record{colReceiptKey: key, colEventID: e.ID,
			colFactsHash: factsHash, colFacts: string(encoded), colFirstSeen: instant(at),
			colLastSeen: instant(at), colDeliveries: int64(1), colMemberCount: int64(0)})
		if err != nil {
			return err
		}
		projection := &provenanceProjection{}
		if err := materialize(sc, at, projection); err != nil {
			return err
		}
		members, err := sc.Ext(observationMemberKind)
		if err != nil {
			return err
		}
		for i, member := range projection.members {
			body, err := json.Marshal(member)
			if err != nil {
				return err
			}
			identity, sourceID := "", ""
			if facts.RegistrationState == "registered_snapshot" && member.Native.Ref != "" {
				sourceID = facts.Registration.SourceID
				key, err := json.Marshal(struct {
					Version        int
					Tenant         model.TenantID
					SourceID, Kind string
					Native         nativeReference
				}{1, tenant, sourceID, member.Kind, member.Native})
				if err != nil {
					return err
				}
				identity = digest(key)
			}
			if _, err := members.Create(ctx, model.Record{colReceiptID: receipt.String(model.ColID),
				colMemberOrdinal: int64(i), colObservationKey: identity, colSourceID: sourceID,
				colEntityKind: member.Kind, colEntityID: member.EntityID.String(), colFacts: string(body)}); err != nil {
				return err
			}
		}
		receipt[colMemberCount] = int64(len(projection.members))
		_, err = repo.Update(ctx, receipt)
		return err
	})
	if err != nil {
		return err
	}
	if conflict {
		return ErrObservationReceiptConflict
	}
	return nil
}

func persistReceiptConflict(ctx context.Context, sc store.Scope, receipt model.Record, hash, facts string, at time.Time) error {
	repo, err := sc.Ext(observationConflictKind)
	if err != nil {
		return err
	}
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{
		eq(colReceiptID, receipt.String(model.ColID)), eq(colFactsHash, hash)}, Limit: 1})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		_, err = repo.Create(ctx, model.Record{colReceiptID: receipt.String(model.ColID), colFactsHash: hash,
			colFacts: facts, colFirstSeen: instant(at), colLastSeen: instant(at), colDeliveries: int64(1)})
		return err
	}
	rec := rows[0]
	rec[colDeliveries] = rec.Int(colDeliveries) + 1
	rec[colLastSeen] = instant(at)
	_, err = repo.Update(ctx, rec)
	return err
}

// Replay refreshes the legacy delivery catalog from committed members. It cannot
// re-resolve core aliases or mutate the original snapshot. V1 materializes at
// most four members; an incomplete/corrupt projection is an error, never success.
func (m *Module) replayCatalog(ctx context.Context, sc store.Scope, receipt model.Record, at time.Time) error {
	repo, err := sc.Ext(observationMemberKind)
	if err != nil {
		return err
	}
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colReceiptID, receipt.String(model.ColID))}, Limit: 16})
	if err != nil {
		return err
	}
	if len(rows) != int(receipt.Int(colMemberCount)) || len(rows) > 4 {
		return errors.New("inventory: incomplete receipt members")
	}
	for _, row := range rows {
		var member observationMember
		if err := json.Unmarshal([]byte(row.String(colFacts)), &member); err != nil {
			return err
		}
		var occurred time.Time
		if member.OccurredAt != "" {
			occurred, err = time.Parse(time.RFC3339Nano, member.OccurredAt)
			if err != nil {
				return err
			}
		}
		if err := m.upsertCatalogEntry(ctx, sc, member.Kind, member.EntityID, member.Name, member.Ref,
			member.Signal, member.Host, at, occurred); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) catalogMember(ctx context.Context, sc store.Scope, member observationMember, at, occurred time.Time, p *provenanceProjection) error {
	if err := m.upsertCatalogEntry(ctx, sc, member.Kind, member.EntityID, member.Name, member.Ref,
		member.Signal, member.Host, at, occurred); err != nil {
		return err
	}
	member.OccurredAt = instant(occurred)
	p.members = append(p.members, member)
	if len(p.members) > 4 {
		return fmt.Errorf("inventory: projection exceeds v1 member bound")
	}
	return nil
}

func resourceNative(kind, ref string, edge sdkmodel.EdgeObservation) nativeReference {
	n := nativeReference{Namespace: edge.ResourceKind, Ref: ref}
	switch kind {
	case kindMCPServer:
		n.Namespace = rkMCPServer
	case kindTool:
		switch edge.ResourceKind {
		case rkMCPTool:
			n.Parent, n.Ref = splitServerTool(edge.ResourceRef)
			if n.Ref == "" {
				n.Ref = edge.ToolRef
			}
		case rkCMAAgentTool:
			if edge.OriginKind == kindAgent {
				n.Parent = edge.OriginRef
			}
		case rkClaudeTool:
		default:
			n.Namespace = "" // payload names an operation, but supplies no tool namespace
		}
	case kindSkill:
		if edge.ResourceKind == rkMCPPrompt {
			n.Parent, n.Ref = splitServerTool(edge.ResourceRef)
		}
	case kindResource:
		if (edge.ResourceKind == rkMCPResource || edge.ResourceKind == rkMCPResourceTemplate) && edge.OriginKind == kindMCPServer {
			n.Parent = edge.OriginRef
		}
	}
	return n
}
