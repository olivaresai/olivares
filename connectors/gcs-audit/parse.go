// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gcsaudit

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk/model"
)

// gcsServiceName is the Cloud Audit Logs serviceName for Cloud Storage. Entries
// from any other service are ignored (other GCP services are out of scope here).
const gcsServiceName = "storage.googleapis.com"

// entry is the subset of a Cloud Logging LogEntry the connector reads. Only the
// fields needed to build the access edge are declared; the request/response/
// payload (metadata, request, response, status detail) are deliberately NOT read
// — the connector emits the edge, never the body (docs/SECURITY-HARDENING.md).
type entry struct {
	// Timestamp is the top-level LogEntry timestamp (RFC3339 with Z). It is the
	// time of the audited operation and the dedup natural key (docs/contracts).
	Timestamp    string       `json:"timestamp"`
	ProtoPayload protoPayload `json:"protoPayload"`
}

// protoPayload is the google.cloud.audit.AuditLog payload of the entry. Only the
// identity, method and resource references are read; authorizationInfo, request,
// response, requestMetadata and status are intentionally not parsed.
type protoPayload struct {
	ServiceName        string             `json:"serviceName"`
	MethodName         string             `json:"methodName"`
	ResourceName       string             `json:"resourceName"`
	AuthenticationInfo authenticationInfo `json:"authenticationInfo"`
}

// authenticationInfo carries the principal the access is attributed to. Only the
// principal's email (the identity reference) is read — never a token, key or any
// credential value (docs/SECURITY-HARDENING.md).
type authenticationInfo struct {
	PrincipalEmail string `json:"principalEmail"`
}

// classifyMethod maps a Cloud Storage methodName to an AccessMode, verbatim from
// the platform's own operation vocabulary. A methodName the platform does not map
// to a clear read or write (anything outside the listed set) yields ModeUnknown:
// the read/write nature is not guessed (ARCHITECTURE.md, docs/contracts).
func classifyMethod(method string) model.AccessMode {
	switch method {
	case "storage.objects.get", "storage.objects.list",
		"storage.buckets.get", "storage.buckets.list":
		return model.ModeRead
	case "storage.objects.create", "storage.objects.delete", "storage.objects.update":
		return model.ModeWrite
	default:
		return model.ModeUnknown
	}
}

// resolveResource maps a Cloud Storage resourceName to the resource kind and a
// gs:// reference. The audit resourceName is
// "projects/_/buckets/BUCKET/objects/OBJECT" for an object and
// "projects/_/buckets/BUCKET" for a bucket. An object yields
// ("gcs.object", "gs://BUCKET/OBJECT"); a bucket yields ("gcs.bucket",
// "gs://BUCKET"). ok=false if no bucket can be parsed.
func resolveResource(resourceName string) (kind, ref string, ok bool) {
	const bucketsSeg = "buckets/"
	i := strings.Index(resourceName, bucketsSeg)
	if i < 0 {
		return "", "", false
	}
	rest := resourceName[i+len(bucketsSeg):]
	if rest == "" {
		return "", "", false
	}
	if j := strings.Index(rest, "/objects/"); j >= 0 {
		bucket := rest[:j]
		object := rest[j+len("/objects/"):]
		if bucket == "" || object == "" {
			return "", "", false
		}
		return "gcs.object", "gs://" + bucket + "/" + object, true
	}
	// Bucket-level resourceName: the bucket is the remainder (it may carry no
	// trailing path; a stray trailing slash is trimmed).
	bucket := strings.TrimSuffix(rest, "/")
	if bucket == "" {
		return "", "", false
	}
	return "gcs.bucket", "gs://" + bucket, true
}

// entryFromLine parses one NDJSON Cloud Logging LogEntry line. It returns
// ok=false for a line that is not valid JSON.
func entryFromLine(line []byte) (entry, bool) {
	var e entry
	if err := json.Unmarshal(line, &e); err != nil {
		return entry{}, false
	}
	return e, true
}

// originKind is the EdgeObservation origin kind (always "identity").
const originKind = identity.OriginKind

// gcsTimeLayouts are the timestamp formats Cloud Logging emits in the top-level
// timestamp field (RFC3339, with or without fractional seconds, always 'Z' UTC).
var gcsTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
}

// parseTime parses a Cloud Logging timestamp and normalizes it to UTC, returning
// ok=false if no layout matches.
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range gcsTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
