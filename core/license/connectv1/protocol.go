// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package connectv1 is the client half of the connect-v1 protocol primitives: the canonical
// proof-of-possession (PoP) message, the strict JSON reader, PoP key identity and the closed
// vocabulary of routes, operations and error codes.
//
// The byte encodings are shared with the license Worker's TypeScript implementation
// (commercial/license-worker/src/connect/{canonical,keys,errors}.ts, protocol commit
// 16b41ca56de959528557076b92731317872fb6c1) and are proven by cross-language vectors in testdata.
// This package performs no I/O: it never opens a connection, reads a file or fetches a key.
//
// A PoP key is a deployment identity key. It is a different trust domain from the license
// signing keys in core/license and from the OTA release keys in core/release; the KID derivation
// is the same formula, and nothing here makes one key usable in another domain.
package connectv1

import (
	"fmt"
	"regexp"
)

// Protocol negotiation and headers (errors.ts).
const (
	Protocol        = "connect-v1"
	Domain          = "olivares.connect.v1"
	HeaderProtocol  = "Olivares-Connect-Protocol"
	HeaderChallenge = "Olivares-Connect-Challenge"
	HeaderProof     = "Olivares-Connect-Proof"
	// HeaderNewKeyProof carries the proposed key's signature over the SAME canonical message as
	// HeaderProof during a normal rotation (Root direction r115-connect-bc-coordination item 4).
	HeaderNewKeyProof    = "Olivares-Connect-New-Key-Proof"
	HeaderKey            = "Olivares-Connect-Key"
	HeaderError          = "Olivares-Connect-Error"
	HeaderIdempotencyKey = "Idempotency-Key"
	// BodyMax is the Worker's request body bound.
	BodyMax = 8192
	// LabelMax bounds the optional deployment label.
	LabelMax = 64
	// ChallengeTTLSeconds is the Worker's challenge lifetime.
	ChallengeTTLSeconds = 120
)

// Routes.
const (
	PathChallenges  = "/connect/challenges"
	PathDeployments = "/connect/deployments"
	PathRefresh     = "/connect/refresh"
)

// Operation names a challenge is bound to (types.ts ConnectOperationName).
const (
	OpBindPending = "bind_pending"
	OpBind        = "bind"
	OpRefresh     = "refresh"
	OpRotateKey   = "rotate-key"
	OpDelete      = "delete"
	OpRecover     = "recover"
	OpReactivate  = "reactivate"
)

// Bind request operations carried in a pending bind body (`operation`).
const (
	BindOperationBind       = "bind"
	BindOperationRecover    = "recover"
	BindOperationReactivate = "reactivate"
)

var deploymentIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// ValidDeploymentID reports whether id may be placed in a route path. The Worker assigns ids;
// the client refuses one that would change the path's shape.
func ValidDeploymentID(id string) bool { return deploymentIDPattern.MatchString(id) }

// RotateKeyPath is the rotate-key route of a deployment.
func RotateKeyPath(deploymentID string) (string, error) {
	if !ValidDeploymentID(deploymentID) {
		return "", fmt.Errorf("connectv1: deployment id is not a route-safe identifier")
	}
	return PathDeployments + "/" + deploymentID + "/rotate-key", nil
}

// DeploymentPath is the deletion route of a deployment.
func DeploymentPath(deploymentID string) (string, error) {
	if !ValidDeploymentID(deploymentID) {
		return "", fmt.Errorf("connectv1: deployment id is not a route-safe identifier")
	}
	return PathDeployments + "/" + deploymentID, nil
}

// IntendedRoute is the method and path a challenge for operation and target is bound to, so a
// client can refuse a challenge that names another route. Recover and reactivate complete on the
// rotate-key route of their target deployment (Root direction r115-connect-bc-coordination item 2;
// at protocol commit 16b they mapped to the deployments route, which could not complete).
func IntendedRoute(operation, target string) (method, path string, err error) {
	switch operation {
	case OpBindPending, OpBind:
		return "POST", PathDeployments, nil
	case OpRefresh:
		return "POST", PathRefresh, nil
	case OpRotateKey, OpRecover, OpReactivate:
		p, err := RotateKeyPath(target)
		return "POST", p, err
	case OpDelete:
		p, err := DeploymentPath(target)
		return "DELETE", p, err
	}
	return "", "", fmt.Errorf("connectv1: unknown operation %q", operation)
}

// ErrorCode is the closed connect-v1 refusal vocabulary (errors.ts ConnectErrorCode).
type ErrorCode string

// The refusal codes.
const (
	ErrApprovalRequired          ErrorCode = "approval_required"
	ErrRecoveryRequired          ErrorCode = "recovery_required"
	ErrCredentialReissueRequired ErrorCode = "credential_reissue_required"
	ErrOrderFormPolicyRequired   ErrorCode = "order_form_policy_required"
	ErrProofRequired             ErrorCode = "proof_required"
	ErrProofInvalid              ErrorCode = "proof_invalid"
	ErrAuthorityDenied           ErrorCode = "authority_denied"
	ErrBindingDenied             ErrorCode = "binding_denied"
	ErrLicenseDenied             ErrorCode = "license_denied"
	ErrOperationConflict         ErrorCode = "operation_conflict"
	ErrGenerationStale           ErrorCode = "generation_stale"
	ErrQuotaExhausted            ErrorCode = "quota_exhausted"
	ErrBodyInvalid               ErrorCode = "body_invalid"
	ErrProtocolInvalid           ErrorCode = "protocol_invalid"
	ErrAuthorityUnavailable      ErrorCode = "authority_unavailable"
	ErrProviderAdapterMissing    ErrorCode = "provider_adapter_missing"
	ErrTrustUnavailable          ErrorCode = "trust_unavailable"
)

// ErrorCodeUnknown is the display label for a code outside the vocabulary.
const ErrorCodeUnknown ErrorCode = "unknown"

var knownCodes = map[ErrorCode]string{
	ErrApprovalRequired:          "the owner must approve this request in the customer portal, then retry",
	ErrRecoveryRequired:          "the owner must approve recovery of this deployment in the customer portal, then retry",
	ErrCredentialReissueRequired: "refresh with proof of possession to obtain the current credential",
	ErrOrderFormPolicyRequired:   "additional Enterprise deployments need a policy from an authorized order-form operation",
	ErrProofRequired:             "the request carried no proof of possession",
	ErrProofInvalid:              "the proof did not verify for this origin, route and body; retry with a new challenge",
	ErrAuthorityDenied:           "the commercial authority named is not current for this provider, business and holder",
	ErrBindingDenied:             "this key or deployment is not the current bound identity",
	ErrLicenseDenied:             "the signed-in owner must select a license they own for this holder",
	ErrOperationConflict:         "this operation id was used with a different body; start a new operation",
	ErrGenerationStale:           "the commercial authority changed; start a new operation",
	ErrQuotaExhausted:            "this contract has no free deployment slot; the owner chooses which slot to keep",
	ErrBodyInvalid:               "the request body was refused",
	ErrProtocolInvalid:           "the service did not accept the connect-v1 request",
	ErrAuthorityUnavailable:      "the commercial authority is not available; retry later or ask the owner to approve recovery",
	ErrProviderAdapterMissing:    "this commerce provider has no connect-v1 authority adapter",
	ErrTrustUnavailable:          "the service is missing its license signing or download configuration",
}

// Known reports whether c is in the vocabulary.
func (c ErrorCode) Known() bool {
	_, ok := knownCodes[c]
	return ok
}

// Display returns c when known and ErrorCodeUnknown otherwise, so a response header is never
// echoed verbatim.
func (c ErrorCode) Display() ErrorCode {
	if c.Known() {
		return c
	}
	return ErrorCodeUnknown
}

// Action is the client's own next-step text for a known code. It never repeats server text.
func (c ErrorCode) Action() string {
	if a, ok := knownCodes[c]; ok {
		return a
	}
	return "the service refused the request with an unrecognized code"
}
