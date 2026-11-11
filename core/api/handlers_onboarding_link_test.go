// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// lockedLog collects a server's log; a request may log from another goroutine.
type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// TestOnboardLinkDeliveryShowsTokenOnce: on a server with no mailer the
// administrator's session receives the invitation's secret once, for the link
// on the screen. Nothing else ever does: not a list, not the audit, not a log,
// not an API token. The secret redeems once and a regeneration voids it.
func TestOnboardLinkDeliveryShowsTokenOnce(t *testing.T) {
	logs := &lockedLog{}
	onEachEngine(t, func(o *api.Options) {
		o.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}, func(t *testing.T, h *harness) {
		admin := h.elevatedAdmin()
		tenant := h.createOrg(admin, "link-invite")
		hdr := tenantHdr(tenant)
		const email = "person@link-invite.example"

		for _, bad := range []map[string]any{
			{"email": email, "role": auth.RoleViewer, "mode": "password", "password": consentPassword, "delivery": "link"},
			{"email": email, "role": auth.RoleViewer, "mode": "invite", "delivery": "carrier-pigeon"},
		} {
			if r := h.do("POST", "/v1/onboard", admin, bad, hdr); r.code != http.StatusBadRequest {
				t.Errorf("onboard %v = %d %s, want 400", bad, r.code, r.raw)
			}
		}

		issued := h.do("POST", "/v1/tokens", admin, map[string]any{"name": "automation", "superadmin": true}, nil)
		if issued.code != http.StatusCreated {
			t.Fatalf("issue API token = %d %s", issued.code, issued.raw)
		}
		link := map[string]any{"email": email, "role": auth.RoleViewer, "mode": "invite", "delivery": "link"}
		if r := h.do("POST", "/v1/onboard", issued.body["token"].(string), link, hdr); r.code != http.StatusForbidden {
			t.Errorf("onboard by an API token = %d %s, want 403", r.code, r.raw)
		}
		if _, found := h.userByEmail(email); found {
			t.Fatal("a refused invitation created the account")
		}

		created := h.do("POST", "/v1/onboard", admin, link, hdr)
		if created.code != http.StatusCreated {
			t.Fatalf("onboard link = %d %s, want 201", created.code, created.raw)
		}
		if got := created.hdr.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store on a response that carries a secret", got)
		}
		invite, _ := created.body["invite"].(map[string]any)
		first, _ := invite["token"].(string)
		if !strings.HasPrefix(first, auth.PrefixInvite+"_") || invite["delivery"] != "link" {
			t.Fatalf("invite = %v, want a token and delivery link", invite)
		}
		inviteID, _ := invite["id"].(string)

		resent := h.do("POST", "/v1/invites/"+inviteID+"/resend?delivery=link", admin, nil, hdr)
		if resent.code != http.StatusAccepted || resent.hdr.Get("Cache-Control") != "no-store" {
			t.Fatalf("regenerate = %d %s", resent.code, resent.raw)
		}
		second, _ := resent.body["token"].(string)
		if second == "" || second == first || resent.body["delivery"] != "link" {
			t.Fatalf("regenerate = %v, want a new token and delivery link", resent.body)
		}
		if r := h.do("POST", "/v1/invites/"+inviteID+"/resend", admin, nil, hdr); r.code != http.StatusConflict || errCode(r.body) != "invite_delivery_unavailable" {
			t.Errorf("resend without a mailer = %d %s, want 409", r.code, r.raw)
		}

		for _, path := range []string{"/v1/invites", "/v1/audit", "/v1/members"} {
			r := h.do("GET", path, admin, nil, hdr)
			if r.code != http.StatusOK {
				t.Fatalf("GET %s = %d %s", path, r.code, r.raw)
			}
			if strings.Contains(r.raw, first) || strings.Contains(r.raw, second) || strings.Contains(r.raw, `"token"`) {
				t.Errorf("GET %s carries invitation token material: %s", path, r.raw)
			}
		}

		accept := func(token string) resp {
			return h.do("POST", "/v1/invites/accept", "", map[string]any{"token": token, "password": consentPassword}, nil)
		}
		if r := accept(first); r.code != http.StatusBadRequest {
			t.Errorf("accept the voided token = %d %s, want 400", r.code, r.raw)
		}
		if r := accept(second); r.code != http.StatusOK {
			t.Fatalf("accept = %d %s, want 200", r.code, r.raw)
		}
		if r := accept(second); r.code != http.StatusBadRequest {
			t.Errorf("accept twice = %d %s, want 400", r.code, r.raw)
		}
		h.login(email, consentPassword)

		for _, token := range []string{first, second} {
			if strings.Contains(logs.String(), token) {
				t.Errorf("the server log carries the invitation token")
			}
		}
	})
}
