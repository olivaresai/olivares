// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netguard

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

func TestGuardEdge_ExposureIsExactRootUnitAndStatusOnly(t *testing.T) {
	peer := helperschema.Peer{UID: 0, Account: "root", Unit: "olivares-helper-system@12-34-0.service", Attested: true}
	if err := AdmitEdge(peer, "status"); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"apply", "confirm", "revert"} {
		if err := AdmitEdge(peer, action); err == nil {
			t.Fatal("exposure may mutate")
		}
	}
	for _, change := range []func(*helperschema.Peer){func(p *helperschema.Peer) { p.UID = 1000 }, func(p *helperschema.Peer) { p.Unit = "olivares-helper-systemx@12.service" }, func(p *helperschema.Peer) { p.Attested = false }} {
		bad := peer
		change(&bad)
		if err := AdmitEdge(bad, "status"); err == nil {
			t.Fatal("unattested exposure admitted")
		}
	}
}
func TestGuardEdge_TransientDriverIsRefused(t *testing.T) {
	for _, account := range []string{"root", "olivares-portal", "olivares-net-guard"} {
		p := helperschema.Peer{UID: 1001, Account: account, Unit: "run-test-driver.service", Attested: true}
		if account == "root" {
			p.UID = 0
		}
		if err := AdmitEdge(p, "apply"); err == nil {
			t.Fatal(account, "driver admitted")
		}
	}
}
func TestGuardEdge_StrictBoundedDocumentAndEOF(t *testing.T) {
	valid := `{"action":"status","operation_id":"0123456789abcdef0123456789abcdef"}`
	if _, err := DecodeEdge(strings.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{valid + ` {}`, valid + " garbage", valid + strings.Repeat(" ", MaxEdgeBytes), `{"action":"status","action":"apply"}`, `{"Action":"status"}`, `{"action":"status","path":"/etc/shadow"}`} {
		if _, err := DecodeEdge(strings.NewReader(bad)); err == nil {
			t.Fatal("invalid or oversized document accepted")
		}
	}
}
func TestGuard_StatusDuringPendingCallDoesNotTakeNetworkLock(t *testing.T) {
	e, n, _, change := newTestEngine(t)
	n.delay = UpdateInMemory
	if _, _, err := e.Apply(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	e.operation.Lock()
	defer e.operation.Unlock()
	status, err := e.Status(change.OperationID)
	if err != nil || status.PendingCalls != 1 {
		t.Fatal(status, err)
	}
}
