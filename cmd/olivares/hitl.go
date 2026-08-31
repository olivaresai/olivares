// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/slack"
	"github.com/olivaresai/olivares/connectors/teams"
	"github.com/olivaresai/olivares/connectors/webhook"
)

// loadHITLConfig reads the optional inbound-receiver provisioning file named by
// OLIVARES_HITL_CONFIG (a JSON hitlConfig). It is the operator's secret-bearing config,
// kept out of the store (the same pattern as OLIVARES_NOTIFY_CONFIG). A missing path
// yields an empty config (the receiver is simply not mounted); a supplied path must be
// readable and contain valid JSON or startup fails closed.
func loadHITLConfig(_ *slog.Logger) (hitlConfig, error) {
	path := os.Getenv("OLIVARES_HITL_CONFIG")
	if path == "" {
		return hitlConfig{}, nil
	}
	var cfg hitlConfig
	if err := loadOperatorJSONConfig("OLIVARES_HITL_CONFIG", path, &cfg); err != nil {
		return hitlConfig{}, err
	}
	return cfg, nil
}

// This file is the INBOUND HALF of the HITL round-trip: the hardened receiver that
// turns an approve/deny click in an ITSM/ChatOps tool into a governed decision against
// the approval engine. It is the composition-root HTTP bridge that wire.go names as
// the missing piece ("governance exposes no in-process Go approval API, only HTTP
// routes, so [the gates] stay deny-closed until a composition-root HTTP bridge ... is
// built"). It lives in the AGPL wiring, NEVER in a connector, because it consumes the
// engine's governed API; a connector may not import /core.
//
// Security posture (docs/SECURITY-HARDENING.md/§1/§4). The out side is notification; THIS side is a NEW
// ATTACK SURFACE, so every callback is verified FAIL-CLOSED before anything else
// happens: a Slack callback by Slack's v0 HMAC, a generic ITSM callback (ServiceNow
// Flow, JSM automation, a Teams Workflow) by the operator-configured X-Olivares HMAC
// (the only verifiable scheme those tools can emit — they have no native outbound
// signature; primary-source verified). A callback whose signature is
// invalid or whose timestamp is outside the replay window is rejected WITHOUT ever
// reaching the engine.
//
// It does NOT bypass the approval engine. After verifying the signature it maps the
// provider identity (the Slack user id, the ITSM actor ref) to a configured Olivares
// API token OWNED BY THE REAL HUMAN APPROVER, and records the decision by calling the
// governed, audited decision API in-process — through the full authenticate → tenant →
// authorize → handler → audit chain. Separation-of-duty, the duplicate-decider guard,
// the approval threshold and lazy expiry are ALL enforced by (they key on the
// token's stable Principal.UserID); this receiver opens no path around them. If
// declines (the requester tried to self-approve, the approval already expired, the user
// already decided), the receiver reflects that honestly.

// hitlApprover maps one external provider identity to the Olivares credential the
// receiver acts as. The token is an API token issued to the REAL human approver and
// scoped to governance:approval:admin in their tenant; because a token principal's
// UserID is its owner, SoD and the duplicate-decider guard key on the human, not on the
// receiver. The token is a secret (operator config, never the store, never logged).
type hitlApprover struct {
	ExternalID string `json:"external_id"`
	Tenant     string `json:"tenant"`
	Token      string `json:"token"`
}

// hitlProviderSpec provisions one inbound provider surface (mounted at /hitl/<name>).
type hitlProviderSpec struct {
	Name          string         `json:"name"`
	Kind          string         `json:"kind"` // "slack" | "webhook" | "teams"
	SigningSecret string         `json:"signing_secret"`
	ReplaySeconds int            `json:"replay_window_seconds"`
	Approvers     []hitlApprover `json:"approvers"`

	// Teams (Bot Framework) native JWT verification. When kind is
	// "teams", the inbound Action.Execute callback is authenticated by its native RS256
	// JWT instead of our HMAC: BotAppID is the bot's Microsoft App ID (the required token
	// audience); MetadataURL/Issuers override the public-cloud defaults for the emulator
	// or sovereign clouds. Approvers' external_id is the user's Entra aadObjectId. The
	// HMAC "webhook" kind stays the default when no bot is registered.
	BotAppID    string   `json:"bot_app_id,omitempty"`
	MetadataURL string   `json:"oidc_metadata_url,omitempty"`
	Issuers     []string `json:"issuers,omitempty"`
}

// hitlConfig is the operator's inbound-receiver provisioning (OLIVARES_HITL_CONFIG),
// mirroring OLIVARES_NOTIFY_CONFIG / OLIVARES_SOURCES_CONFIG: secrets by value, out of
// the store. With no config the receiver is simply not mounted (an honest absence).
type hitlConfig struct {
	// Listen is the receiver's own socket. Default 127.0.0.1:8455 (loopback secure-
	// default): a production deployment fronts it with the operator's ingress and
	// sets a reachable bind explicitly — exposure is intentional for this surface, and
	// its security is signature verification, not network isolation.
	Listen    string             `json:"listen"`
	Providers []hitlProviderSpec `json:"providers"`
}

// provider kinds.
const (
	hitlKindSlack   = "slack"
	hitlKindWebhook = "webhook"
	hitlKindTeams   = "teams"
)

// Generic-webhook signature headers (the scheme connectors/webhook emits and the
// operator's ITSM tool reproduces).
const (
	hdrOlivaresTimestamp = "X-Olivares-Timestamp"
	hdrOlivaresSignature = "X-Olivares-Signature"
)

// maxInboundBody bounds a callback body (a Block Kit payload / an ITSM JSON is small).
const maxInboundBody = 256 << 10

// defaultHITLListen is the loopback secure-default bind.
const defaultHITLListen = "127.0.0.1:8455"

// decisionResult is the outcome of resolving one decision against.
type decisionResult struct {
	Recorded   bool   // the engine processed it (HTTP 2xx)
	HTTPStatus int    // the engine's status code (0 if the call never completed)
	State      string // the resulting approval status on success (approved/rejected/pending)
	Message    string // a non-sensitive error message on a decline
}

// approvalDecider records a human decision against the governed approval API. The real
// implementation calls the in-process API handler; a test injects a spy to prove the
// engine is never reached on a failed signature.
type approvalDecider interface {
	Decide(ctx context.Context, tenant, token, approvalID, decision, note string) decisionResult
}

// hitlProvider is one verified inbound surface.
type hitlProvider struct {
	name      string
	kind      string
	secret    string
	window    time.Duration
	approvers map[string]hitlApprover // external_id -> approver
	teams     *teams.Verifier         // set for kind=="teams" (native Bot Framework JWT)
}

// hitlReceiver routes verified callbacks to the approval engine.
type hitlReceiver struct {
	providers map[string]*hitlProvider
	decider   approvalDecider
	clock     func() time.Time
	log       *slog.Logger
}

// newHITLReceiver builds a receiver from the operator config and the decider. A
// provider with an unknown kind or no signing secret is skipped with a warning (a
// visible misconfiguration, never a silently-open surface). It returns nil when no
// usable provider is configured.
func newHITLReceiver(cfg hitlConfig, decider approvalDecider, log *slog.Logger) *hitlReceiver {
	r := &hitlReceiver{
		providers: map[string]*hitlProvider{},
		decider:   decider,
		clock:     time.Now,
		log:       log,
	}
	for _, spec := range cfg.Providers {
		if spec.Name == "" {
			log.Warn("hitl: provider with no name; skipped")
			continue
		}
		p := &hitlProvider{name: spec.Name, kind: spec.Kind, window: webhook.DefaultReplayWindow}
		if spec.ReplaySeconds > 0 {
			p.window = time.Duration(spec.ReplaySeconds) * time.Second
		}

		// Per-kind authentication setup. Every surface MUST establish a fail-closed
		// verifier before it is mounted — there is no unauthenticated open door.
		switch spec.Kind {
		case hitlKindSlack, hitlKindWebhook:
			if spec.SigningSecret == "" {
				log.Warn("hitl: provider has no signing secret; skipped (cannot verify callbacks fail-closed)", "name", spec.Name)
				continue
			}
			p.secret = spec.SigningSecret
		case hitlKindTeams:
			// Native Bot Framework RS256 JWT. The bot App ID is the required token
			// audience — without it the verifier cannot bind the token to this bot.
			if spec.BotAppID == "" {
				log.Warn("hitl: teams provider has no bot_app_id; skipped (the App ID is the required JWT audience)", "name", spec.Name)
				continue
			}
			ver, err := teams.NewVerifier(teams.VerifierConfig{
				AppID: spec.BotAppID, MetadataURL: spec.MetadataURL, Issuers: spec.Issuers,
			})
			if err != nil {
				log.Warn("hitl: teams provider verifier could not be built; skipped", "name", spec.Name, "error", err)
				continue
			}
			p.teams = ver
		default:
			log.Warn("hitl: provider has unknown kind; skipped", "name", spec.Name, "kind", spec.Kind)
			continue
		}

		appr := make(map[string]hitlApprover, len(spec.Approvers))
		for _, a := range spec.Approvers {
			if a.ExternalID == "" || a.Tenant == "" || a.Token == "" {
				log.Warn("hitl: approver missing external_id/tenant/token; skipped", "provider", spec.Name)
				continue
			}
			appr[a.ExternalID] = a
		}
		p.approvers = appr
		if _, dup := r.providers[spec.Name]; dup {
			log.Warn("hitl: duplicate provider name; later definition ignored", "name", spec.Name)
			continue
		}
		r.providers[spec.Name] = p
	}
	if len(r.providers) == 0 {
		return nil
	}
	return r
}

// handler returns the receiver's HTTP handler: POST /hitl/{provider}. Every other
// method/path is rejected.
func (r *hitlReceiver) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hitl/{provider}", r.handle)
	return mux
}

// handle verifies the callback's signature fail-closed, maps the provider identity to
// an approver credential, and records the decision through the governed API.
func (r *hitlReceiver) handle(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("provider")
	p, ok := r.providers[name]
	if !ok {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, maxInboundBody))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	now := r.clock()

	// 1. FAIL-CLOSED signature verification BEFORE any processing (docs/SECURITY-HARDENING.md).
	if !p.verify(req, raw, now) {
		// A rejected callback never reaches the engine. The log line is non-sensitive.
		r.log.Warn("hitl: rejected callback (signature/replay)", "provider", p.name)
		http.Error(w, "signature verification failed", http.StatusUnauthorized)
		return
	}

	// 2. Parse the provider's decision payload.
	dec, perr := p.parse(raw)
	if perr != nil {
		http.Error(w, "malformed callback", http.StatusBadRequest)
		return
	}

	// 3. Map the provider identity to a real human approver's credential. An unmapped
	//    identity is rejected — the receiver never fabricates a principal.
	appr, ok := p.approvers[dec.externalID]
	if !ok {
		http.Error(w, "approver not provisioned", http.StatusForbidden)
		return
	}

	// 4. Normalize the decision and require an approval id.
	decision := normalizeDecision(dec.decision)
	if decision == "" || dec.approvalID == "" {
		http.Error(w, "missing or invalid decision/approval", http.StatusBadRequest)
		return
	}

	// 5. Record the decision through the GOVERNED API (authenticate → tenant → authorize
	//    → handler → audit), as the real human. Enforces SoD/dup/threshold/expiry.
	note := "decided via " + p.kind + " HITL callback (" + p.name + ")"
	res := r.decider.Decide(req.Context(), appr.Tenant, appr.Token, dec.approvalID, decision, note)
	r.log.Info("hitl: decision recorded via governed API",
		"provider", p.name, "approval", dec.approvalID, "decision", decision,
		"engine_status", res.HTTPStatus, "result", res.State)

	p.respond(w, res)
}

// verify checks the callback's authenticity fail-closed per the provider's scheme: Slack's
// native v0 HMAC, Teams' native Bot Framework RS256 JWT, or our X-Olivares HMAC for
// the generic webhook provider (the scheme an ITSM tool with no native outbound signature
// reproduces).
func (p *hitlProvider) verify(req *http.Request, raw []byte, now time.Time) bool {
	switch p.kind {
	case hitlKindSlack:
		return slack.VerifyRequest(
			p.secret,
			req.Header.Get(slack.HeaderTimestamp),
			req.Header.Get(slack.HeaderSignature),
			raw, now, p.window,
		)
	case hitlKindTeams:
		// The native Bot Framework JWT in the Authorization header binds the token to this
		// bot (aud), the trusted Connector issuer, RS256, exp/nbf and the activity's
		// serviceUrl. Any failure rejects the callback before the engine is reached.
		if p.teams == nil {
			return false
		}
		_, err := p.teams.Verify(req.Context(), req.Header.Get("Authorization"), raw, now)
		return err == nil
	default: // hitlKindWebhook
		ts := req.Header.Get(hdrOlivaresTimestamp)
		sig := req.Header.Get(hdrOlivaresSignature)
		if ts == "" {
			ts = webhook.SignatureTimestamp(sig)
		}
		return webhook.VerifyWithin(p.secret, ts, sig, raw, now, p.window)
	}
}

// parsedDecision is the provider-agnostic decision extracted from a callback.
type parsedDecision struct {
	externalID string
	approvalID string
	decision   string
}

// parse extracts the decision from the provider's payload shape.
func (p *hitlProvider) parse(raw []byte) (parsedDecision, error) {
	switch p.kind {
	case hitlKindSlack:
		return parseSlack(raw)
	case hitlKindTeams:
		return parseTeams(raw)
	default:
		return parseWebhook(raw)
	}
}

// teamsInvoke is the inbound Teams Action.Execute Invoke activity (VERIFIED shape). The
// approver identity is from.aadObjectId (the user's stable Entra object id) — NOT from.id,
// which is channel-scoped and altered per-bot; an absent aadObjectId fails closed (the
// receiver never falls back to an unstable id). The decision/approval id live in
// value.action.data, the payload WE define on the Action.Execute we render (reusing the
// {decision, approval_id} contract the webhook provider already speaks).
type teamsInvoke struct {
	Type string `json:"type"`
	Name string `json:"name"`
	From struct {
		ID          string `json:"id"`
		AadObjectID string `json:"aadObjectId"`
	} `json:"from"`
	Value struct {
		Action struct {
			Verb string          `json:"verb"`
			Data json.RawMessage `json:"data"`
		} `json:"action"`
	} `json:"value"`
}

// parseTeams decodes a Teams Action.Execute callback. It requires the universal-action
// envelope (type=invoke, name=adaptiveCard/action) and a present aadObjectId; the decision
// and approval id come from the action data.
func parseTeams(raw []byte) (parsedDecision, error) {
	var inv teamsInvoke
	if err := json.Unmarshal(raw, &inv); err != nil {
		return parsedDecision{}, err
	}
	if inv.Type != "invoke" || inv.Name != "adaptiveCard/action" {
		return parsedDecision{}, fmt.Errorf("hitl: teams payload is not an adaptiveCard/action invoke")
	}
	if inv.From.AadObjectID == "" {
		// Fail closed: without the stable Entra object id the approver cannot be mapped,
		// and from.id is not a safe substitute (channel-scoped, per-bot mutated).
		return parsedDecision{}, fmt.Errorf("hitl: teams activity carries no aadObjectId")
	}
	var data webhookCallback // {decision, approval_id} — the contract we author on the card
	if len(inv.Value.Action.Data) > 0 {
		if err := json.Unmarshal(inv.Value.Action.Data, &data); err != nil {
			return parsedDecision{}, err
		}
	}
	return parsedDecision{externalID: inv.From.AadObjectID, approvalID: data.ApprovalID, decision: data.Decision}, nil
}

// parseSlack decodes a Slack Block Kit interactivity payload. Slack POSTs
// application/x-www-form-urlencoded with a single "payload" field whose value is
// URL-encoded JSON. The clicked button's action_id carries the decision (approve/deny)
// and its value carries the approval id (or "decision:approvalID").
func parseSlack(raw []byte) (parsedDecision, error) {
	form, err := url.ParseQuery(string(raw))
	if err != nil {
		return parsedDecision{}, err
	}
	payload := form.Get("payload")
	if payload == "" {
		return parsedDecision{}, fmt.Errorf("hitl: slack payload field missing")
	}
	var bp struct {
		Type string `json:"type"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Actions []struct {
			ActionID string `json:"action_id"`
			Value    string `json:"value"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(payload), &bp); err != nil {
		return parsedDecision{}, err
	}
	if len(bp.Actions) == 0 {
		return parsedDecision{}, fmt.Errorf("hitl: slack payload has no actions")
	}
	a := bp.Actions[0]
	decision, approvalID := decisionAndID(a.ActionID, a.Value)
	return parsedDecision{externalID: bp.User.ID, approvalID: approvalID, decision: decision}, nil
}

// webhookCallback is the JSON shape the generic provider (ServiceNow Flow / JSM
// automation / Teams Workflow) sends — a contract WE define and the operator's tool
// signs with our HMAC, since those tools emit no native outbound signature.
type webhookCallback struct {
	ApprovalID string `json:"approval_id"`
	Decision   string `json:"decision"`
	ExternalID string `json:"external_id"`
}

func parseWebhook(raw []byte) (parsedDecision, error) {
	var c webhookCallback
	if err := json.Unmarshal(raw, &c); err != nil {
		return parsedDecision{}, err
	}
	return parsedDecision{externalID: c.ExternalID, approvalID: c.ApprovalID, decision: c.Decision}, nil
}

// decisionAndID derives the decision and approval id from a Slack button's action_id
// and value. The action_id names the decision (approve/deny, optionally prefixed); the
// value is the approval id, or "decision:approvalID" when the button packs both.
func decisionAndID(actionID, value string) (decision, approvalID string) {
	decision = actionID
	approvalID = value
	if i := strings.Index(value, ":"); i >= 0 {
		// value packs "decision:approvalID" — the value's decision wins (it is the
		// button's intent), the suffix is the id.
		decision = value[:i]
		approvalID = value[i+1:]
	}
	return decision, approvalID
}

// normalizeDecision maps the many ways a provider spells a decision onto the engine's
// two values ("approve"/"reject"), or "" for an unrecognized decision (rejected as a
// bad request — never guessed).
func normalizeDecision(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "olivares_")
	switch s {
	case "approve", "approved", "approval", "yes", "accept":
		return "approve"
	case "reject", "rejected", "deny", "denied", "decline", "declined", "no":
		return "reject"
	default:
		return ""
	}
}

// respond writes the provider-appropriate response. For Slack a business decline is NOT
// a transport failure (Slack retries non-2xx), so the receiver always 200-acks and
// records the outcome in the body; the decision and any decline are auditable.
// For the generic webhook provider the operator's tool wants the engine's status, so it
// is mirrored directly.
func (p *hitlProvider) respond(w http.ResponseWriter, res decisionResult) {
	switch p.kind {
	case hitlKindSlack:
		writeReceiverJSON(w, http.StatusOK, map[string]any{"ok": res.Recorded, "result": outcomeText(res)})
	case hitlKindTeams:
		// Teams Action.Execute REQUIRES HTTP 200 with an {statusCode, type, value} Invoke
		// response; like Slack it retries a non-2xx transport, so a business decline is a
		// 200-ack carrying the engine's sub-status, not an HTTP error.
		sc := res.HTTPStatus
		if sc == 0 {
			sc = http.StatusOK
		}
		writeReceiverJSON(w, http.StatusOK, map[string]any{
			"statusCode": sc,
			"type":       "application/vnd.microsoft.activity.message",
			"value":      outcomeText(res),
		})
	default: // hitlKindWebhook — the operator's tool wants the engine's status mirrored
		status := res.HTTPStatus
		if status == 0 {
			status = http.StatusBadGateway
		}
		writeReceiverJSON(w, status, map[string]any{"recorded": res.Recorded, "result": outcomeText(res), "message": res.Message})
	}
}

// outcomeText is a short non-sensitive description of the decision outcome.
func outcomeText(res decisionResult) string {
	if res.Recorded {
		if res.State != "" {
			return res.State
		}
		return "recorded"
	}
	if res.Message != "" {
		return "declined: " + res.Message
	}
	return "declined"
}

func writeReceiverJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// --- the real decider: the in-process governed API ---------------------------------

// apiDecider records decisions by calling the engine's own HTTP API IN-PROCESS — the
// full authenticate → tenant → authorize → handler → audit chain, with zero new code
// path. It is the "composition-root HTTP bridge" wire.go anticipated.
type apiDecider struct {
	handler http.Handler
}

// Decide POSTs {decision, note} to /v1/m/governance/approvals/{id}/decisions as the
// approver (their bearer token + tenant header), exactly as an external caller would,
// and parses the engine's response.
func (a apiDecider) Decide(ctx context.Context, tenant, token, approvalID, decision, note string) decisionResult {
	body, _ := json.Marshal(map[string]string{"decision": decision, "note": note})
	req, err := http.NewRequestWithContext(loopbackContext(ctx), http.MethodPost,
		"/v1/m/governance/approvals/"+url.PathEscape(approvalID)+"/decisions", bytes.NewReader(body))
	if err != nil {
		return decisionResult{Message: "internal request build error"}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Olivares-Tenant", tenant)
	req.Header.Set("Content-Type", "application/json")

	rec := &captureWriter{header: http.Header{}, status: http.StatusOK}
	a.handler.ServeHTTP(rec, req)

	res := decisionResult{HTTPStatus: rec.status, Recorded: rec.status >= 200 && rec.status < 300}
	if res.Recorded {
		var dto struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(rec.body.Bytes(), &dto)
		res.State = dto.Status
	} else {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(rec.body.Bytes(), &e)
		res.Message = e.Error.Message
	}
	return res
}

// captureWriter is a minimal http.ResponseWriter that records the status and body of an
// in-process API call (so the receiver need not open a socket to its own engine).
type captureWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
	wrote  bool
}

func (c *captureWriter) Header() http.Header { return c.header }

func (c *captureWriter) WriteHeader(status int) {
	if !c.wrote {
		c.status = status
		c.wrote = true
	}
}

func (c *captureWriter) Write(b []byte) (int, error) {
	c.wrote = true
	return c.body.Write(b)
}
