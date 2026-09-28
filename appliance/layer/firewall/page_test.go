// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
)

// get serves one GET of h and returns the status and body.
func get(t *testing.T, h http.Handler, method string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, "/firewall", nil))
	return w, w.Body.String()
}

func TestPage_ShowsTheMeasuredRowsAndWhyEveryActIsDisabled(t *testing.T) {
	ctx := context.Background()
	o := newOwner(t)
	install(t, o, managed())
	candidate := managed()
	candidate.Portal.Enabled = false
	id := operationID(4)
	w, err := o.Apply(ctx, firewall.ApplyRequest{OperationID: id, Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := o.published(t)
	snapshot := firewall.Snapshot{
		SignInStatement: "Signed in for product-down repair.",
		Measurement:     firewall.Measurement{SchemaVersion: m.SchemaVersion, BootID: m.BootID, MeasuredAt: m.MeasuredAt, PolicyDigest: policy.Digest(managed()), InputPolicy: "drop", Rows: policy.Rows(managed())},
		Windows:         []firewall.Window{w},
	}
	page := firewall.Page(snapshot)
	// The page renders the snapshot it was given, never a later read or a later change.
	snapshot.Measurement.Rows[len(snapshot.Measurement.Rows)-1].Interfaces[0] = "eth9"
	rec, body := get(t, page, http.MethodGet)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET: %d %v", rec.Code, rec.Header())
	}
	for _, want := range []string{
		"Signed in for product-down repair.",
		"9443/tcp", "eth0", "every interface", "546/udp",
		"reverts unless confirmed",
		"IPv6 neighbor and router discovery",
		id,
		"apply: disabled (act_not_adopted)", "olivares-appliance firewall apply --revert-after",
		"confirm: disabled (act_not_adopted)", "olivares-appliance firewall confirm " + id,
		"revert: disabled (act_not_adopted)", "olivares-appliance firewall revert " + id,
		"app-row: disabled (act_not_adopted)", "olivares-appliance firewall app-row",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not show %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "eth9") || strings.Contains(body, "<form") {
		t.Errorf("the page shows a later change or carries a form:\n%s", body)
	}
	if rec, _ := get(t, page, http.MethodPost); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %d Allow %q", rec.Code, rec.Header().Get("Allow"))
	}

	// Unmeasured: the reason, loopback, and no row.
	_, body = get(t, firewall.Page(firewall.Snapshot{SignInStatement: "Not signed in.", Reason: "no measurement is published"}), http.MethodGet)
	for _, want := range []string{"unmeasured", "no measurement is published", "loopback", "apply: disabled (act_not_adopted)"} {
		if !strings.Contains(body, want) {
			t.Errorf("the unmeasured page does not show %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "9443/tcp") {
		t.Errorf("the unmeasured page shows a row:\n%s", body)
	}
}

func TestMeasurementReader_TrustsOnlyARootOwnedMeasurementOfThisBoot(t *testing.T) {
	o := newOwner(t)
	install(t, o, managed())
	path := filepath.Join(o.RunDir, "measured.json")
	bootID := filepath.Join(t.TempDir(), "boot_id")
	if err := os.WriteFile(bootID, []byte(bootA+"\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	root := func(os.FileInfo) (uint32, bool) { return 0, true }
	reader := firewall.MeasurementReader{Path: path, BootID: bootID, Owner: root}
	m, reason := reader.Read()
	if reason != "" || m.PolicyDigest != policy.Digest(managed()) || m.BootID != bootA {
		t.Fatalf("the owner's measurement reads %+v (%q)", m, reason)
	}
	for name, prepare := range map[string]func(*firewall.MeasurementReader){
		"another owner": func(r *firewall.MeasurementReader) {
			r.Owner = func(os.FileInfo) (uint32, bool) { return 1000, true }
		},
		"no measurement": func(r *firewall.MeasurementReader) { r.Path = filepath.Join(t.TempDir(), "measured.json") },
		"another boot": func(r *firewall.MeasurementReader) {
			other := filepath.Join(t.TempDir(), "boot_id")
			if err := os.WriteFile(other, []byte(bootB+"\n"), 0o444); err != nil {
				t.Fatal(err)
			}
			r.BootID = other
		},
		"a link to the measurement": func(r *firewall.MeasurementReader) {
			link := filepath.Join(t.TempDir(), "measured.json")
			if err := os.Symlink(path, link); err != nil {
				t.Fatal(err)
			}
			r.Path = link
		},
		"a measurement others may write": func(r *firewall.MeasurementReader) {
			copyPath := filepath.Join(t.TempDir(), "measured.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(copyPath, data, 0o666); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(copyPath, 0o666); err != nil {
				t.Fatal(err)
			}
			r.Path = copyPath
		},
		"a member the schema does not name": func(r *firewall.MeasurementReader) {
			copyPath := filepath.Join(t.TempDir(), "measured.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(copyPath, []byte(strings.Replace(string(data), `"rows"`, `"extra": 1, "rows"`, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			r.Path = copyPath
		},
	} {
		r := reader
		prepare(&r)
		if m, reason := r.Read(); reason == "" {
			t.Errorf("%s: read as measured: %+v", name, m)
		}
	}
}
