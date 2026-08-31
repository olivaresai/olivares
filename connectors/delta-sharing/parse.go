// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package deltasharing

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// entry is the subset of a Delta Sharing server audit-log line the connector
// reads. The Delta Sharing open protocol identifies a recipient access by the
// recipient credential and the share/schema/table path of the RPC it called
// (delta-io/delta-sharing PROTOCOL.md: the sharing server serves /shares,
// /shares/{share}/schemas, /shares/{share}/schemas/{schema}/tables,
// .../tables/{table}/version, .../{table}/metadata, .../{table}/query).
//
// Only the edge-shaped fields are declared. The query predicate, the response
// rows, the pre-signed file URLs and the recipient bearer token are deliberately
// NOT fields here: the connector emits the access edge, never the payload or any
// credential (minimal data, docs/SECURITY-HARDENING.md).
//
// PROVENANCE OF THE WIRE SHAPE (read before changing a field): the flat
// recipient/share/schema/table/action shape is NOT the platform's native audit
// schema. Databricks Unity-Catalog records a recipient read in
// system.access.audit with `actionName` = deltaSharingQueriedTable (or
// deltaSharingQueriedTableChanges) and nests the identifiers under
// request_params.{recipient_name, share_name, table_full_name}; the delta-io
// reference sharing server (DeltaSharingService.scala) emits NO action-keyed
// audit log at all. This flat shape is the operator-normalized export contract
// dictated VERBATIM by the session brief, which an operator job
// produces by flattening whichever native source it has into one JSON object
// per line. The connector is faithful to that brief contract, not to either
// platform's raw schema.
type entry struct {
	Timestamp string `json:"timestamp"` // ISO-8601/RFC3339 'Z' instant of the access
	Recipient string `json:"recipient"` // the cross-org recipient identity
	Share     string `json:"share"`     // shared dataset (top-level share)
	Schema    string `json:"schema"`    // schema within the share
	Table     string `json:"table"`     // table within the schema
	Action    string `json:"action"`    // operator-export action token: queryTable | getTableData | …
}

// parseEntry decodes one audit JSON line. It returns ok=false for a line that is
// not valid JSON or carries no action (nothing to classify).
func parseEntry(line []byte) (entry, bool) {
	var e entry
	if err := json.Unmarshal(line, &e); err != nil {
		return entry{}, false
	}
	e.Recipient = strings.TrimSpace(e.Recipient)
	e.Share = strings.TrimSpace(e.Share)
	e.Schema = strings.TrimSpace(e.Schema)
	e.Table = strings.TrimSpace(e.Table)
	e.Action = strings.TrimSpace(e.Action)
	if e.Action == "" {
		return entry{}, false
	}
	return e, true
}

// classifyAction maps a Delta Sharing recipient action token to an AccessMode —
// never inferred from a query body. Every recipient action is a READ of shared
// data leaving the provider org: listing shares/schemas/tables, reading metadata
// or a table version, and querying/pulling table data are all egress. Delta
// Sharing is one-directional — a recipient cannot write back through it — so
// there is no write mode to emit. An action the export records that is not a
// recognized recipient read yields ModeUnknown: the read/write nature is stated
// as unknown, never guessed (ARCHITECTURE.md).
//
// VOCABULARY PROVENANCE (every token below is reconciled to a confirmable
// authority — none is invented platform vocabulary):
//
//   - listShares, getShare, listSchemas, listTables, listAllTables,
//     getTableVersion, getMetadata are the delta-io reference sharing server's
//     handler names (delta-io/delta-sharing DeltaSharingService.scala). Note the
//     metadata handler is named getMetadata (GET .../metadata), NOT
//     getTableMetadata, which is not a string in either authority.
//   - queryTable and getTableData are the brief's SYNTHETIC tokens
//     for the POST .../query handler (named listFiles in the reference server;
//     the open-protocol path segment is `query`). They are NOT platform strings:
//     the Databricks Unity-Catalog audit names this recipient read with
//     actionName deltaSharingQueriedTable / deltaSharingQueriedTableChanges, and
//     the reference server emits no action-keyed audit at all. They are kept
//     because the session brief defines them verbatim as the operator-export
//     contract — a future reader must not mistake them for native platform
//     vocabulary.
func classifyAction(action string) model.AccessMode {
	switch action {
	// Reference-server handler names (delta-io DeltaSharingService.scala).
	case "listShares", "getShare",
		"listSchemas", "listTables", "listAllTables",
		"getTableVersion", "getMetadata":
		return model.ModeRead
	// Brief synthetic tokens for POST .../query (reference handler
	// listFiles); not platform vocabulary — see VOCABULARY PROVENANCE above.
	case "queryTable", "getTableData":
		return model.ModeRead
	default:
		return model.ModeUnknown
	}
}

// isTableScoped reports whether an action's resource is a specific shared table
// (vs a share-level listing). A table-scoped action carries a table name; a
// share-level action (e.g. listShares, listSchemas) does not, so its resource is
// the share itself.
func isTableScoped(e entry) bool {
	return e.Table != ""
}

// dsTimeLayouts are the timestamp formats the Delta Sharing audit log emits. The
// audit records an ISO-8601 / RFC3339 'Z' instant; both the with- and
// without-fractional-seconds forms are accepted and normalized to UTC.
var dsTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
}

// parseTime parses an audit timestamp and normalizes it to UTC, returning
// ok=false if no layout matches. ObservedAt always comes from this source
// timestamp, never time.Now(): it is the dedup natural key (docs/contracts).
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range dsTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
