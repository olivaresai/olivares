// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/sessions"
)

const (
	appsGatewaySnapshot       = "2026-07-10"
	deviceGrantType           = "urn:ietf:params:oauth:grant-type:device_code"
	deviceGrantTTL            = 10 * time.Minute
	deviceGrantPollInterval   = 5 * time.Second
	userCodeAlphabet          = "BCDFGHJKLMNPQRSTVWXZ"
	headerOlivaresVersion     = "x-olivares-version"
	headerSpendRequestID      = "request-id"
	appsGatewaySpendLimitPath = "/v1/organizations/spend_limits"
)

type deviceGrantStore interface {
	CreateDeviceGrant(ctx context.Context, storageTenant model.TenantID, deviceCode, userCode string, now time.Time, ttl time.Duration) (inferenceproxy.DeviceGrant, error)
	PollDeviceGrant(ctx context.Context, storageTenant model.TenantID, deviceCode string, now time.Time, interval time.Duration) (inferenceproxy.DeviceGrant, inferenceproxy.DeviceGrantPollStatus, error)
	ConsumeDeviceGrant(ctx context.Context, storageTenant model.TenantID, id model.ID, now time.Time) (inferenceproxy.DeviceGrant, error)
}

type spendLimitAdmin interface {
	SpendLimitUpsert(context.Context, model.TenantID, finops.SpendLimitSpec, string) (finops.SpendLimit, bool, error)
	SpendLimitGet(context.Context, model.TenantID, model.ID) (finops.SpendLimit, error)
	SpendLimitDelete(context.Context, model.TenantID, model.ID, string) error
	SpendLimitList(context.Context, model.TenantID, int, model.ID, model.ID) (finops.SpendLimitListResult, error)
	SpendLimitEffective(context.Context, model.TenantID, finops.SpendLimitEffectiveOptions) (finops.SpendLimitEffectiveResult, error)
	SpendLimitAudit(context.Context, model.TenantID, int) (finops.SpendLimitAuditResult, error)
}

var _ spendLimitAdmin = (*finops.Module)(nil)

type appsGatewayHandler struct {
	publicURL           string
	managedSettingsPath string
	tenantHint          model.TenantID
	authr               principalAuthenticator
	grants              deviceGrantStore
	spendLimits         spendLimitAdmin
	creds               sessions.CredentialSource
	clock               func() time.Time
	version             string
	descriptor          appsGatewayDescriptor
}

type appsGatewayDescriptor struct {
	Protocol    string   `json:"protocol"`
	Superset    string   `json:"superset"`
	Snapshot    string   `json:"snapshot"`
	Endpoints   []string `json:"endpoints"`
	Divergences []string `json:"divergences"`
}

func newAppsGatewayHandler(cfg inferenceProxyConfig, tenantHint model.TenantID, authr principalAuthenticator, grants deviceGrantStore, spendLimits spendLimitAdmin, creds sessions.CredentialSource, clock func() time.Time, buildVersion string) *appsGatewayHandler {
	if clock == nil {
		clock = time.Now
	}
	buildVersion = strings.TrimSpace(buildVersion)
	if buildVersion == "" {
		buildVersion = "dev"
	}
	h := &appsGatewayHandler{
		publicURL:           normalizePublicURL(cfg.PublicURL),
		managedSettingsPath: strings.TrimSpace(cfg.ManagedSettingsPath),
		tenantHint:          tenantHint,
		authr:               authr,
		grants:              grants,
		spendLimits:         spendLimits,
		creds:               creds,
		clock:               clock,
		version:             buildVersion,
	}
	h.descriptor = appsGatewayDescriptor{
		Protocol:  "llm-gateway",
		Superset:  "claude-apps-gateway/olivares",
		Snapshot:  appsGatewaySnapshot,
		Endpoints: h.enabledEndpoints(),
		Divergences: []string{
			"spend-limit deny: 402 billing_error (hard) / 429 rate_limit_error (throttle), both with x-should-retry: false",
			"managed settings: single-document mode",
			"version header: x-olivares-version",
			"device verification page: /device is reserved for phase 2; approval uses /v1/m/inferenceproxy/device/approve",
			"admin authentication: Olivares bearer principals replace static admin keys",
			"spend-limit user_id: Olivares audit actor ref (user:<id> or token:<id>), not an OIDC subject",
			"spend-limit group_limit_mode: fixed to min",
		},
	}
	return h
}

func mountAppsGatewayHandlers(mux *http.ServeMux, h *appsGatewayHandler) {
	mux.HandleFunc("/protocol", h.handleProtocol)
	mux.HandleFunc("/.well-known/oauth-authorization-server", h.handleOAuthDiscovery)
	mux.HandleFunc("/oauth/device_authorization", h.handleDeviceAuthorization)
	mux.HandleFunc("/oauth/token", h.handleOAuthToken)
	mux.HandleFunc("/managed/settings", h.handleManagedSettings)
	mux.HandleFunc(appsGatewaySpendLimitPath, h.handleSpendLimits)
	mux.HandleFunc(appsGatewaySpendLimitPath+"/", h.handleSpendLimits)
}

func appsGatewayRootHandler(proxy http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && r.URL.Path == "/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		proxy.ServeHTTP(w, r)
	}
}

func (h *appsGatewayHandler) enabledEndpoints() []string {
	out := []string{
		"/",
		"/protocol",
		"/v1/messages",
		"/v1/messages/batches",
		appsGatewaySpendLimitPath,
		appsGatewaySpendLimitPath + "/{id}",
		appsGatewaySpendLimitPath + "/effective",
		appsGatewaySpendLimitPath + "/audit",
	}
	if h.publicURL != "" {
		out = append(out,
			"/.well-known/oauth-authorization-server",
			"/oauth/device_authorization",
			"/oauth/token",
		)
	}
	if h.managedSettingsPath != "" {
		out = append(out, "/managed/settings")
	}
	return out
}

func (h *appsGatewayHandler) handleProtocol(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeGatewayJSON(w, http.StatusOK, h.descriptor)
}

func (h *appsGatewayHandler) handleOAuthDiscovery(w http.ResponseWriter, r *http.Request) {
	if h.publicURL == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeGatewayJSON(w, http.StatusOK, map[string]any{
		"issuer":                        h.publicURL,
		"device_authorization_endpoint": h.publicURL + "/oauth/device_authorization",
		"token_endpoint":                h.publicURL + "/oauth/token",
		"grant_types_supported":         []string{deviceGrantType},
		"response_types_supported":      []string{},
	})
}

func (h *appsGatewayHandler) handleDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	if h.publicURL == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.grants == nil {
		writeOAuthError(w, http.StatusServiceUnavailable, "server_error", "device grant store unavailable")
		return
	}
	now := h.clock().UTC()
	var grant inferenceproxy.DeviceGrant
	var err error
	for i := 0; i < 5; i++ {
		deviceCode, derr := randomDeviceCode()
		userCode, uerr := randomUserCode()
		if derr != nil || uerr != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not create device code")
			return
		}
		grant, err = h.grants.CreateDeviceGrant(r.Context(), h.deviceGrantStorageTenant(), deviceCode, userCode, now, deviceGrantTTL)
		if err == nil {
			break
		}
		if !errors.Is(err, store.ErrConflict) {
			break
		}
	}
	if err != nil {
		writeOAuthError(w, http.StatusServiceUnavailable, "server_error", "could not store device code")
		return
	}
	verificationURI := h.publicURL + "/device"
	writeGatewayJSON(w, http.StatusOK, map[string]any{
		"device_code":               grant.DeviceCode,
		"user_code":                 grant.UserCode,
		"verification_uri":          verificationURI,
		"verification_uri_complete": verificationURI + "?user_code=" + url.QueryEscape(grant.UserCode),
		"expires_in":                int(deviceGrantTTL.Seconds()),
		"interval":                  int(deviceGrantPollInterval.Seconds()),
	})
}

func (h *appsGatewayHandler) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if h.publicURL == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid form body")
		return
	}
	if r.PostForm.Get("grant_type") != deviceGrantType {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "unsupported grant_type")
		return
	}
	deviceCode := strings.TrimSpace(r.PostForm.Get("device_code"))
	if deviceCode == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "device_code is required")
		return
	}
	if h.grants == nil {
		writeOAuthError(w, http.StatusServiceUnavailable, "server_error", "device grant store unavailable")
		return
	}
	now := h.clock().UTC()
	grant, status, err := h.grants.PollDeviceGrant(r.Context(), h.deviceGrantStorageTenant(), deviceCode, now, deviceGrantPollInterval)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "device_code is invalid")
		return
	}
	switch status {
	case inferenceproxy.DeviceGrantPollPending:
		writeOAuthError(w, http.StatusBadRequest, "authorization_pending", "authorization is pending")
	case inferenceproxy.DeviceGrantPollSlowDown:
		writeOAuthError(w, http.StatusBadRequest, "slow_down", "polling interval not elapsed")
	case inferenceproxy.DeviceGrantPollExpired:
		writeOAuthError(w, http.StatusBadRequest, "expired_token", "device_code expired")
	case inferenceproxy.DeviceGrantPollDenied:
		writeOAuthError(w, http.StatusBadRequest, "access_denied", "device authorization denied")
	case inferenceproxy.DeviceGrantPollConsumed:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "device_code already consumed")
	case inferenceproxy.DeviceGrantPollApproved:
		h.mintDeviceToken(w, r, grant, now)
	default:
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "device_code is invalid")
	}
}

func (h *appsGatewayHandler) mintDeviceToken(w http.ResponseWriter, r *http.Request, grant inferenceproxy.DeviceGrant, now time.Time) {
	if h.creds == nil {
		writeGatewayError(w, http.StatusNotImplemented, "not_implemented", "credential minting source is not configured", "")
		return
	}
	if grant.Tenant.IsZero() {
		writeOAuthError(w, http.StatusBadRequest, "access_denied", "device authorization has no tenant binding")
		return
	}
	consumed, err := h.grants.ConsumeDeviceGrant(r.Context(), h.deviceGrantStorageTenant(), grant.ID, now)
	if err != nil {
		switch {
		case errors.Is(err, inferenceproxy.ErrDeviceGrantExpired):
			writeOAuthError(w, http.StatusBadRequest, "expired_token", "device_code expired")
		case errors.Is(err, inferenceproxy.ErrDeviceGrantDenied):
			writeOAuthError(w, http.StatusBadRequest, "access_denied", "device authorization denied")
		default:
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "device_code already consumed")
		}
		return
	}
	cred, err := h.creds.Mint(r.Context(), sessions.CredentialRequest{
		Tenant: consumed.Tenant, RunRef: "device:" + consumed.ID.String(), Transport: sessions.TransportStreamJSON,
	})
	if err != nil {
		writeOAuthError(w, http.StatusServiceUnavailable, "server_error", "credential mint failed")
		return
	}
	expiresIn := int(cred.NotAfter.Sub(now).Seconds())
	if cred.Token == "" || expiresIn <= 0 {
		writeOAuthError(w, http.StatusServiceUnavailable, "server_error", "credential mint returned an expired token")
		return
	}
	writeGatewayJSON(w, http.StatusOK, map[string]any{
		"access_token": cred.Token,
		"token_type":   "Bearer",
		"expires_in":   expiresIn,
	})
}

func (h *appsGatewayHandler) handleManagedSettings(w http.ResponseWriter, r *http.Request) {
	if h.managedSettingsPath == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if _, ok := h.authenticateBearer(r.Context(), r); !ok {
		writeGatewayError(w, http.StatusUnauthorized, "authentication_error", "bearer authentication required", "")
		return
	}
	body, err := os.ReadFile(h.managedSettingsPath)
	if err != nil {
		writeGatewayError(w, http.StatusInternalServerError, "api_error", "managed settings unavailable", "")
		return
	}
	sum := sha256.Sum256(body)
	etag := `"sha256:` + hex.EncodeToString(sum[:]) + `"`
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", etag)
	w.Header().Set(headerOlivaresVersion, h.version)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *appsGatewayHandler) handleSpendLimits(w http.ResponseWriter, r *http.Request) {
	requestID := spendRequestID(r)
	w.Header().Set(headerSpendRequestID, requestID)
	principal, ok := h.authenticateBearer(r.Context(), r)
	if !ok {
		writeSpendError(w, http.StatusUnauthorized, "authentication_error", "bearer authentication required", requestID)
		return
	}
	tenant, ok := h.resolveTenant(principal)
	if !ok {
		writeSpendError(w, http.StatusForbidden, "permission_error", "tenant not resolvable from the inbound credential", requestID)
		return
	}
	if (r.Method == http.MethodPost || r.Method == http.MethodDelete) && !spendLimitAdminAllowed(principal, tenant) {
		writeSpendError(w, http.StatusForbidden, "permission_error", "tenant administrator role required", requestID)
		return
	}
	if h.spendLimits == nil {
		writeSpendError(w, http.StatusServiceUnavailable, "api_error", "spend-limit store unavailable", requestID)
		return
	}
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == appsGatewaySpendLimitPath && r.Method == http.MethodGet:
		h.handleSpendLimitList(w, r, tenant, requestID)
	case path == appsGatewaySpendLimitPath && r.Method == http.MethodPost:
		h.handleSpendLimitUpsert(w, r, tenant, principal.Actor(), requestID)
	case path == appsGatewaySpendLimitPath+"/effective" && r.Method == http.MethodGet:
		h.handleSpendLimitEffective(w, r, tenant, requestID)
	case path == appsGatewaySpendLimitPath+"/audit" && r.Method == http.MethodGet:
		h.handleSpendLimitAudit(w, r, tenant, requestID)
	case strings.HasPrefix(path, appsGatewaySpendLimitPath+"/") && r.Method == http.MethodGet:
		h.handleSpendLimitGet(w, r, tenant, strings.TrimPrefix(path, appsGatewaySpendLimitPath+"/"), requestID)
	case strings.HasPrefix(path, appsGatewaySpendLimitPath+"/") && r.Method == http.MethodDelete:
		h.handleSpendLimitDelete(w, r, tenant, principal.Actor(), strings.TrimPrefix(path, appsGatewaySpendLimitPath+"/"), requestID)
	default:
		writeSpendError(w, http.StatusNotFound, "not_found_error", "spend-limit endpoint not found", requestID)
	}
}

func spendLimitAdminAllowed(p auth.Principal, tenant model.TenantID) bool {
	if p.Superadmin {
		return true
	}
	role, ok := p.RoleIn(tenant)
	return ok && auth.RoleRank(role) >= auth.RoleRank(auth.RoleAdmin)
}

func spendLimitQueryLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return 20, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 1000 {
		return 0, errors.New("limit must be between 1 and 1000")
	}
	return n, nil
}

func spendLimitQueryID(raw string) (model.ID, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	return finops.ParseSpendLimitID(strings.TrimSpace(raw))
}

func (h *appsGatewayHandler) handleSpendLimitList(w http.ResponseWriter, r *http.Request, tenant model.TenantID, requestID string) {
	limit, err := spendLimitQueryLimit(r)
	if err != nil {
		writeSpendError(w, http.StatusBadRequest, "invalid_request_error", err.Error(), requestID)
		return
	}
	q := r.URL.Query()
	if q.Get("after_id") != "" && q.Get("before_id") != "" {
		writeSpendError(w, http.StatusBadRequest, "invalid_request_error", "after_id and before_id are mutually exclusive", requestID)
		return
	}
	after, err := spendLimitQueryID(q.Get("after_id"))
	if err != nil {
		writeSpendError(w, http.StatusBadRequest, "invalid_request_error", "after_id must be a spl_ id", requestID)
		return
	}
	before, err := spendLimitQueryID(q.Get("before_id"))
	if err != nil {
		writeSpendError(w, http.StatusBadRequest, "invalid_request_error", "before_id must be a spl_ id", requestID)
		return
	}
	page, err := h.spendLimits.SpendLimitList(r.Context(), tenant, limit, after, before)
	if err != nil {
		h.writeSpendStoreError(w, err, requestID)
		return
	}
	var firstID, lastID any
	if len(page.Data) > 0 {
		firstID, lastID = page.Data[0].ID, page.Data[len(page.Data)-1].ID
	}
	writeGatewayJSON(w, http.StatusOK, map[string]any{"data": page.Data, "has_more": page.HasMore, "first_id": firstID, "last_id": lastID})
}

func (h *appsGatewayHandler) handleSpendLimitUpsert(w http.ResponseWriter, r *http.Request, tenant model.TenantID, actor, requestID string) {
	var wire struct {
		Scope    finops.SpendLimitScope `json:"scope"`
		Amount   json.RawMessage        `json:"amount"`
		Currency string                 `json:"currency,omitempty"`
		Period   string                 `json:"period"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	// dec.More(): a body is ONE JSON document (scripts/check-json-decoders.sh).
	if err := dec.Decode(&wire); err != nil || dec.More() || len(wire.Amount) == 0 {
		writeSpendError(w, http.StatusBadRequest, "invalid_request_error", "invalid spend-limit body", requestID)
		return
	}
	in := finops.SpendLimitSpec{Scope: wire.Scope, Currency: wire.Currency, Period: wire.Period}
	if string(wire.Amount) != "null" {
		var amount string
		if err := json.Unmarshal(wire.Amount, &amount); err != nil {
			writeSpendError(w, http.StatusBadRequest, "invalid_request_error", "amount must be an integer cents string or null", requestID)
			return
		}
		in.Amount = &amount
	}
	row, _, err := h.spendLimits.SpendLimitUpsert(r.Context(), tenant, in, actor)
	if err != nil {
		h.writeSpendStoreError(w, err, requestID)
		return
	}
	writeGatewayJSON(w, http.StatusOK, row)
}

func (h *appsGatewayHandler) handleSpendLimitGet(w http.ResponseWriter, r *http.Request, tenant model.TenantID, wireID, requestID string) {
	id, err := finops.ParseSpendLimitID(wireID)
	if err != nil {
		writeSpendError(w, http.StatusNotFound, "not_found_error", "spend limit not found", requestID)
		return
	}
	row, err := h.spendLimits.SpendLimitGet(r.Context(), tenant, id)
	if err != nil {
		h.writeSpendStoreError(w, err, requestID)
		return
	}
	writeGatewayJSON(w, http.StatusOK, row)
}

func (h *appsGatewayHandler) handleSpendLimitDelete(w http.ResponseWriter, r *http.Request, tenant model.TenantID, actor, wireID, requestID string) {
	id, err := finops.ParseSpendLimitID(wireID)
	if err != nil {
		writeSpendError(w, http.StatusNotFound, "not_found_error", "spend limit not found", requestID)
		return
	}
	if err := h.spendLimits.SpendLimitDelete(r.Context(), tenant, id, actor); err != nil {
		h.writeSpendStoreError(w, err, requestID)
		return
	}
	writeGatewayJSON(w, http.StatusOK, map[string]any{"type": "spend_limit_deleted", "id": wireID})
}

func (h *appsGatewayHandler) handleSpendLimitEffective(w http.ResponseWriter, r *http.Request, tenant model.TenantID, requestID string) {
	limit, err := spendLimitQueryLimit(r)
	if err != nil {
		writeSpendError(w, http.StatusBadRequest, "invalid_request_error", err.Error(), requestID)
		return
	}
	q := r.URL.Query()
	periods := append([]string(nil), q["period[]"]...)
	if q.Get("sort") == "spend_desc" && len(periods) != 1 {
		writeSpendError(w, http.StatusBadRequest, "invalid_request_error", "spend_desc requires exactly one period[]", requestID)
		return
	}
	result, err := h.spendLimits.SpendLimitEffective(r.Context(), tenant, finops.SpendLimitEffectiveOptions{
		UserIDs: append([]string(nil), q["user_ids[]"]...), Periods: periods,
		Query: q.Get("q"), Sort: q.Get("sort"), Limit: limit, Page: q.Get("page"),
	})
	if err != nil {
		h.writeSpendStoreError(w, err, requestID)
		return
	}
	var next any
	if result.NextPage != "" {
		next = result.NextPage
	}
	writeGatewayJSON(w, http.StatusOK, map[string]any{"data": result.Data, "next_page": next})
}

func (h *appsGatewayHandler) handleSpendLimitAudit(w http.ResponseWriter, r *http.Request, tenant model.TenantID, requestID string) {
	limit, err := spendLimitQueryLimit(r)
	if err != nil {
		writeSpendError(w, http.StatusBadRequest, "invalid_request_error", err.Error(), requestID)
		return
	}
	result, err := h.spendLimits.SpendLimitAudit(r.Context(), tenant, limit)
	if err != nil {
		h.writeSpendStoreError(w, err, requestID)
		return
	}
	writeGatewayJSON(w, http.StatusOK, map[string]any{"data": result.Data, "has_more": result.HasMore})
}

func (h *appsGatewayHandler) writeSpendStoreError(w http.ResponseWriter, err error, requestID string) {
	switch {
	case errors.Is(err, finops.ErrInvalidSpendLimit):
		writeSpendError(w, http.StatusBadRequest, "invalid_request_error", strings.TrimPrefix(err.Error(), finops.ErrInvalidSpendLimit.Error()+": "), requestID)
	case errors.Is(err, store.ErrNotFound):
		writeSpendError(w, http.StatusNotFound, "not_found_error", "spend limit not found", requestID)
	default:
		writeSpendError(w, http.StatusServiceUnavailable, "api_error", "spend-limit store unavailable", requestID)
	}
}

func (h *appsGatewayHandler) authenticateBearer(ctx context.Context, r *http.Request) (auth.Principal, bool) {
	if h.authr == nil {
		return auth.Principal{}, false
	}
	p, err := h.authr.Authenticate(ctx, gatewayBearerToken(r))
	if err != nil || p.IsPurposeRestricted() {
		return auth.Principal{}, false
	}
	return p, true
}

func (h *appsGatewayHandler) resolveTenant(p auth.Principal) (model.TenantID, bool) {
	if p.IsPurposeRestricted() {
		return model.TenantID(""), false
	}
	if !h.tenantHint.IsZero() {
		if p.Superadmin || p.IsMember(h.tenantHint) {
			return h.tenantHint, true
		}
		return model.TenantID(""), false
	}
	if ts := p.Tenants(); len(ts) == 1 {
		return ts[0], true
	}
	return model.TenantID(""), false
}

func (h *appsGatewayHandler) deviceGrantStorageTenant() model.TenantID {
	if !h.tenantHint.IsZero() {
		return h.tenantHint
	}
	return model.SystemTenantID
}

func writeGatewayJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOAuthError(w http.ResponseWriter, status int, code, _ string) {
	writeGatewayJSON(w, status, map[string]string{"error": code})
}

func writeGatewayError(w http.ResponseWriter, status int, errType, message, requestID string) {
	body := map[string]any{"type": "error", "error": map[string]string{"type": errType, "message": message}}
	if requestID != "" {
		body["request_id"] = requestID
	}
	writeGatewayJSON(w, status, body)
}

func writeSpendError(w http.ResponseWriter, status int, errType, message, requestID string) {
	writeGatewayError(w, status, errType, message, requestID)
}

func spendRequestID(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get(headerSpendRequestID)); id != "" {
		return id
	}
	if id := strings.TrimSpace(r.Header.Get("x-request-id")); id != "" {
		return id
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "req_" + hex.EncodeToString(b[:])
	}
	return "req_" + hex.EncodeToString([]byte(time.Now().UTC().Format("150405.000000000")))
}

func randomDeviceCode() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func randomUserCode() (string, error) {
	var b strings.Builder
	bound := big.NewInt(int64(len(userCodeAlphabet)))
	for i := 0; i < 8; i++ {
		n, err := rand.Int(rand.Reader, bound)
		if err != nil {
			return "", err
		}
		if i == 4 {
			b.WriteByte('-')
		}
		b.WriteByte(userCodeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

func gatewayBearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}

func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.TrimSpace(part) == etag {
			return true
		}
	}
	return false
}

func normalizePublicURL(v string) string {
	return strings.TrimRight(strings.TrimSpace(v), "/")
}
