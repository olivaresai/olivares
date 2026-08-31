// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package icebergcatalog

import (
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// The Polaris table-data privilege tokens this connector classifies as an R/RW
// data-access grant. They are matched verbatim; every other privilege token
// (TABLE_CREATE, TABLE_DROP, TABLE_READ_PROPERTIES, … and the catalog/namespace
// admin privileges) is a metadata/admin grant, not a data read or write, so it
// produces no edge (https://polaris.apache.org/releases/1.0.0/access-control/).
const (
	privTableReadData  = "TABLE_READ_DATA"
	privTableWriteData = "TABLE_WRITE_DATA"
)

// resourceKind is the resource class for every edge: an Iceberg table.
const resourceKind = "iceberg.table"

// snapshot is the catalog export the operator ships: the catalog's current static
// grants plus its outstanding vended (short-lived) credentials, as of snapshotAt.
// Only the fields the connector reads are declared — no storage credential, token
// or secret has a field here (minimal-data, docs/SECURITY-HARDENING.md).
type snapshot struct {
	SnapshotAt        string             `json:"snapshot_at"`
	Grants            []grant            `json:"grants"`
	VendedCredentials []vendedCredential `json:"vended_credentials"`
}

// grant is one static catalog grant: a principal is permitted privilege on table.
type grant struct {
	Principal string `json:"principal"` // e.g. "role:analysts"
	Table     string `json:"table"`     // catalog.namespace.table
	Privilege string `json:"privilege"` // e.g. "TABLE_READ_DATA"
}

// vendedCredential is one outstanding short-lived credential the catalog vended to
// a principal for a table, carrying one or more privileges. The credential MATERIAL
// is deliberately absent: only the vended principal identifier and the privileges
// are read. expires_at is read for completeness of the documented shape but is not
// emitted (the natural-key timestamp is the snapshot time — see Source.buildEdges).
type vendedCredential struct {
	Principal  string   `json:"principal"` // e.g. "vended:abc123"
	Table      string   `json:"table"`     // catalog.namespace.table
	Privileges []string `json:"privileges"`
	ExpiresAt  string   `json:"expires_at"`
}

// privilegeToMode maps a Polaris table-data privilege to an AccessMode, verbatim.
// The second result is false for any privilege that is not a data R/RW grant
// (metadata/admin privileges), which the connector skips rather than emit a
// meaningless or guessed edge (ARCHITECTURE.md: unknown is explicit, never invented).
func privilegeToMode(privilege string) (model.AccessMode, bool) {
	switch strings.TrimSpace(privilege) {
	case privTableReadData:
		return model.ModeRead, true
	case privTableWriteData:
		return model.ModeWrite, true
	default:
		return "", false
	}
}

// icebergTimeLayouts are the timestamp formats the catalog export emits in
// snapshot_at, ISO-8601/RFC3339 with a 'Z' (UTC) or numeric offset.
var icebergTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
}

// parseTime parses a snapshot timestamp and normalizes it to UTC, returning
// ok=false if no layout matches. ObservedAt must come from the source's own clock
// (the snapshot time), never time.Now() — it is the dedup natural key.
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range icebergTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
