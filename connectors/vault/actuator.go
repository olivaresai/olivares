// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// NHI lifecycle actuator for HashiCorp Vault. Actuator is the
// WRITE-capable arm beside the read-only Source, following the claude-wif
// Exchanger precedent: a SEPARATE, explicitly-wired type with its own minimal
// HTTP client code. It is never part of the Gather plane — the Source's
// read-only guarantee stays true by construction (connectors/internal/httpx is
// GET-only and this file does not touch it). The composition root builds an
// Actuator only when the operator opts in with a write-capable token, and the
// consumer (modules/governance) invokes it only behind a granted HITL approval.
//
// Verified Vault API surface (developer.hashicorp.com/vault/api-docs,
// 2026-06-10) — exactly these endpoints, nothing from memory:
//
//   - POST /v1/identity/entity/id/{id} {"disabled": true|false} — disable or
//     re-enable an identity entity. Doc quote: "Disabled entities' associated
//     tokens cannot be used, but are not revoked." The update endpoint accepts
//     other fields (name/metadata/policies); sending ONLY the disabled flag is
//     the correct minimal write.
//   - LIST /v1/auth/approle/role/{role}/secret-id — accessors of the role's
//     live secret-ids (data.keys).
//   - POST /v1/auth/approle/role/{role}/secret-id {} — mint a new secret-id
//     (data.secret_id is SECRET, data.secret_id_accessor is its non-secret ref).
//   - POST /v1/auth/approle/role/{role}/secret-id-accessor/destroy
//     {"secret_id_accessor": ...} — destroy one secret-id by accessor.
//
// Vault has NO entity-wide token revoke and entity DELETE does not revoke
// tokens either, so the actuator never fabricates a destructive finalize:
// Finalize honestly re-applies disabled=true and the receipt says so.

package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
)

// kindEntity is the roster Identity.Kind the Source stamps on Vault identity
// entities in Snapshot; the capability matrix keys on the same string.
const kindEntity = "vault_entity"

// targetRefPrefix is the operator-declared actuation-target form for the
// AppRole credential ops: "approle:<role_name>". The product never guesses the
// role behind an entity — the operator declares it and the consumer persists it.
const targetRefPrefix = "approle:"

// maxActuatorBody caps every response read; maxErrExcerpt bounds how much of a
// failed response may ride an error string (never a request body, never the
// token).
const (
	maxActuatorBody = 1 << 20
	maxErrExcerpt   = 256
)

// Honest per-op semantics, declared in Capabilities and surfaced verbatim to
// the operator on receipts. The caveats are the point: a consumer that hides
// "NOT revoked" turns quarantine into a false sense of safety.
const (
	detailDisable  = "disables the Vault identity entity; existing tokens are blocked from use but NOT revoked"
	detailRestore  = "re-enables the Vault identity entity"
	detailFinalize = "re-applies disabled=true as the definitive offboarding step; Vault offers no safe destructive primitive (entity delete does not revoke tokens), so definitive offboarding keeps the entity disabled"
	detailRotate   = `mints a new AppRole secret-id for the declared role (target ref "approle:<role_name>"); the old secret-id stays active until retired`
	detailRetire   = "destroys previously issued AppRole secret-ids by accessor"
)

// errNotConfigured is the fail-safe answer of an actuator wired without a
// token. The composition root should never build one — but if it does, every
// op refuses before any request is emitted.
var errNotConfigured = errors.New("vault: actuator not configured (empty token); refusing to actuate")

// Doer is the minimal HTTP capability the actuator needs (the Exchanger
// precedent). *http.Client satisfies it; tests inject a stub.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Actuator performs governed NHI lifecycle writes against Vault: entity
// disable/restore/finalize and AppRole secret-id rotate/retire. It holds the
// write-capable token in memory only, applies it per request via X-Vault-Token,
// and never logs or persists it. All errors are non-sensitive: no token, no
// secret-id, no request body ever rides an error string.
//
// Ref semantics: the entity ops address Vault's update-by-id endpoint, so
// req.Ref must be the Vault entity ID (the canonical UUID). Note the Source's
// roster stamps Identity.Ref as "entity:<name>", NOT the id — the consumer
// resolves and supplies the entity id when wiring the actuation (an unknown id
// fails loudly at Vault; nothing is guessed).
type Actuator struct {
	baseURL   string
	token     string
	namespace string
	timeout   time.Duration

	doer Doer             // injected transport (tests); never nil after NewActuator
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Actuator satisfies the lifecycle contract.
var _ identitysource.LifecycleActuator = (*Actuator)(nil)

// NewActuator builds a Vault lifecycle actuator. An empty baseURL uses the
// Source's default; a nil doer uses the default HTTP client; namespace, when
// set, is sent as the non-secret X-Vault-Namespace routing header. An empty
// token leaves the actuator unconfigured: every op fails safe (see
// errNotConfigured) instead of emitting an unauthenticated write.
func NewActuator(baseURL, token, namespace string, doer Doer) *Actuator {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if doer == nil {
		doer = &http.Client{}
	}
	return &Actuator{
		baseURL:   strings.TrimRight(baseURL, "/"),
		token:     token,
		namespace: namespace,
		timeout:   defaultTimeout,
		doer:      doer,
	}
}

// Capabilities declares, honestly, what this actuator can really do against
// Vault. The AppRole ops require an operator-declared target ref because the
// roster ref alone never identifies the role to act on. It is static per
// configuration and safe to call without a credential.
func (a *Actuator) Capabilities() []identitysource.ActuatorCapability {
	return []identitysource.ActuatorCapability{
		{Op: identitysource.OpDisable, TargetKind: kindEntity, Detail: detailDisable},
		{Op: identitysource.OpRestore, TargetKind: kindEntity, Detail: detailRestore},
		{Op: identitysource.OpFinalize, TargetKind: kindEntity, Detail: detailFinalize},
		{Op: identitysource.OpRotate, TargetKind: kindEntity, Detail: detailRotate, RequiresTargetRef: true},
		{Op: identitysource.OpRetire, TargetKind: kindEntity, Detail: detailRetire, RequiresTargetRef: true},
	}
}

// entityUpdate is the minimal entity write: ONLY the disabled flag. The update
// endpoint also accepts name/metadata/policies — deliberately not sent, so the
// actuation can never clobber entity attributes it was not approved to touch.
type entityUpdate struct {
	Disabled bool `json:"disabled"`
}

// Disable quarantines the entity (disabled=true). Honest caveat, verbatim from
// the Vault docs: the entity's existing tokens are blocked from use but NOT
// revoked — the receipt Detail carries that caveat to the operator.
func (a *Actuator) Disable(ctx context.Context, req identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	return a.setEntityDisabled(ctx, identitysource.OpDisable, req, true, detailDisable)
}

// Restore reverses Disable (disabled=false) within the recovery window.
func (a *Actuator) Restore(ctx context.Context, req identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	return a.setEntityDisabled(ctx, identitysource.OpRestore, req, false, detailRestore)
}

// Finalize is Vault's definitive offboarding step — which is, honestly,
// re-applying disabled=true. Entity delete is NOT safer (it does not revoke
// tokens and erases the audit anchor), so the entity is kept disabled and the
// receipt says exactly that.
func (a *Actuator) Finalize(ctx context.Context, req identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	return a.setEntityDisabled(ctx, identitysource.OpFinalize, req, true, detailFinalize)
}

// setEntityDisabled posts the minimal disabled-flag write shared by
// Disable/Restore/Finalize and builds the ledger-safe receipt.
func (a *Actuator) setEntityDisabled(ctx context.Context, op identitysource.LifecycleOp, req identitysource.ActuationRequest, disabled bool, detail string) (identitysource.ActuationReceipt, error) {
	if strings.TrimSpace(req.Ref) == "" {
		return identitysource.ActuationReceipt{}, fmt.Errorf("vault: actuator: %s: empty entity ref", op)
	}
	status, raw, err := a.doJSON(ctx, http.MethodPost, "/v1/identity/entity/id/"+url.PathEscape(req.Ref), entityUpdate{Disabled: disabled})
	if err != nil {
		return identitysource.ActuationReceipt{}, fmt.Errorf("vault: actuator: %s: %w", op, err)
	}
	if status < 200 || status >= 300 {
		return identitysource.ActuationReceipt{}, actuationError(op, status, raw)
	}
	return a.receipt(op, req.Ref, detail), nil
}

// secretIDResponse is the relevant slice of the secret-id mint reply. SecretID
// is SECRET: it is moved into RotatedCredential.Secret and appears nowhere else.
type secretIDResponse struct {
	Data struct {
		SecretID         string `json:"secret_id"`
		SecretIDAccessor string `json:"secret_id_accessor"`
		SecretIDTTL      int    `json:"secret_id_ttl"` // seconds; 0 = no expiry
	} `json:"data"`
}

// Rotate mints a new AppRole secret-id for the role declared in req.TargetRef
// ("approle:<role_name>") and returns it ONCE. It first lists the live
// accessors so the receipt carries the retirement work list (the old
// secret-ids stay active for cutover — Vault has no atomic swap; retire them
// with Retire after cutover). The secret rides only RotatedCredential.Secret;
// the receipt is ledger-safe.
func (a *Actuator) Rotate(ctx context.Context, req identitysource.ActuationRequest) (identitysource.RotatedCredential, error) {
	role, err := approleFromTargetRef(identitysource.OpRotate, req.TargetRef)
	if err != nil {
		return identitysource.RotatedCredential{}, err
	}
	basePath := "/v1/auth/approle/role/" + url.PathEscape(role) + "/secret-id"

	// (1) The pre-mint accessors become the receipt's retirement work list.
	old, err := a.listAccessors(ctx, basePath)
	if err != nil {
		return identitysource.RotatedCredential{}, fmt.Errorf("vault: actuator: rotate: list accessors: %w", err)
	}

	// (2) Mint. The empty JSON body is deliberate: no custom metadata/CIDR/TTL
	// overrides — the role's own configuration governs the credential.
	status, raw, err := a.doJSON(ctx, http.MethodPost, basePath, struct{}{})
	if err != nil {
		return identitysource.RotatedCredential{}, fmt.Errorf("vault: actuator: rotate: %w", err)
	}
	if status < 200 || status >= 300 {
		return identitysource.RotatedCredential{}, actuationError(identitysource.OpRotate, status, raw)
	}
	var out secretIDResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return identitysource.RotatedCredential{}, fmt.Errorf("vault: actuator: rotate: decode response: %w", err)
	}
	if out.Data.SecretID == "" || out.Data.SecretIDAccessor == "" {
		return identitysource.RotatedCredential{}, errors.New("vault: actuator: rotate: response carried no secret_id/accessor")
	}

	// (3) Return-once. ExpiresAt derives from secret_id_ttl when the role sets
	// one; zero means the secret-id does not expire on its own.
	var expires time.Time
	if out.Data.SecretIDTTL > 0 {
		expires = a.clock().UTC().Add(time.Duration(out.Data.SecretIDTTL) * time.Second)
	}
	return identitysource.RotatedCredential{
		Receipt: identitysource.RotationReceipt{
			ActuationReceipt:  a.receipt(identitysource.OpRotate, req.Ref, fmt.Sprintf("minted a new AppRole secret-id for role %q; the old secret-ids stay active until retired", role)),
			NewCredentialRef:  out.Data.SecretIDAccessor,
			OldCredentialRefs: old,
			ExpiresAt:         expires,
		},
		Secret: out.Data.SecretID,
	}, nil
}

// destroyRequest names the secret-id to destroy by its non-secret accessor.
type destroyRequest struct {
	SecretIDAccessor string `json:"secret_id_accessor"`
}

// Retire destroys the secret-ids named by req.CredentialRefs (accessors from a
// prior RotationReceipt) under the role declared in req.TargetRef. Destruction
// is per accessor; on a partial failure the error names how many were destroyed
// and which accessor failed (accessors are non-secret) so the caller can retry
// the remainder.
func (a *Actuator) Retire(ctx context.Context, req identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	role, err := approleFromTargetRef(identitysource.OpRetire, req.TargetRef)
	if err != nil {
		return identitysource.ActuationReceipt{}, err
	}
	if len(req.CredentialRefs) == 0 {
		return identitysource.ActuationReceipt{}, errors.New("vault: actuator: retire: no credential refs (accessors) to destroy")
	}
	destroyPath := "/v1/auth/approle/role/" + url.PathEscape(role) + "/secret-id-accessor/destroy"

	for i, acc := range req.CredentialRefs {
		status, raw, err := a.doJSON(ctx, http.MethodPost, destroyPath, destroyRequest{SecretIDAccessor: acc})
		if err != nil {
			return identitysource.ActuationReceipt{}, fmt.Errorf("vault: actuator: retire: destroyed %d of %d; accessor %d (%s): %w", i, len(req.CredentialRefs), i+1, acc, err)
		}
		if status < 200 || status >= 300 {
			return identitysource.ActuationReceipt{}, fmt.Errorf("vault: actuator: retire: destroyed %d of %d; accessor %d (%s): %w", i, len(req.CredentialRefs), i+1, acc, actuationError(identitysource.OpRetire, status, raw))
		}
	}
	return a.receipt(identitysource.OpRetire, req.Ref, fmt.Sprintf("destroyed %d AppRole secret-id(s) of role %q by accessor", len(req.CredentialRefs), role)), nil
}

// listAccessors lists the role's live secret-id accessors. It uses the custom
// "LIST" HTTP method, like the Vault CLI does (Vault equally accepts GET with
// ?list=true; one form is enough and LIST matches `vault list` semantics). A
// 404 is the documented shape of "no secret-ids yet" and means an empty list,
// not an error — a fresh role must still be rotatable.
func (a *Actuator) listAccessors(ctx context.Context, basePath string) ([]string, error) {
	status, raw, err := a.doJSON(ctx, "LIST", basePath, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status < 200 || status >= 300 {
		return nil, actuationError(identitysource.OpRotate, status, raw)
	}
	var resp listResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode list response: %w", err)
	}
	return resp.Data.Keys, nil
}

// approleFromTargetRef validates and parses the "approle:<role_name>" target
// ref for the credential ops. An empty ref is the contract's
// ErrTargetRefRequired (the consumer's honest-degrade signal); any other shape
// is a hard reject — the product never guesses a role name.
func approleFromTargetRef(op identitysource.LifecycleOp, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("vault: actuator: %s: %w", op, identitysource.ErrTargetRefRequired)
	}
	role, ok := strings.CutPrefix(ref, targetRefPrefix)
	if !ok || strings.TrimSpace(role) == "" {
		return "", fmt.Errorf("vault: actuator: %s: malformed target ref %q (want %q<role_name>)", op, ref, targetRefPrefix)
	}
	return role, nil
}

// doJSON performs one authenticated Vault request and returns the status and
// the bounded response body. It is the single choke point for the fail-safe
// unconfigured check, the token header, the namespace header and the
// per-request timeout. Transport errors carry the method and path only — never
// the token and never a request body.
func (a *Actuator) doJSON(ctx context.Context, method, path string, body any) (int, []byte, error) {
	if a.token == "" {
		return 0, nil, errNotConfigured
	}
	if a.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}

	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("encode request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, rd)
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set(headerToken, a.token)
	if a.namespace != "" {
		req.Header.Set(headerNamespace, a.namespace)
	}
	req.Header.Set("accept", "application/json")
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}

	resp, err := a.doer.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxActuatorBody))
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: read response: %w", method, path, err)
	}
	return resp.StatusCode, raw, nil
}

// actuationError builds a non-sensitive error from a failed write: the op, the
// HTTP status and Vault's own error strings ({"errors":[...]}) when present,
// else a bounded excerpt of the response body. It never echoes the token or
// any request body (the only place secret material could ride).
func actuationError(op identitysource.LifecycleOp, status int, raw []byte) error {
	var parsed struct {
		Errors []string `json:"errors"`
	}
	_ = json.Unmarshal(raw, &parsed)
	detail := strings.Join(parsed.Errors, "; ")
	if detail == "" {
		detail = strings.TrimSpace(string(raw))
	}
	if len(detail) > maxErrExcerpt {
		detail = detail[:maxErrExcerpt] + "…"
	}
	return fmt.Errorf("vault: actuator: %s rejected: http %d: %s", op, status, detail)
}

// receipt builds the ledger-safe record of one completed actuation.
func (a *Actuator) receipt(op identitysource.LifecycleOp, ref, detail string) identitysource.ActuationReceipt {
	return identitysource.ActuationReceipt{
		Op:         op,
		Ref:        ref,
		Provider:   identitysource.SourceVault,
		Detail:     detail,
		OccurredAt: a.clock().UTC(),
	}
}

// clock returns the actuator's time source (injectable for tests).
func (a *Actuator) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}
