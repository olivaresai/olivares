// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServices_PageShowsClassesAndDisabledActsWithoutAForm(t *testing.T) {
	snapshot := Snapshot{
		Measured:        true,
		SignInStatement: "Sign-in is refused on this console: the repair console on tty1 remains.",
		ChangesCode:     "act_not_adopted",
		Inventory: Inventory{Units: []Listed{
			{Unit: active("dbus.service"), Class: ClassManaged},
			{Unit: Unit{Name: "srv-olivares-mnt-host-data.mount", LoadState: "loaded", ActiveState: "active", SubState: "mounted"}},
			{Unit: active("sshd.service")},
			{Unit: active("olivares.service")},
			{Unit: active("<script>alert(1)</script>.service")},
		}},
	}
	page := Page(snapshot)
	get := func(method string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		page.ServeHTTP(rec, httptest.NewRequest(method, "/services", nil))
		return rec
	}
	rec := get(http.MethodGet)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		"Services", snapshot.SignInStatement, "act_not_adopted", "unit_protected", "olivares-portal-storage",
		// The class comes from the class table, not from the list answer.
		"dbus.service</td><td>protected",
		"srv-olivares-mnt-host-data.mount</td><td>storage-owned",
		"sshd.service</td><td>lockout-risk",
		"olivares.service</td><td>managed",
		"olivares-appliance service stop olivares.service",
		"olivares-appliance service logs olivares.service --lines 100",
		"remote shell access",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not show %q", want)
		}
	}
	for _, never := range []string{"<form", "<script>", "<input", "<button", "method=\"post\""} {
		if strings.Contains(strings.ToLower(body), never) {
			t.Errorf("the page carries %q: it has no act of its own", never)
		}
	}
	if rec := get(http.MethodPost); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
	unmeasured := httptest.NewRecorder()
	Page(Snapshot{SignInStatement: "x", ChangesCode: "p1_stage_missing"}).ServeHTTP(unmeasured, httptest.NewRequest(http.MethodGet, "/services", nil))
	if !strings.Contains(unmeasured.Body.String(), "unmeasured") || !strings.Contains(unmeasured.Body.String(), "p1_stage_missing") {
		t.Errorf("an unread inventory is not shown as unmeasured: %s", unmeasured.Body.String())
	}
}
