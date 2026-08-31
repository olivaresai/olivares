// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package s3cloudtrail

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk/model"
)

// s3EventSource is the CloudTrail eventSource for Amazon S3. Other sources are
// out of scope here (cloud discovery is).
const s3EventSource = "s3.amazonaws.com"

// record is the subset of a CloudTrail event the connector reads.
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
	Type     string `json:"type"`
	ARN      string `json:"arn"`
	UserName string `json:"userName"`
}

type requestParameters struct {
	BucketName string `json:"bucketName"`
	Key        string `json:"key"`
	// ModelID is the Bedrock InvokeModel/Converse target model id (CLA-11). It is the
	// inference-profile / model id the call invoked, e.g. "us.anthropic.claude-opus-4-8"
	// (legacy CRIS) or "anthropic.claude-opus-4-8" (Mantle); the prefix encodes the
	// surface (see bedrock.go).
	ModelID string `json:"modelId"`
}

type resourceRef struct {
	Type string `json:"type"`
	ARN  string `json:"ARN"`
}

// recordsFromBytes extracts CloudTrail records from a file's bytes, accepting the
// three real shapes: a {"Records":[…]} wrapper, newline-delimited records, or a
// single record object.
func recordsFromBytes(data []byte) []record {
	var wrap struct {
		Records []record `json:"Records"`
	}
	if err := json.Unmarshal(data, &wrap); err == nil && len(wrap.Records) > 0 {
		return wrap.Records
	}

	// Newline-delimited records.
	var recs []record
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // tolerate long lines
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

	// A single (possibly pretty-printed) record object.
	var one record
	if err := json.Unmarshal(data, &one); err == nil && one.EventSource != "" {
		return []record{one}
	}
	return nil
}

// classifyMode maps CloudTrail's readOnly flag to a mode, verbatim. A missing
// flag yields unknown rather than a guess (ARCHITECTURE.md).
func classifyMode(readOnly *bool) model.AccessMode {
	switch {
	case readOnly == nil:
		return model.ModeUnknown
	case *readOnly:
		return model.ModeRead
	default:
		return model.ModeWrite
	}
}

// resolveResource returns the S3 resource kind and reference for a record,
// preferring the most specific resources[] ARN (object over bucket) and falling
// back to requestParameters. ok=false if no S3 resource can be determined.
func resolveResource(r record) (kind, ref string, ok bool) {
	var bucketARN string
	for _, res := range r.Resources {
		switch {
		case strings.HasSuffix(res.Type, "::Object") && res.ARN != "":
			return "s3.object", res.ARN, true
		case strings.HasSuffix(res.Type, "::Bucket") && res.ARN != "":
			bucketARN = res.ARN
		}
	}
	if bucketARN != "" {
		return "s3.bucket", bucketARN, true
	}
	b := r.RequestParameters.BucketName
	if b == "" {
		return "", "", false
	}
	if k := r.RequestParameters.Key; k != "" {
		return "s3.object", "arn:aws:s3:::" + b + "/" + k, true
	}
	return "s3.bucket", "arn:aws:s3:::" + b, true
}

// resolveIdentity returns the raw IAM identity the call is attributed to and the
// attribution confidence (docs/contracts). The raw principal is always
// emitted; confidence drops to approximate for a shared assumed-role, an AWS
// service principal, or an account/anonymous identity. ok=false if no identity
// reference is present at all.
func (s *Source) resolveIdentity(ui userIdentity) (ref string, conf model.Confidence, ok bool) {
	switch ui.Type {
	case "AWSService":
		if ui.InvokedBy == "" {
			return "", "", false
		}
		// A service acting on S3 is not a per-agent identity.
		return ui.InvokedBy, model.ConfidenceApproximate, true

	case "AssumedRole":
		ref = firstNonEmpty(ui.ARN, ui.PrincipalID)
		if ref == "" {
			return "", "", false
		}
		// The session ARN is the most specific raw reference (OriginRef); the
		// confidence is decided solely by the DURABLE role (sessionIssuer ARN),
		// which is what a shared-role declaration names — the session ARN is
		// ephemeral and never meaningfully declared shared (docs/contracts).
		return ref, s.shared.ConfidenceFor(ui.SessionContext.SessionIssuer.ARN), true

	case "IAMUser", "Root", "FederatedUser", "Directory", "IdentityCenterUser":
		ref = firstNonEmpty(ui.ARN, ui.PrincipalID)
		if ref == "" {
			return "", "", false
		}
		return ref, s.shared.ConfidenceFor(ref), true

	default:
		// AWSAccount, Unknown, empty, or any unrecognized type: a present
		// reference, but attribution to an agent is ambiguous.
		ref = firstNonEmpty(ui.ARN, ui.PrincipalID, ui.AccountID)
		if ref == "" {
			return "", "", false
		}
		return ref, model.ConfidenceApproximate, true
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// originKind is the EdgeObservation origin kind (always "identity").
const originKind = identity.OriginKind

// ctTimeLayouts are the timestamp formats CloudTrail emits in eventTime.
var ctTimeLayouts = []string{
	time.RFC3339,
	time.RFC3339Nano,
}

func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range ctTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
