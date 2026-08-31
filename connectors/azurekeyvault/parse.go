// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azurekeyvault

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// The diagnostic log category Key Vault and Managed HSM emit audit records under.
const categoryAuditEvent = "AuditEvent"

// Resource kinds emitted, keyed by the operationName object prefix.
const (
	resourceKey  = "azure.keyvault.key"
	resourceSec  = "azure.keyvault.secret"
	resourceCert = "azure.keyvault.certificate"
	resourceVlt  = "azure.keyvault.vault"
)

// record is the subset of an exported Key Vault / Managed HSM AuditEvent the
// connector reads. Deliberately ABSENT: the request URL query string (it can carry
// a SAS token), the request/response bodies, the authorization assignments — the
// connector emits the access edge, never a credential or payload (docs/SECURITY-HARDENING.md).
//
// identity is json.RawMessage because Key Vault encodes it as a nested object
// ({"claim":{...}}) while Managed HSM encodes it as a STRINGIFIED JSON blob
// ("{\"claim\":{...}}") — both are handled by caller().
type record struct {
	Time          string          `json:"time"`
	Category      string          `json:"category"`
	OperationName string          `json:"operationName"`
	ResourceID    string          `json:"resourceId"`
	ResourceIDAlt string          `json:"_ResourceId"` // Log Analytics flattened form
	ID            string          `json:"id"`          // the request URI (key/secret object), when present
	Identity      json.RawMessage `json:"identity"`
	Properties    properties      `json:"properties"`
}

type properties struct {
	ID string `json:"id"` // some exports nest the object URI here
}

// claimBlock is the caller identity claim set. Only non-secret identity claims are
// read: the app id, the object identifier and the UPN. The token itself, its hash
// and the authorization block are never modeled.
type claimBlock struct {
	Claim map[string]any `json:"claim"`
}

// caller returns the best non-secret principal reference from the identity blob,
// handling both the nested-object (Key Vault) and stringified (Managed HSM) forms.
// Preference: appid (the AAD app / service principal) > objectidentifier (oid) >
// upn. Empty if none present.
func (r record) caller() string {
	raw := bytes.TrimSpace(r.Identity)
	if len(raw) == 0 {
		return ""
	}
	// Managed HSM: identity is a JSON STRING containing JSON. Unwrap one level.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			raw = []byte(s)
		}
	}
	var cb claimBlock
	if err := json.Unmarshal(raw, &cb); err != nil {
		return ""
	}
	for _, k := range []string{
		"appid",
		"http://schemas.microsoft.com/identity/claims/objectidentifier",
		"oid",
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/upn",
		"upn",
	} {
		if v, ok := cb.Claim[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// writeOps are the operationName values that MUTATE a vault/key/secret/cert
// (create/import/update/set/delete/recover/purge/backup/restore). Everything else
// that names an object — get/list and the crypto-use ops (sign/verify/encrypt/
// decrypt/wrap/unwrap) — is a read. An operationName outside both sets yields
// ModeUnknown (never guessed). Authentication names no object and is skipped.
var writeOps = map[string]bool{
	"VaultPut": true, "VaultPatch": true, "VaultDelete": true, "VaultAccessPolicyChanged": true,
	"KeyCreate": true, "KeyImport": true, "KeyUpdate": true, "KeyDelete": true,
	"KeyRecover": true, "KeyPurge": true, "KeyBackup": true, "KeyRestore": true, "KeyRotate": true,
	"SecretSet": true, "SecretUpdate": true, "SecretDelete": true,
	"SecretRecover": true, "SecretPurge": true, "SecretBackup": true, "SecretRestore": true,
	"CertificateCreate": true, "CertificateImport": true, "CertificateUpdate": true,
	"CertificateDelete": true, "CertificateRecover": true, "CertificatePurge": true,
}

var readOps = map[string]bool{
	"VaultGet": true,
	"KeyGet":   true, "KeyList": true, "KeyListVersions": true,
	"KeySign": true, "KeyVerify": true, "KeyEncrypt": true, "KeyDecrypt": true,
	"KeyWrap": true, "KeyUnwrap": true, "KeyRelease": true,
	"SecretGet": true, "SecretList": true, "SecretListVersions": true,
	"CertificateGet": true, "CertificateList": true, "CertificateListVersions": true,
}

// classify maps a record's operationName to a resource kind and access mode, plus
// ok=false for an operation that names no governed object (Authentication, an empty
// or unrecognized object prefix).
func classify(r record) (kind string, mode model.AccessMode, ok bool) {
	op := r.OperationName
	switch {
	case strings.HasPrefix(op, "Vault"):
		kind = resourceVlt
	case strings.HasPrefix(op, "Key"):
		kind = resourceKey
	case strings.HasPrefix(op, "Secret"):
		kind = resourceSec
	case strings.HasPrefix(op, "Certificate"):
		kind = resourceCert
	default:
		return "", model.ModeUnknown, false // Authentication, etc.
	}
	switch {
	case writeOps[op]:
		mode = model.ModeWrite
	case readOps[op]:
		mode = model.ModeRead
	default:
		mode = model.ModeUnknown
	}
	return kind, mode, true
}

// resourceID returns the canonical (lower-cased) vault/HSM resourceId — Azure
// uppercases it in logs, so this normalizes for stable convergence.
func (r record) resourceID() string {
	id := r.ResourceID
	if id == "" {
		id = r.ResourceIDAlt
	}
	return strings.ToLower(strings.TrimSpace(id))
}

// objectRef returns the specific key/secret/cert object URI when the record carries
// one (id or properties.id), else the vault/HSM resourceId — so an edge always
// names something stable. The query string is dropped: a Key Vault request URI can
// in principle carry a SAS token in the query, which is a credential (docs/SECURITY-HARDENING.md)
// and is never part of the object's identity.
func (r record) objectRef() string {
	if r.ID != "" {
		return stripQuery(r.ID)
	}
	if r.Properties.ID != "" {
		return stripQuery(r.Properties.ID)
	}
	return r.resourceID()
}

// stripQuery drops a URI's query string and fragment (a SAS token rides in the
// query — docs/SECURITY-HARDENING.md, never persisted).
func stripQuery(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		return s[:i]
	}
	return s
}

// store identifies a Key Vault or Managed HSM custodian for the inventory.
type store struct {
	id      string // canonical resourceId
	service string // "keyvault" | "managedhsm"
	name    string
}

func (s store) ref() string { return s.id }

func (s store) displayName() string {
	if s.service == "managedhsm" {
		return "Azure Managed HSM (" + s.name + ")"
	}
	return "Azure Key Vault (" + s.name + ")"
}

// storeFromResourceID parses the service and name out of a canonical Key Vault /
// Managed HSM resourceId. ok=false for a non-Key-Vault resource id.
func storeFromResourceID(id string) (store, bool) {
	if id == "" {
		return store{}, false
	}
	low := id // already lower-cased by resourceID()
	var service string
	switch {
	case strings.Contains(low, "/providers/microsoft.keyvault/managedhsms/"):
		service = "managedhsm"
	case strings.Contains(low, "/providers/microsoft.keyvault/vaults/"):
		service = "keyvault"
	default:
		return store{}, false
	}
	name := low[strings.LastIndexByte(low, '/')+1:]
	if name == "" {
		return store{}, false
	}
	return store{id: id, service: service, name: name}, true
}

// recordsFromBytes parses exported AuditEvent records, accepting a {"records":[…]}
// wrapper (storage account export), a JSON array, NDJSON, or a single object.
func recordsFromBytes(data []byte) []record {
	var wrap struct {
		Records []record `json:"records"`
	}
	if err := json.Unmarshal(data, &wrap); err == nil && len(wrap.Records) > 0 {
		return wrap.Records
	}
	var arr []record
	if err := json.Unmarshal(data, &arr); err == nil && len(arr) > 0 {
		return arr
	}
	var recs []record
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r record
		if err := json.Unmarshal(line, &r); err == nil && r.OperationName != "" {
			recs = append(recs, r)
		}
	}
	if len(recs) > 0 {
		return recs
	}
	var one record
	if err := json.Unmarshal(data, &one); err == nil && one.OperationName != "" {
		return []record{one}
	}
	return nil
}

var azureTimeLayouts = []string{time.RFC3339Nano, time.RFC3339}

func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range azureTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
