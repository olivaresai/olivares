// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package awskms

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// The CloudTrail eventSource strings this connector recognizes. Any other source
// is ignored — KMS/Secrets-Manager only (other AWS audit is). Verified against
// docs.aws.amazon.com (kms cloudtrail / secretsmanager cloudtrail_log_entries).
const (
	kmsEventSource     = "kms.amazonaws.com"
	secretsEventSource = "secretsmanager.amazonaws.com"
)

// The resource kinds emitted for the two services.
const (
	resourceKMSKey = "aws.kms.key"
	resourceSecret = "aws.secret"
)

// resourceTypeKMSKey and resourceTypeSecret are the CloudTrail resources[].type
// strings for a KMS key and a Secrets Manager secret.
const (
	resourceTypeKMSKey = "AWS::KMS::Key"
	resourceTypeSecret = "AWS::SecretsManager::Secret"
)

// record is the subset of a CloudTrail event the connector reads. Deliberately
// ABSENT: requestParameters.Plaintext (KMS already omits it), the response of any
// cryptographic op, SecretString/SecretBinary (Secrets Manager omits them) — the
// connector emits the edge, never the material (docs/SECURITY-HARDENING.md).
type record struct {
	EventTime         string            `json:"eventTime"`
	EventSource       string            `json:"eventSource"`
	EventName         string            `json:"eventName"`
	ReadOnly          *bool             `json:"readOnly"`
	UserIdentity      userIdentity      `json:"userIdentity"`
	RequestParameters requestParameters `json:"requestParameters"`
	Resources         []resourceRef     `json:"resources"`
}

type userIdentity struct {
	Type           string         `json:"type"`
	PrincipalID    string         `json:"principalId"`
	ARN            string         `json:"arn"`
	AccountID      string         `json:"accountId"`
	UserName       string         `json:"userName"`
	InvokedBy      string         `json:"invokedBy"`
	SessionContext sessionContext `json:"sessionContext"`
}

type sessionContext struct {
	SessionIssuer sessionIssuer `json:"sessionIssuer"`
}

type sessionIssuer struct {
	ARN string `json:"arn"`
}

// requestParameters carries only the key/secret reference. keyId may be a key ARN,
// a bare key id, an mrk- multi-Region id or an alias; secretId may be a secret ARN
// or a friendly name (verified: docs.aws.amazon.com KMS concepts / GetSecretValue).
type requestParameters struct {
	KeyID    string `json:"keyId"`
	SecretID string `json:"secretId"`
}

type resourceRef struct {
	Type string `json:"type"`
	ARN  string `json:"ARN"`
}

// recordsFromBytes extracts CloudTrail records from a file's bytes, accepting the
// three real shapes: a {"Records":[…]} wrapper, newline-delimited records, or a
// single record object (the same envelope handling as s3-cloudtrail).
func recordsFromBytes(data []byte) []record {
	var wrap struct {
		Records []record `json:"Records"`
	}
	if err := json.Unmarshal(data, &wrap); err == nil && len(wrap.Records) > 0 {
		return wrap.Records
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
		if err := json.Unmarshal(line, &r); err == nil && r.EventSource != "" {
			recs = append(recs, r)
		}
	}
	if len(recs) > 0 {
		return recs
	}
	var one record
	if err := json.Unmarshal(data, &one); err == nil && one.EventSource != "" {
		return []record{one}
	}
	return nil
}

// secretsReadOps are the Secrets Manager operations that read but do not mutate a
// secret. The remaining secret-naming operations (CreateSecret/PutSecretValue/
// UpdateSecret/DeleteSecret/RotateSecret) are writes. Classification is by the
// platform's own verb, used ONLY when a record omits the readOnly flag — the flag
// wins when present (ARCHITECTURE.md, verbatim from the source).
var secretsReadOps = map[string]bool{
	"GetSecretValue":       true,
	"BatchGetSecretValue":  true,
	"DescribeSecret":       true,
	"ListSecrets":          true,
	"ListSecretVersionIds": true,
	"GetResourcePolicy":    true,
}

// classifyMode maps a record to an access mode. The record's own readOnly flag is
// authoritative when present (KMS always sets it: crypto ops true, management ops
// false). When absent (some Secrets Manager records), it falls back to the verb;
// an unclassifiable operation yields ModeUnknown — never guessed.
func classifyMode(r record) model.AccessMode {
	if r.ReadOnly != nil {
		if *r.ReadOnly {
			return model.ModeRead
		}
		return model.ModeWrite
	}
	if r.EventSource == secretsEventSource {
		if secretsReadOps[r.EventName] {
			return model.ModeRead
		}
		// Create/Put/Update/Delete/Rotate/Restore/Tag etc. mutate the secret.
		return model.ModeWrite
	}
	return model.ModeUnknown
}

// resolveResources returns the (kind, ref) resources a record touches. KMS records
// usually carry the key in resources[] (type AWS::KMS::Key) and in
// requestParameters.keyId; ReEncrypt names TWO keys (source then destination), so
// this returns a slice. Secrets Manager records carry the secret ARN in resources[]
// or the name/ARN in requestParameters.secretId. An operation that names no
// specific key/secret (GetRandomPassword, ListSecrets) yields none.
func resolveResources(r record) []resource {
	switch r.EventSource {
	case kmsEventSource:
		return resolveKMSResources(r)
	case secretsEventSource:
		return resolveSecretResources(r)
	default:
		return nil
	}
}

type resource struct {
	kind string
	ref  string
}

func resolveKMSResources(r record) []resource {
	var out []resource
	seen := map[string]bool{}
	for _, res := range r.Resources {
		if res.Type == resourceTypeKMSKey && res.ARN != "" && !seen[res.ARN] {
			seen[res.ARN] = true
			out = append(out, resource{resourceKMSKey, res.ARN})
		}
	}
	if len(out) > 0 {
		return out
	}
	// Fall back to requestParameters.keyId (ARN / bare id / mrk- / alias). An alias
	// is a legitimate, stable reference to a key for the purposes of the access map.
	if k := strings.TrimSpace(r.RequestParameters.KeyID); k != "" {
		return []resource{{resourceKMSKey, k}}
	}
	return nil
}

func resolveSecretResources(r record) []resource {
	for _, res := range r.Resources {
		if res.Type == resourceTypeSecret && res.ARN != "" {
			return []resource{{resourceSecret, res.ARN}}
		}
		// Some records carry only the ARN with no type set.
		if res.ARN != "" && strings.Contains(res.ARN, ":secretsmanager:") {
			return []resource{{resourceSecret, res.ARN}}
		}
	}
	if s := strings.TrimSpace(r.RequestParameters.SecretID); s != "" {
		return []resource{{resourceSecret, s}}
	}
	return nil
}

// store identifies a secret-manager custodian for the inventory: an
// AWS KMS or Secrets Manager scope in one account+region. It is derived from the
// ARNs seen in the export, never invented.
type store struct {
	service string // "kms" | "secretsmanager"
	account string
	region  string
}

// ref is the stable external id the roster converges on.
func (s store) ref() string { return "aws." + s.service + ":" + s.account + ":" + s.region }

func (s store) displayName() string {
	switch s.service {
	case "kms":
		return "AWS KMS (" + s.account + "/" + s.region + ")"
	default:
		return "AWS Secrets Manager (" + s.account + "/" + s.region + ")"
	}
}

// storeFromARN parses the account+region+service out of a KMS key ARN or a Secrets
// Manager secret ARN. ARN form: arn:partition:service:region:account:resource.
// ok=false for a non-ARN reference (a bare key id or alias has no account/region).
func storeFromARN(arn string) (store, bool) {
	if !strings.HasPrefix(arn, "arn:") {
		return store{}, false
	}
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 {
		return store{}, false
	}
	service := parts[2]
	if service != "kms" && service != "secretsmanager" {
		return store{}, false
	}
	return store{service: service, region: parts[3], account: parts[4]}, true
}

// awsTimeLayouts are the timestamp formats CloudTrail emits in eventTime.
var awsTimeLayouts = []string{time.RFC3339, time.RFC3339Nano}

func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range awsTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
