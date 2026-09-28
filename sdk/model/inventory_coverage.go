// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// InventoryCoverageCapability negotiates collection controls on the plugin wire.
const InventoryCoverageCapability = "inventory-coverage-v1"

// AzureInventoryContract identifies the exact ordered Resource Graph query.
const AzureInventoryContract = "azure-resource-graph/2022-10-01/id-v1"

// MaxInventoryMembers bounds durable admissions in a single Gather.
const MaxInventoryMembers int64 = 100000

// InventoryScope is a versioned requested scope, never source or tenant authority.
// Selectors are explicit subscription IDs. Auto-discovery cannot prove this scope.
type InventoryScope struct {
	Contract  string   `json:"contract"`
	Family    string   `json:"family"`
	Selectors []string `json:"selectors"`
}

// Valid checks the first implemented contract and bounded canonical selectors.
func (s InventoryScope) Valid() bool {
	if s.Contract != AzureInventoryContract || s.Family != "azure.resource" || len(s.Selectors) == 0 || len(s.Selectors) > 256 {
		return false
	}
	prior := ""
	for _, v := range s.Selectors {
		if len(v) > 128 || v == "" || v <= prior {
			return false
		}
		for _, c := range v {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
		prior = v
	}
	return true
}

// Clone owns the selector storage.
func (s InventoryScope) Clone() InventoryScope { s.Selectors = slices.Clone(s.Selectors); return s }

// Fingerprint hashes the exact contract, family and canonical requested selectors.
func (s InventoryScope) Fingerprint() string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Contains checks the typed member's declared subscription and ARM identity.
func (s InventoryScope) Contains(e EdgeObservation) bool {
	return s.Valid() && e.Source == "azure" && e.OriginKind == "azure.subscription" && e.ResourceKind == s.Family &&
		slices.Contains(s.Selectors, e.OriginRef) && len(e.ResourceRef) <= 4096 &&
		e.ResourceRef == strings.ToLower(e.ResourceRef) && strings.HasPrefix(e.ResourceRef, "/subscriptions/"+e.OriginRef+"/") &&
		len(e.ResourceRef) > len("/subscriptions/"+e.OriginRef+"/")
}

// InventoryCollectionStart opens one scope in the current Gather. The host owns
// all run identity and delays terminal qualification until Gather returns.
type InventoryCollectionStart struct {
	Scope      InventoryScope
	ObservedAt time.Time
}

func (InventoryCollectionStart) ObservationType() ObservationType {
	return "inventory_collection_start"
}
func (InventoryCollectionStart) isObservation() {}

// InventoryCollectionMember explicitly separates query resources from activity,
// RAI and tenant/subscription topology emitted in the same Gather.
type InventoryCollectionMember struct{ Edge EdgeObservation }

func (InventoryCollectionMember) ObservationType() ObservationType {
	return "inventory_collection_member"
}
func (InventoryCollectionMember) isObservation() {}

// InventoryCollectionReport proposes enumeration coverage, never persistence.
// Complete means this query's enumeration over an interval, not an atomic estate.
type InventoryCollectionReport struct {
	State          string
	Reason         string
	RequestedScope string
	FulfilledScope string
	Count          int64
	ObservedUntil  time.Time
}

func (InventoryCollectionReport) ObservationType() ObservationType {
	return "inventory_collection_report"
}
func (InventoryCollectionReport) isObservation() {}

// ValidInventoryResult limits persisted vocabulary and diagnostics.
func ValidInventoryResult(state, reason string) bool {
	switch state {
	case "complete", "partial", "unavailable", "unsupported", "unknown":
	default:
		return false
	}
	switch reason {
	case "", "exhausted", "page_limit", "repeated_cursor", "invalid_response", "scope_unproven", "scope_mismatch", "provider_error", "offline", "disabled", "member_limit", "missing_report", "protocol_error", "gather_error", "canceled", "sink_error", "persistence_rejected", "commit_outcome_unknown", "persistence_canceled", "persistence_unavailable", "persistence_error":
		return true
	default:
		return false
	}
}

// ValidInventoryFingerprint accepts only absent proof or a canonical SHA-256.
func ValidInventoryFingerprint(v string) bool {
	if v == "" {
		return true
	}
	if len(v) != 64 || v != strings.ToLower(v) {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}
