// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gcpkms

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// The Cloud Audit Logs serviceName values this connector recognizes. Verified
// against cloud.google.com (kms/secret-manager audit-logging).
const (
	kmsServiceName    = "cloudkms.googleapis.com"
	secretServiceName = "secretmanager.googleapis.com"
)

// The resource kinds emitted for the two services.
const (
	resourceKMSKey = "gcp.kms.key"
	resourceSecret = "gcp.secret"
)

// The full methodName prefixes (verified verbatim).
const (
	kmsMethodPrefix    = "google.cloud.kms.v1.KeyManagementService."
	secretMethodPrefix = "google.cloud.secretmanager.v1.SecretManagerService."
)

// entry is the subset of a Cloud Logging LogEntry the connector reads. Only the
// identity, method and resource references are read; authorizationInfo, request,
// response, requestMetadata and status are intentionally NOT parsed (docs/SECURITY-HARDENING.md).
type entry struct {
	Timestamp    string       `json:"timestamp"`
	ProtoPayload protoPayload `json:"protoPayload"`
}

type protoPayload struct {
	ServiceName        string             `json:"serviceName"`
	MethodName         string             `json:"methodName"`
	ResourceName       string             `json:"resourceName"`
	AuthenticationInfo authenticationInfo `json:"authenticationInfo"`
}

type authenticationInfo struct {
	PrincipalEmail string `json:"principalEmail"`
}

// kmsWriteMethods and secretWriteMethods are the methods that MUTATE a key/secret
// (admin writes). Everything else that names a resource — crypto USE
// (Decrypt/Encrypt/AsymmetricSign/…), get and list — is a read. A method outside
// the recognized set yields ModeUnknown (never guessed, ARCHITECTURE.md). The short
// method name (after the service prefix) is matched.
var kmsWriteMethods = map[string]bool{
	"CreateCryptoKey":               true,
	"CreateCryptoKeyVersion":        true,
	"UpdateCryptoKey":               true,
	"UpdateCryptoKeyVersion":        true,
	"DestroyCryptoKeyVersion":       true,
	"ImportCryptoKeyVersion":        true,
	"UpdateCryptoKeyPrimaryVersion": true,
	"CreateKeyRing":                 true,
	"CreateImportJob":               true,
}

var kmsReadMethods = map[string]bool{
	"Decrypt":               true,
	"Encrypt":               true,
	"AsymmetricSign":        true,
	"AsymmetricDecrypt":     true,
	"MacSign":               true,
	"MacVerify":             true,
	"RawEncrypt":            true,
	"RawDecrypt":            true,
	"GetPublicKey":          true,
	"GetCryptoKey":          true,
	"GetCryptoKeyVersion":   true,
	"ListCryptoKeys":        true,
	"ListCryptoKeyVersions": true,
}

var secretWriteMethods = map[string]bool{
	"AddSecretVersion":     true,
	"CreateSecret":         true,
	"UpdateSecret":         true,
	"DeleteSecret":         true,
	"DestroySecretVersion": true,
	"DisableSecretVersion": true,
	"EnableSecretVersion":  true,
	"SetIamPolicy":         true,
}

var secretReadMethods = map[string]bool{
	"AccessSecretVersion": true,
	"GetSecret":           true,
	"GetSecretVersion":    true,
	"ListSecrets":         true,
	"ListSecretVersions":  true,
	"GetIamPolicy":        true,
}

// classify returns the resource kind and access mode for an entry, plus ok=false
// if the entry is not a recognized KMS/Secret-Manager method. The short method is
// the segment after the service prefix.
func classify(e entry) (kind string, mode model.AccessMode, ok bool) {
	method := e.ProtoPayload.MethodName
	switch e.ProtoPayload.ServiceName {
	case kmsServiceName:
		short := strings.TrimPrefix(method, kmsMethodPrefix)
		switch {
		case kmsWriteMethods[short]:
			return resourceKMSKey, model.ModeWrite, true
		case kmsReadMethods[short]:
			return resourceKMSKey, model.ModeRead, true
		default:
			return resourceKMSKey, model.ModeUnknown, true
		}
	case secretServiceName:
		short := strings.TrimPrefix(method, secretMethodPrefix)
		switch {
		case secretWriteMethods[short]:
			return resourceSecret, model.ModeWrite, true
		case secretReadMethods[short]:
			return resourceSecret, model.ModeRead, true
		default:
			return resourceSecret, model.ModeUnknown, true
		}
	default:
		return "", model.ModeUnknown, false
	}
}

// project parses the GCP project id out of a resourceName
// (projects/<project>/...). ok=false if absent.
func project(resourceName string) (string, bool) {
	const seg = "projects/"
	i := strings.Index(resourceName, seg)
	if i < 0 {
		return "", false
	}
	rest := resourceName[i+len(seg):]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	if rest == "" {
		return "", false
	}
	return rest, true
}

// store identifies a Cloud KMS or Secret Manager custodian in one project for the
// inventory.
type store struct {
	service string // "cloudkms" | "secretmanager"
	project string
}

func (s store) ref() string { return "gcp." + s.service + ":" + s.project }

func (s store) displayName() string {
	if s.service == "cloudkms" {
		return "Google Cloud KMS (" + s.project + ")"
	}
	return "Google Secret Manager (" + s.project + ")"
}

// storeFor derives the custodian for a recognized entry, or ok=false.
func storeFor(e entry) (store, bool) {
	proj, ok := project(e.ProtoPayload.ResourceName)
	if !ok {
		return store{}, false
	}
	switch e.ProtoPayload.ServiceName {
	case kmsServiceName:
		return store{"cloudkms", proj}, true
	case secretServiceName:
		return store{"secretmanager", proj}, true
	default:
		return store{}, false
	}
}

// entriesFromBytes parses Cloud Logging entries from a file's bytes, accepting an
// {"entries":[…]} wrapper, a JSON array, NDJSON, or a single entry object.
func entriesFromBytes(data []byte) []entry {
	var wrap struct {
		Entries []entry `json:"entries"`
	}
	if err := json.Unmarshal(data, &wrap); err == nil && len(wrap.Entries) > 0 {
		return wrap.Entries
	}
	var arr []entry
	if err := json.Unmarshal(data, &arr); err == nil && len(arr) > 0 {
		return arr
	}
	var recs []entry
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e entry
		if err := json.Unmarshal(line, &e); err == nil && e.ProtoPayload.ServiceName != "" {
			recs = append(recs, e)
		}
	}
	if len(recs) > 0 {
		return recs
	}
	var one entry
	if err := json.Unmarshal(data, &one); err == nil && one.ProtoPayload.ServiceName != "" {
		return []entry{one}
	}
	return nil
}

var gcpTimeLayouts = []string{time.RFC3339Nano, time.RFC3339}

func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range gcpTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
