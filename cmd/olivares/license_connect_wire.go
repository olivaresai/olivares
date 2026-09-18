// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/license/connectv1"
)

// license_connect_wire.go is the connected client's transport and the ONLY place that knows the
// request and response shapes of connect-v1.
//
// SHAPES: B's published wire, an internal design note (not shipped)
// (SHA-256 fb5cc1e8…, relayed by Root as the currently implemented wire at B commit c04256a4), under
// the Root direction an internal design note (not shipped) It is
// not yet B's accepted final delivery; Root reports any bytes that change. Nothing outside this file
// names a body or response field.

// connectResponseMaxBytes bounds every response before it is parsed.
const connectResponseMaxBytes = 256 << 10

// defaultConnectEndpoint is the licensing service origin.
const defaultConnectEndpoint = defaultEnterpriseEndpoint

// connectEndpoint is one licensing service origin and the client that talks to it.
type connectEndpoint struct {
	origin string
	client *http.Client
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// newConnectEndpoint accepts an ORIGIN only. Plain http is accepted only for a loopback host,
// which is how owned local fixtures are reached; a remote service is https.
func newConnectEndpoint(raw string, timeout time.Duration) (*connectEndpoint, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("the connect endpoint must be an origin such as %s", defaultConnectEndpoint)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return nil, fmt.Errorf("the connect endpoint must use https (plain http is accepted only for a loopback test service)")
		}
	default:
		return nil, fmt.Errorf("the connect endpoint must use https")
	}
	// cli-transport-exempt: the connect-v1 licensing service, not the operator's control plane.
	// Its authority is the Ed25519 proof of possession over the challenge, route and exact body,
	// and the returned credential is verified offline against the data directory's license trust
	// before anything is replaced. cliTransport's control-plane credential must never reach this
	// host, so it is not attached; no redirect is followed, so no second signed request is sent.
	client := &http.Client{Timeout: timeout, CheckRedirect: refuseGatedRedirect}
	return &connectEndpoint{origin: u.Scheme + "://" + u.Host, client: client}, nil
}

// connectUnknownError means the request may or may not have taken effect. The pending operation
// is kept and the same operation is retried later.
type connectUnknownError struct {
	reason string
	err    error
}

func (e *connectUnknownError) Error() string {
	if e.err != nil {
		return "the connect outcome is unknown (" + e.reason + "): " + e.err.Error() + "; the pending operation is kept and a re-run retries the same operation"
	}
	return "the connect outcome is unknown (" + e.reason + "); the pending operation is kept and a re-run retries the same operation"
}

func (e *connectUnknownError) Unwrap() error { return e.err }

// connectRefusal is a typed connect-v1 refusal.
type connectRefusal struct {
	status int
	code   connectv1.ErrorCode
}

func (e *connectRefusal) Error() string {
	return fmt.Sprintf("the licensing service refused with %d (%s): %s", e.status, e.code.Display(), e.code.Action())
}

// definitive reports a refusal the same operation cannot overcome by repetition. A 401 is not
// one: a proof is bound to a one-use challenge that each attempt draws fresh, so a proof refusal
// (an expired challenge, a clock-skewed hop) must not discard a step whose earlier attempt may have
// committed.
func (e *connectRefusal) definitive() bool {
	return e.status >= 400 && e.status < 500 && e.status != http.StatusUnauthorized
}

func marshalConnectBody(v map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func (e *connectEndpoint) do(ctx context.Context, method, path string, headers map[string]string, body []byte) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, e.origin+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("build the connect request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "olivares-connect")
	req.Header.Set(connectv1.HeaderProtocol, connectv1.Protocol)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// NOT REPLAYABLE BY THE TRANSPORT. net/http treats a request carrying an Idempotency-Key
	// header as idempotent and silently re-sends it when a reused connection drops before an
	// answer. That re-send carries the same ONE-USE challenge, so a committed effect whose answer
	// was lost came back as a proof refusal — measured by this client's own lost-answer test.
	// Clearing GetBody makes the request non-replayable; the client repeats the operation itself,
	// with a fresh challenge, from its persisted step.
	req.GetBody = nil
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, nil, nil, &connectUnknownError{reason: "no answer was received", err: wrapTransportMethod(method, e.origin, err)}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, connectResponseMaxBytes+1))
	if err != nil {
		return 0, nil, nil, &connectUnknownError{reason: "the answer could not be read", err: wrapTransportMethod(method, e.origin, err)}
	}
	if len(data) > connectResponseMaxBytes {
		return 0, nil, nil, &connectUnknownError{reason: fmt.Sprintf("the answer exceeds %d bytes", connectResponseMaxBytes)}
	}
	return resp.StatusCode, resp.Header, data, nil
}

// checkConnectHeaders requires every answer to be a connect-v1 answer that forbids caching. A
// proxy or another service answering is not a refusal of the operation: the outcome is unknown.
func checkConnectHeaders(status int, hdr http.Header) error {
	if hdr.Get(connectv1.HeaderProtocol) != connectv1.Protocol {
		return &connectUnknownError{reason: fmt.Sprintf("HTTP %d without the %s: %s header, so another service answered", status, connectv1.HeaderProtocol, connectv1.Protocol)}
	}
	noStore := false
	for _, v := range hdr.Values("Cache-Control") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "no-store") {
				noStore = true
			}
		}
	}
	if !noStore {
		return &connectUnknownError{reason: fmt.Sprintf("HTTP %d without Cache-Control: no-store", status)}
	}
	return nil
}

func parseConnectRefusal(status int, hdr http.Header, body []byte) error {
	if status >= 200 && status < 300 {
		return &connectUnknownError{reason: fmt.Sprintf("unexpected success status %d", status)}
	}
	code := connectv1.ErrorCode(hdr.Get(connectv1.HeaderError))
	if obj, err := connectv1.ParseStrictObject(body, connectResponseMaxBytes); err == nil {
		if s, ok := obj["error"].(string); ok && connectv1.ErrorCode(s) != code {
			code = connectv1.ErrorCodeUnknown
		}
	}
	return &connectRefusal{status: status, code: code.Display()}
}

type connectChallenge struct {
	id, nonce, origin string
	exp               int64
}

var connectChallengeFields = []string{"challenge_id", "nonce", "expires_at", "origin", "method", "path", "domain"}

func (e *connectEndpoint) challenge(ctx context.Context, op *connectPendingOp, kid string) (connectChallenge, error) {
	req := map[string]any{
		"operation":       op.Operation,
		"target":          op.Target,
		"body_sha256":     op.BodySHA256,
		"idempotency_key": op.IdempotencyKey,
		"kid":             kid,
	}
	if op.BindingEpoch > 0 {
		req["binding_epoch"] = op.BindingEpoch
	}
	body, err := marshalConnectBody(req)
	if err != nil {
		return connectChallenge{}, err
	}
	status, hdr, data, err := e.do(ctx, http.MethodPost, connectv1.PathChallenges, nil, body)
	if err != nil {
		return connectChallenge{}, err
	}
	if err := checkConnectHeaders(status, hdr); err != nil {
		return connectChallenge{}, err
	}
	if status != http.StatusOK {
		return connectChallenge{}, parseConnectRefusal(status, hdr, data)
	}
	obj, err := connectv1.ParseStrictObject(data, connectResponseMaxBytes)
	if err == nil {
		err = connectv1.RequireExactFields(obj, connectChallengeFields, nil)
	}
	if err != nil {
		return connectChallenge{}, &connectUnknownError{reason: "the challenge answer is malformed", err: err}
	}
	var ch connectChallenge
	var expires, method, path, domain string
	for _, f := range []struct {
		k string
		p *string
		n int
	}{{"challenge_id", &ch.id, 128}, {"nonce", &ch.nonce, 256}, {"expires_at", &expires, 64}, {"origin", &ch.origin, 256},
		{"method", &method, 8}, {"path", &path, 256}, {"domain", &domain, 64}} {
		if *f.p, err = connectv1.String(obj, f.k, f.n); err != nil {
			return connectChallenge{}, &connectUnknownError{reason: "the challenge answer is malformed", err: err}
		}
	}
	// The client signs the origin, route and domain it is actually addressing. A challenge bound
	// to anything else is not signed.
	if ch.origin != e.origin || method != op.Method || path != op.Path || domain != connectv1.Domain {
		return connectChallenge{}, &connectUnknownError{reason: "the challenge is bound to another origin, route or domain; nothing was signed"}
	}
	t, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return connectChallenge{}, &connectUnknownError{reason: "the challenge expiry is not an RFC3339 instant", err: err}
	}
	ch.exp = t.Unix()
	return ch, nil
}

// send performs ONE attempt of op: a fresh challenge, the proof(s) over the canonical message and
// the exact persisted body. A 200/202 returns the strict response object.
func (e *connectEndpoint) send(ctx context.Context, op *connectPendingOp, signer connectIdentity, next *connectIdentity) (int, map[string]any, error) {
	body, err := op.bodyBytes()
	if err != nil {
		return 0, nil, err
	}
	ch, err := e.challenge(ctx, op, signer.KID)
	if err != nil {
		return 0, nil, err
	}
	msg := connectv1.PopMessage{
		BindingEpoch: op.BindingEpoch, BodySHA256: op.BodySHA256, Challenge: ch.nonce, Exp: ch.exp,
		IdempotencyKey: op.IdempotencyKey, KID: signer.KID, Method: op.Method, Operation: op.Operation,
		Origin: ch.origin, Path: op.Path, Target: op.Target,
	}
	proof, err := connectv1.Sign(signer.Private, msg)
	if err != nil {
		return 0, nil, err
	}
	headers := map[string]string{
		connectv1.HeaderIdempotencyKey: op.IdempotencyKey,
		connectv1.HeaderChallenge:      ch.id,
		connectv1.HeaderProof:          proof,
	}
	if op.NewKeyProof {
		if next == nil {
			return 0, nil, fmt.Errorf("%w: the rotation needs the proposed identity", errConnectStateUnsafe)
		}
		np, err := connectv1.Sign(next.Private, msg)
		if err != nil {
			return 0, nil, err
		}
		headers[connectv1.HeaderNewKeyProof] = np
	}
	status, hdr, data, err := e.do(ctx, op.Method, op.Path, headers, body)
	if err != nil {
		return 0, nil, err
	}
	if err := checkConnectHeaders(status, hdr); err != nil {
		return status, nil, err
	}
	if status != http.StatusOK && status != http.StatusAccepted {
		return status, nil, parseConnectRefusal(status, hdr, data)
	}
	obj, err := connectv1.ParseStrictObject(data, connectResponseMaxBytes)
	if err != nil {
		return status, nil, &connectUnknownError{reason: "the answer is not a strict JSON object", err: err}
	}
	return status, obj, nil
}

// ---- request bodies (provisional, see the file header) -----------------------------------

type connectRequestInput struct {
	provider, businessID, holderID, licenseID, purpose, parent, label string
	publicKey, evidence                                               string
	// operation is bind, recover, reactivate or delete; targetDeployment names the deployment for
	// every operation but bind.
	operation, targetDeployment string
}

func connectRequestBody(in connectRequestInput) ([]byte, error) {
	b := map[string]any{
		"provider":    in.provider,
		"business_id": in.businessID,
		"holder_id":   in.holderID,
		"license_id":  in.licenseID,
		"purpose":     in.purpose,
		"public_key":  in.publicKey,
		"evidence":    in.evidence,
		"label":       in.label,
	}
	if in.parent != "" {
		b["parent"] = in.parent
	}
	if in.operation != "" && in.operation != connectv1.BindOperationBind {
		b["operation"] = in.operation
		b["target_deployment_id"] = in.targetDeployment
	}
	return marshalConnectBody(b)
}

// connectCompleteBindBody is exactly the four published fields; the label travels in the request.
func connectCompleteBindBody(requestID, approvalID, publicKey, channel string) ([]byte, error) {
	return marshalConnectBody(map[string]any{"request_id": requestID, "approval_id": approvalID, "public_key": publicKey, "channel": channel})
}

func connectRefreshBody(deploymentID, channel, publicKey string) ([]byte, error) {
	return marshalConnectBody(map[string]any{"deployment_id": deploymentID, "channel": channel, "public_key": publicKey})
}

func connectRotateBody(deploymentID, newPublicKey, channel, oldPublicKey string) ([]byte, error) {
	return marshalConnectBody(map[string]any{"deployment_id": deploymentID, "operation": connectv1.OpRotateKey, "new_public_key": newPublicKey, "channel": channel, "public_key": oldPublicKey})
}

// connectApprovedRotateBody completes recover or reactivate on the rotate-key route.
func connectApprovedRotateBody(operation, deploymentID, newPublicKey, channel, approvalID string) ([]byte, error) {
	return marshalConnectBody(map[string]any{"operation": operation, "deployment_id": deploymentID, "new_public_key": newPublicKey, "channel": channel, "approval_id": approvalID})
}

func connectDeleteBody(deploymentID, publicKey, approvalID string) ([]byte, error) {
	b := map[string]any{"deployment_id": deploymentID, "public_key": publicKey}
	if approvalID != "" {
		b["approval_id"] = approvalID
	}
	return marshalConnectBody(b)
}

// ---- responses (provisional, see the file header) ----------------------------------------

type connectRequestAnswer struct {
	status      string // pending | approved
	requestID   string
	approvalURL string
	approvalID  string // never printed
}

// connectRequestExpect is what the client itself asked for: the answer must describe that request.
type connectRequestExpect struct {
	operation   string // bind, recover, reactivate or delete
	target      string // "" for bind
	fingerprint string // of the key that signed the request
}

func (e *connectEndpoint) parseRequestAnswer(httpStatus int, obj map[string]any, want connectRequestExpect) (connectRequestAnswer, error) {
	malformed := func(err error) (connectRequestAnswer, error) {
		return connectRequestAnswer{}, &connectUnknownError{reason: "the request answer is malformed", err: err}
	}
	if err := connectv1.RequireExactFields(obj, []string{"status", "request_id"},
		[]string{"approval_url", "approval_id", "operation", "target_deployment_id", "pop_fingerprint"}); err != nil {
		return malformed(err)
	}
	// The answer must describe THIS request: the owner approves what the page shows, so an answer
	// naming another operation, deployment or key is not the request this key made.
	if v, ok := obj["operation"]; ok {
		if s, _ := v.(string); s != want.operation {
			return malformed(errors.New("the answer names another operation"))
		}
	}
	if _, ok := obj["target_deployment_id"]; ok {
		s, _, err := connectv1.NullableString(obj, "target_deployment_id", 80)
		if err != nil || s != want.target {
			return malformed(errors.New("the answer names another target deployment"))
		}
	}
	if v, ok := obj["pop_fingerprint"]; ok {
		if s, _ := v.(string); s != want.fingerprint {
			return malformed(errors.New("the answer names another key fingerprint"))
		}
	}
	var a connectRequestAnswer
	var err error
	if a.status, err = connectv1.String(obj, "status", 16); err != nil {
		return malformed(err)
	}
	if a.requestID, err = connectv1.String(obj, "request_id", 128); err != nil {
		return malformed(err)
	}
	if u, ok := obj["approval_url"]; ok {
		s, _ := u.(string)
		if !e.ownApprovalURL(s) {
			return malformed(errors.New("approval_url is not a plain page of the connect origin"))
		}
		a.approvalURL = s
	}
	switch a.status {
	case "pending":
		if httpStatus != http.StatusAccepted || a.approvalURL == "" {
			return malformed(errors.New("a pending request is a 202 with an approval_url"))
		}
		for _, f := range []string{"operation", "target_deployment_id", "pop_fingerprint"} {
			if _, ok := obj[f]; !ok {
				return malformed(fmt.Errorf("a pending request names its %s", f))
			}
		}
		if _, ok := obj["approval_id"]; ok {
			return malformed(errors.New("a pending request carries no approval"))
		}
	case "approved":
		if a.approvalID, err = connectv1.String(obj, "approval_id", 128); err != nil {
			return malformed(err)
		}
	default:
		return malformed(fmt.Errorf("unknown request status"))
	}
	return a, nil
}

// ownApprovalURL accepts a page of the connect origin with no userinfo, query or fragment: the
// link a separate browser opens carries the request id and nothing secret.
func (e *connectEndpoint) ownApprovalURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" {
		return false
	}
	return u.Scheme+"://"+u.Host == e.origin
}

type connectCredentialAnswer struct {
	deploymentID string
	popKID       string
	bindingEpoch int64
	slot         *int64
	credential   string // never printed
	ota          string // never printed
	otaExp       int64
	version      string
	serial       string // credential_serial, cross-checked against the signed credential
	issueSeq     int64  // credential_issue_seq, likewise
	phase        string // base grant phase, likewise
	// credential_effective_until is validated and NOT kept: the refresh planning boundary is derived
	// from the signed grants (connectRefreshBoundary), and this unsigned summary is the base line's.
}

func parseCredentialAnswer(obj map[string]any) (connectCredentialAnswer, error) {
	malformed := func(err error) (connectCredentialAnswer, error) {
		return connectCredentialAnswer{}, &connectUnknownError{reason: "the credential answer is malformed", err: err}
	}
	if err := connectv1.RequireExactFields(obj,
		[]string{"deployment_id", "pop_kid", "binding_epoch", "credential", "ota", "exp", "version"},
		[]string{"slot", "purpose", "parent", "credential_serial", "credential_issue_seq", "phase", "credential_effective_until"}); err != nil {
		return malformed(err)
	}
	var err error
	if _, ok := obj["credential_serial"]; ok {
		if _, err = connectv1.String(obj, "credential_serial", 256); err != nil {
			return malformed(err)
		}
	}
	if _, ok := obj["credential_issue_seq"]; ok {
		if _, err = connectv1.Int(obj, "credential_issue_seq", 1); err != nil {
			return malformed(err)
		}
	}
	if _, ok := obj["phase"]; ok {
		if _, err = connectv1.String(obj, "phase", 32); err != nil {
			return malformed(err)
		}
	}
	if _, ok := obj["credential_effective_until"]; ok {
		s, serr := connectv1.String(obj, "credential_effective_until", 64)
		if serr != nil {
			return malformed(serr)
		}
		if _, err = time.Parse(time.RFC3339Nano, s); err != nil {
			return malformed(err)
		}
	}
	a := connectCredentialAnswer{}
	a.serial, _ = obj["credential_serial"].(string)
	a.issueSeq, _ = obj["credential_issue_seq"].(int64)
	a.phase, _ = obj["phase"].(string)
	if a.deploymentID, err = connectv1.String(obj, "deployment_id", 80); err != nil || !connectv1.ValidDeploymentID(a.deploymentID) {
		return malformed(errors.New("deployment_id is not a route-safe identifier"))
	}
	if a.popKID, err = connectv1.String(obj, "pop_kid", 80); err != nil {
		return malformed(err)
	}
	if a.bindingEpoch, err = connectv1.Int(obj, "binding_epoch", 1); err != nil {
		return malformed(err)
	}
	if a.credential, err = connectv1.String(obj, "credential", license64KiB); err != nil {
		return malformed(err)
	}
	if a.ota, err = connectv1.String(obj, "ota", 8192); err != nil {
		return malformed(err)
	}
	if a.otaExp, err = connectv1.Int(obj, "exp", 1); err != nil {
		return malformed(err)
	}
	if a.version, err = connectv1.String(obj, "version", 64); err != nil {
		return malformed(err)
	}
	if _, ok := obj["slot"]; ok {
		n, present, err := connectv1.NullableInt(obj, "slot", 1)
		if err != nil {
			return malformed(err)
		}
		if present {
			a.slot = &n
		}
	}
	return a, nil
}

const license64KiB = 64 << 10

func parseDeleteAnswer(obj map[string]any, deploymentID string) error {
	if err := connectv1.RequireExactFields(obj, []string{"deployment_id", "deleted"}, nil); err != nil {
		return &connectUnknownError{reason: "the deletion answer is malformed", err: err}
	}
	if d, _ := obj["deployment_id"].(string); d != deploymentID {
		return &connectUnknownError{reason: "the deletion answer names another deployment"}
	}
	if del, _ := obj["deleted"].(bool); !del {
		return &connectUnknownError{reason: "the deletion answer does not confirm the deletion"}
	}
	return nil
}
