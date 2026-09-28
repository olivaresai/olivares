// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

func TestFirewallPage_ServesTheConsolesFirewallReadOnThisHostWithNoAct(t *testing.T) {
	// The console's read model, after one round in which the firewall helper answered its status
	// with an open window and the owner's measurement was read.
	confirmed := managedPolicy()
	candidate := managedPolicy()
	candidate.Portal.Enabled = false
	id := strings.Repeat("cd", 16)
	status := firewall.Status{ConfirmedDigest: policy.Digest(confirmed), Confirmed: &confirmed, Windows: []firewall.Window{{
		SchemaVersion: firewall.WindowSchema, OperationID: id, BootID: "8d8a1f0c-54c0-4b3e-9d6a-2a1f3b4c5d6e", DeadlineNS: 120e9,
		CandidateDigest: policy.Digest(candidate), PreviousDigest: policy.Digest(confirmed), Candidate: candidate, State: firewall.WindowPending}}}
	bundle, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	call := func(_ context.Context, name string, request helperschema.Request) (helperschema.Response, error) {
		document, _ := json.Marshal(request)
		asked = append(asked, name+" "+string(document))
		if name != helperschema.HelperFirewall {
			return helperschema.Response{Result: helperschema.ResultFailed}, nil
		}
		return helperschema.Response{Result: helperschema.ResultAnswered, Bundle: bundle}, nil
	}
	reads := newModuleReads(time.Now)
	reads.measure = func() (firewall.Measurement, string) {
		return firewall.Measurement{SchemaVersion: firewall.MeasurementSchema, BootID: "8d8a1f0c-54c0-4b3e-9d6a-2a1f3b4c5d6e",
			MeasuredAt: "2026-09-27T18:59:00Z", PolicyDigest: policy.Digest(confirmed), InputPolicy: "drop", Rows: policy.Rows(confirmed)}, ""
	}

	// Before any read the page is served unmeasured, with no row.
	handler := NewHandler(Status{Firewalls: reads})
	rec := serve(t, handler, http.MethodGet, "/firewall", localPeer)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "unmeasured") || strings.Contains(rec.Body.String(), "9443/tcp") {
		t.Fatalf("GET /firewall before any read: %d\n%s", rec.Code, rec.Body.String())
	}

	reads.refresh(context.Background(), call)
	rec = serve(t, handler, http.MethodGet, "/firewall", localPeer)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET /firewall from this host: %d %v", rec.Code, rec.Header())
	}
	for _, want := range []string{
		"<h1>Firewall</h1>", (Status{}).SignIn.Statement(),
		"9443/tcp", "eth0", "every interface", "546/udp", "input drop", policy.Digest(confirmed),
		id, "reverts unless confirmed",
		"apply: disabled (act_not_adopted)", "confirm: disabled (act_not_adopted)", "olivares-appliance firewall confirm " + id,
		"revert: disabled (act_not_adopted)", "app-row: disabled (act_not_adopted)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not show %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<form") || strings.Contains(body, "<button") || strings.Contains(body, "method=") {
		t.Errorf("the page carries an act:\n%s", body)
	}
	if rec := serve(t, handler, http.MethodHead, "/firewall", localPeer); rec.Code != http.StatusOK {
		t.Errorf("HEAD /firewall: %d", rec.Code)
	}

	// No method but GET and HEAD, from this host or another; and nothing on another host.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if rec := serve(t, handler, method, "/firewall", localPeer); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s /firewall from this host: %d Allow %q", method, rec.Code, rec.Header().Get("Allow"))
		}
	}
	for _, peer := range []string{"192.0.2.50:5555", "[2001:db8::50]:5555", "", "not-an-address"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			rec := serve(t, handler, method, "/firewall", peer)
			if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "eth0") || strings.Contains(rec.Body.String(), "9443") {
				t.Errorf("%s /firewall from %q: %d\n%s", method, peer, rec.Code, rec.Body.String())
			}
		}
	}

	// Serving the page asked no helper: the one round above asked each helper its read once.
	if len(asked) != 3 {
		t.Errorf("the helpers were asked %v; serving the page asks none", asked)
	}
	for _, sent := range asked {
		if strings.HasPrefix(sent, "firewall ") && sent != `firewall {"op":"status"}` {
			t.Errorf("the reader sent %s", sent)
		}
	}
	// The page's source has one method, a read from memory.
	if n := reflect.TypeOf((*FirewallReads)(nil)).Elem().NumMethod(); n != 1 {
		t.Errorf("FirewallReads has %d methods, want its one read", n)
	}

	// A console with no firewall read serves the page unread.
	rec = serve(t, NewHandler(Status{}), http.MethodGet, "/firewall", localPeer)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "unmeasured") || !strings.Contains(rec.Body.String(), "apply: disabled (act_not_adopted)") {
		t.Errorf("GET /firewall with no read: %d\n%s", rec.Code, rec.Body.String())
	}
}
