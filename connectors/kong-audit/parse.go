// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kongaudit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// The resource/subject kinds emitted for the two Kong audit streams.
const (
	// resourceAdminAPI is the ResourceKind for an Admin API access edge. The
	// ResourceRef is the request path (the control-plane "API" being touched).
	resourceAdminAPI = "kong.admin_api"
	// subjectEntity is the SubjectKind for a config-change finding. The SubjectRef
	// is "<dao_name>:<entity_key>".
	subjectEntity = "kong.entity"
)

// findingKind is the FindingReport.Kind for a Kong config change.
const findingKind = "gateway_config_change"

// record is the tolerant union of the two Kong audit shapes. Kong's audit log has
// no version/type discriminator field, so the connector distinguishes the streams
// by which fields are present (see kind): an audit_objects record has dao_name +
// operation; an audit_requests record has method + path. The struct decodes both
// because the field sets do not collide.
//
// Verified against developer.konghq.com/gateway/audit-logs/ and the Kong Gateway
// Admin API "Audit Log" reference field tables / example JSON. This is the
// documented expected shape of an EXPORTED Kong audit entry, not an invented
// standard — Kong does not publish a stable JSON Schema, so the struct is tolerant
// (optional fields, several known aliases for the RBAC user).
//
// DELIBERATELY ABSENT — the connector emits the edge/finding, never the body
// (docs/SECURITY-HARDENING.md): "payload" (the audit_requests request body) and "entity" (the
// audit_objects full row snapshot, which for a credential entity holds secrets).
// Not declaring them means json.Unmarshal drops them and they can never leak.
type record struct {
	// Common.
	RequestID string `json:"request_id"`

	// audit_requests fields.
	RequestTimestamp *float64 `json:"request_timestamp"` // Unix epoch seconds; pointer => distinguish 0 from absent
	ClientIP         string   `json:"client_ip"`
	Path             string   `json:"path"`
	Method           string   `json:"method"`
	Workspace        string   `json:"workspace"`

	// RBAC identity (audit_requests and audit_objects). rbac_user_id is the
	// documented field (a UUID); rbac_user_name / rbac_user are surfaced by some
	// builds — all are read for attribution, none is a secret.
	RBACUserID   string `json:"rbac_user_id"`
	RBACUserName string `json:"rbac_user_name"`
	RBACUser     string `json:"rbac_user"`

	// audit_objects fields.
	DAOName   string   `json:"dao_name"`
	EntityKey string   `json:"entity_key"`
	Operation string   `json:"operation"`
	Expire    *float64 `json:"expire"` // Unix epoch seconds; creation + record TTL
	TTL       *float64 `json:"ttl"`    // record TTL in seconds
}

// streamKind is which Kong audit stream a record came from.
type streamKind int

const (
	// streamUnknown is a record that fits neither shape (skipped).
	streamUnknown streamKind = iota
	// streamRequests is an audit_requests record (an Admin API access).
	streamRequests
	// streamObjects is an audit_objects record (a config change).
	streamObjects
)

// kind classifies a record by its present fields. audit_objects is checked FIRST
// because an object record may also carry a request_id (and conceivably more), but
// only an object record carries dao_name+operation; an audit_requests record is
// identified by its method+path. A record with neither is unknown.
func (r record) kind() streamKind {
	if strings.TrimSpace(r.DAOName) != "" && strings.TrimSpace(r.Operation) != "" {
		return streamObjects
	}
	if strings.TrimSpace(r.Method) != "" && strings.TrimSpace(r.Path) != "" {
		return streamRequests
	}
	return streamUnknown
}

// rbacUser returns the acting RBAC user reference (id preferred, then name, then
// the generic alias), or "" when the request was unauthenticated (RBAC off).
func (r record) rbacUser() string {
	for _, v := range []string{r.RBACUserID, r.RBACUserName, r.RBACUser} {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// requestsFromBytes extracts Kong audit records from a file's bytes. Kong exports
// in three real shapes, all handled: a single JSON object, newline-delimited JSON
// (the common file/collector form), or a JSON array of records. A line/element
// that is not a usable Kong audit record (neither stream) is skipped, never
// guessed.
func recordsFromBytes(data []byte) []record {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil
	}

	// JSON array of records.
	if data[0] == '[' {
		var arr []record
		if err := json.Unmarshal(data, &arr); err == nil {
			return keepKnown(arr)
		}
	}

	// Single JSON object spanning the whole file.
	if data[0] == '{' {
		var one record
		if err := json.Unmarshal(data, &one); err == nil && one.kind() != streamUnknown {
			return []record{one}
		}
	}

	// Newline-delimited JSON (default).
	var recs []record
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var r record
		if err := json.Unmarshal(line, &r); err == nil && r.kind() != streamUnknown {
			recs = append(recs, r)
		}
	}
	return recs
}

// keepKnown drops array elements that are not a recognizable Kong audit record.
func keepKnown(in []record) []record {
	out := in[:0]
	for _, r := range in {
		if r.kind() != streamUnknown {
			out = append(out, r)
		}
	}
	return out
}

// requestTime returns the time of an audit_requests record from request_timestamp
// (Unix epoch seconds). ok=false when absent so the caller can skip rather than
// emit an edge with a zero time.
func (r record) requestTime() (time.Time, bool) {
	if r.RequestTimestamp == nil {
		return time.Time{}, false
	}
	return epochToTime(*r.RequestTimestamp), true
}

// objectTime reconstructs the time of an audit_objects change. The documented
// shape carries no direct timestamp, so it is derived from (expire - ttl) — Kong
// sets expire = creation_time + record_ttl. It falls back to an explicit
// request_timestamp if a build supplies one. ok=false when neither is available;
// the caller then stamps the connector clock (documented, not fabricated as a
// source timestamp).
func (r record) objectTime() (time.Time, bool) {
	if r.Expire != nil && r.TTL != nil {
		return epochToTime(*r.Expire - *r.TTL), true
	}
	if r.RequestTimestamp != nil {
		return epochToTime(*r.RequestTimestamp), true
	}
	return time.Time{}, false
}

// epochToTime converts a possibly-fractional Unix epoch-seconds value to a UTC
// time. Kong emits integer seconds; the fractional path is defensive.
func epochToTime(sec float64) time.Time {
	whole := int64(sec)
	nanos := int64((sec - float64(whole)) * 1e9)
	return time.Unix(whole, nanos).UTC()
}
