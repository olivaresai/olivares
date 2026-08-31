// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureblobaudit

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// record is the subset of an exported StorageBlobLogs entry the connector reads.
//
// The field names follow the exported Azure Storage resource-log schema (logs
// routed to a storage account / event hub), where the timestamp, category,
// operation and uri are top-level camelCase, the OAuth requester app id lives at
// identity.requester.appId and the authentication type at identity.type, and the
// account name lives at properties.accountName. The duplicated top-level
// AuthenticationType / RequesterAppID / AccountName tolerate the flattened
// Log Analytics export shape, which the operator may ship instead.
//
// Deliberately ABSENT: the request URL query string, any token, the tokenHash,
// the request/response bodies, the authorization block — the connector emits only
// the access edge, never a credential or payload (docs/SECURITY-HARDENING.md).
type record struct {
	Time          string `json:"time"`
	Category      string `json:"category"`
	OperationName string `json:"operationName"`
	URI           string `json:"uri"`

	// Exported resource-log nesting: identity.type, identity.requester.appId.
	Identity identityBlock `json:"identity"`
	// Exported resource-log nesting: properties.accountName.
	Properties propertiesBlock `json:"properties"`

	// Flattened (Log Analytics) tolerance.
	AuthenticationTypeFlat string `json:"authenticationType"`
	RequesterAppIDFlat     string `json:"requesterAppId"`
	AccountNameFlat        string `json:"accountName"`
}

// identityBlock is the exported "identity" object: only the authentication type
// and the OAuth requester's application id are read. The token hash, the
// authorization assignments and the delegated-resource block are intentionally
// not modeled.
type identityBlock struct {
	Type      string         `json:"type"`
	Requester requesterBlock `json:"requester"`
}

// requesterBlock is the exported "identity.requester" object: only the OAuth
// application id is read (the AAD app / service principal that made the request).
type requesterBlock struct {
	AppID string `json:"appId"`
}

// propertiesBlock is the exported "properties" object: only the account name is
// read here (the request URL, bodies, headers and md5 hashes are not modeled).
type propertiesBlock struct {
	AccountName string `json:"accountName"`
}

// recordFromJSON parses one exported StorageBlobLogs line. It returns ok=false
// for a line that is not valid JSON.
func recordFromJSON(line []byte) (record, bool) {
	var r record
	if err := json.Unmarshal(line, &r); err != nil {
		return record{}, false
	}
	return r, true
}

// requesterAppID returns the AAD application / service principal id the log
// attributes the request to, preferring the exported nested location
// (identity.requester.appId) and tolerating the flattened Log Analytics field.
func (r record) requesterAppID() string {
	return firstNonEmpty(r.Identity.Requester.AppID, r.RequesterAppIDFlat)
}

// authType returns the authentication type (e.g. OAuth, AccountKey, SAS,
// Anonymous), preferring the exported nested location (identity.type) and
// tolerating the flattened Log Analytics field.
func (r record) authType() string {
	return firstNonEmpty(r.Identity.Type, r.AuthenticationTypeFlat)
}

// Categories the Azure diagnostic setting assigns to a blob operation. They are
// used as the fallback classifier when operationName is not recognized.
const (
	categoryRead   = "StorageRead"
	categoryWrite  = "StorageWrite"
	categoryDelete = "StorageDelete"
)

// classifyMode maps a StorageBlobLogs access to a mode, verbatim from the
// source's own vocabulary (docs/contracts). The operationName decides
// first; for an operationName the connector does not recognize it falls back to
// the log's own category. If neither classifies the access, the result is
// ModeUnknown — the read/write nature is never guessed (ARCHITECTURE.md).
func classifyMode(operationName, category string) model.AccessMode {
	switch operationName {
	case "GetBlob", "GetBlobProperties", "ListBlobs":
		return model.ModeRead
	case "PutBlob", "PutBlock", "PutBlockList", "SetBlobMetadata":
		return model.ModeWrite
	case "DeleteBlob":
		return model.ModeWrite
	}
	// Unrecognized operationName: fall back to the diagnostic category.
	switch category {
	case categoryRead:
		return model.ModeRead
	case categoryWrite:
		return model.ModeWrite
	case categoryDelete:
		return model.ModeWrite
	default:
		return model.ModeUnknown
	}
}

// resolveResource parses an Azure Blob request URI into a resource kind and a
// reference. The URI has the form
//
//	https://<account>.blob.core.windows.net/<container>/<blob>?<query>
//
// which maps to "<account>/<container>/<blob>" (azureblob.object) or, when no
// blob path is present, "<account>/<container>" (azureblob.container). The query
// string is dropped — it can carry a SAS token (docs/SECURITY-HARDENING.md) and is never part of
// the resource identity. ok=false if no account/container can be determined.
func resolveResource(uri string) (kind, ref string, ok bool) {
	u := strings.TrimSpace(uri)
	if u == "" {
		return "", "", false
	}
	// Drop scheme.
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	// Drop query (may carry a SAS token) and fragment.
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	// Split host from path.
	host, path, _ := strings.Cut(u, "/")
	if host == "" {
		return "", "", false
	}
	account, _, _ := strings.Cut(host, ".") // "<account>.blob.core.windows.net" -> "<account>"
	if account == "" {
		return "", "", false
	}

	path = strings.Trim(path, "/")
	if path == "" {
		return "", "", false // a service-level request names neither container nor blob
	}
	container, blob, hasBlob := strings.Cut(path, "/")
	if container == "" {
		return "", "", false
	}
	if hasBlob && blob != "" {
		return "azureblob.object", account + "/" + container + "/" + blob, true
	}
	return "azureblob.container", account + "/" + container, true
}

// blobTimeLayouts are the timestamp formats StorageBlobLogs emits in its `time`
// field — ISO-8601 / RFC3339 with a 'Z' (UTC) zone, with fractional seconds at
// 7-digit (Azure) precision, which time.RFC3339Nano parses.
var blobTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
}

// parseTime parses a StorageBlobLogs `time` value and normalizes it to UTC,
// returning ok=false if no layout matches.
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range blobTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
