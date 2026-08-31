// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package snmp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func sampleNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "finding.reported",
		Title:    "agent escalation",
		Body:     "claude-1 denied",
		Severity: model.SeverityHigh,
		Tenant:   "acme",
		Fields:   map[string]string{"agent": "claude-1"},
		Time:     time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC),
	}
}

// recorder is an injected trapSender that captures sent traps.
type recorder struct {
	connected bool
	closed    bool
	sent      []gosnmp.SnmpTrap
	sendErr   error
}

func (r *recorder) Connect() error { r.connected = true; return nil }
func (r *recorder) SendTrap(t gosnmp.SnmpTrap) (*gosnmp.SnmpPacket, error) {
	r.sent = append(r.sent, t)
	return nil, r.sendErr
}
func (r *recorder) Close() error { r.closed = true; return nil }

func openWith(t *testing.T, rec *recorder, extra map[string]string) *Output {
	t.Helper()
	cfg := map[string]string{
		"target": "nms.corp", "username": "olivares",
		"auth_passphrase": "authpass12345", "priv_passphrase": "privpass12345",
	}
	for k, v := range extra {
		cfg[k] = v
	}
	o := New()
	o.newSender = func() (trapSender, error) { return rec, nil }
	if err := o.Open(context.Background(), sdk.Config{Settings: cfg}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return o
}

func TestBuildTrapVarbinds(t *testing.T) {
	o := openWith(t, &recorder{}, nil)
	trap := o.buildTrap(sampleNotification())

	if len(trap.Variables) == 0 {
		t.Fatal("no varbinds")
	}
	// First varbind MUST be snmpTrapOID.0, naming the notification OID (RFC 3416).
	v0 := trap.Variables[0]
	if v0.Name != snmpTrapOID || v0.Type != gosnmp.ObjectIdentifier {
		t.Fatalf("varbind[0] = %+v, want snmpTrapOID.0 ObjectIdentifier", v0)
	}
	if v0.Value != "1.3.6.1.4.1.32473.0.1" {
		t.Errorf("snmpTrapOID value = %v, want the PEN-32473 notification OID", v0.Value)
	}

	by := indexByName(trap.Variables)
	if got := by["1.3.6.1.4.1.32473.1.2"]; got == nil || got.Value != "high" {
		t.Errorf("severity label varbind = %v, want 'high'", got)
	}
	if got := by["1.3.6.1.4.1.32473.1.3"]; got == nil || got.Value.(int) != 7 {
		t.Errorf("severity number varbind = %v, want 7", got)
	}
	if got := by["1.3.6.1.4.1.32473.1.4"]; got == nil || got.Value != "agent escalation" {
		t.Errorf("title varbind = %v", got)
	}
	if got := by["1.3.6.1.4.1.32473.1.10"]; got == nil {
		t.Fatal("fields varbind missing")
	} else {
		var m map[string]string
		if err := json.Unmarshal([]byte(got.Value.(string)), &m); err != nil || m["agent"] != "claude-1" {
			t.Errorf("fields varbind not the expected JSON: %v", got.Value)
		}
	}
}

func TestPENOverrideChangesOIDs(t *testing.T) {
	o := openWith(t, &recorder{}, map[string]string{"pen": "9999"})
	trap := o.buildTrap(sampleNotification())
	if trap.Variables[0].Value != "1.3.6.1.4.1.9999.0.1" {
		t.Errorf("notification OID = %v, want PEN 9999 base", trap.Variables[0].Value)
	}
}

func TestNotifySendsTrap(t *testing.T) {
	rec := &recorder{}
	o := openWith(t, rec, nil)
	if !rec.connected {
		t.Error("Open did not Connect the session")
	}
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(rec.sent) != 1 {
		t.Fatalf("sent %d traps, want 1", len(rec.sent))
	}
	if rec.sent[0].IsInform {
		t.Error("default must be an unconfirmed trap, not an inform")
	}
}

func TestNotifyInformMode(t *testing.T) {
	rec := &recorder{}
	o := openWith(t, rec, map[string]string{"inform": "true"})
	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatal(err)
	}
	if !rec.sent[0].IsInform {
		t.Error("inform=true must send an InformRequest")
	}
}

func TestNotifySendErrorSurfaces(t *testing.T) {
	rec := &recorder{sendErr: errors.New("network down")}
	o := openWith(t, rec, nil)
	if err := o.Notify(context.Background(), sampleNotification()); err == nil {
		t.Fatal("a SendTrap error must surface from Notify")
	}
}

// TestSessionIsAuthPrivByDefault is the secure-default guard: the constructed SNMP
// session is always SNMPv3 USM at securityLevel authPriv — there is no noAuth/noPriv
// code path — with the RFC 7860/3826 defaults (SHA-256 + AES).
func TestSessionIsAuthPrivByDefault(t *testing.T) {
	ap, err := authProtocol("")
	if err != nil || ap != gosnmp.SHA256 {
		t.Errorf("default auth = %v (err %v), want SHA256", ap, err)
	}
	pp, err := privProtocol("")
	if err != nil || pp != gosnmp.AES {
		t.Errorf("default priv = %v (err %v), want AES", pp, err)
	}
	sc := sessionConfig{username: "u", authProto: ap, privProto: pp, engineID: "eid", boots: 1}
	g := sc.gosnmp()
	if g.Version != gosnmp.Version3 {
		t.Errorf("version = %v, want Version3", g.Version)
	}
	if g.MsgFlags != gosnmp.AuthPriv {
		t.Errorf("MsgFlags = %v, want AuthPriv (fail-closed, no noAuth/noPriv)", g.MsgFlags)
	}
	if g.SecurityModel != gosnmp.UserSecurityModel {
		t.Errorf("SecurityModel = %v, want USM", g.SecurityModel)
	}
	usm, ok := g.SecurityParameters.(*gosnmp.UsmSecurityParameters)
	if !ok {
		t.Fatalf("SecurityParameters type = %T, want *UsmSecurityParameters", g.SecurityParameters)
	}
	if usm.AuthenticationProtocol != gosnmp.SHA256 || usm.PrivacyProtocol != gosnmp.AES {
		t.Errorf("usm protocols = %v/%v, want SHA256/AES", usm.AuthenticationProtocol, usm.PrivacyProtocol)
	}
}

func TestResolveEngineID(t *testing.T) {
	// Explicit hex round-trips.
	got, err := resolveEngineID("8000000001020304", defaultPEN, "seed")
	if err != nil {
		t.Fatal(err)
	}
	if []byte(got)[0] != 0x80 || len(got) != 8 {
		t.Errorf("explicit engine id wrong: %x", got)
	}
	// Derived default: RFC 3411 enterprise format — first octet has the high bit set
	// and octets 2-4 carry the PEN (32473 = 0x7ED9).
	d, err := resolveEngineID("", 32473, "olivares|nms.corp")
	if err != nil {
		t.Fatal(err)
	}
	b := []byte(d)
	if len(b) < 5 || b[0] != 0x80 || b[2] != 0x7E || b[3] != 0xD9 || b[4] != 0x04 {
		t.Errorf("derived engine id not enterprise-format: %x", b)
	}
	if _, err := resolveEngineID("nothex!!", defaultPEN, "s"); err == nil {
		t.Error("non-hex engine_id must error")
	}
}

func TestOpenRejectsBadConfig(t *testing.T) {
	for i, cfg := range []map[string]string{
		{"username": "u", "auth_passphrase": "a", "priv_passphrase": "p"},                                          // no target
		{"target": "t", "auth_passphrase": "a", "priv_passphrase": "p"},                                            // no username
		{"target": "t", "username": "u", "priv_passphrase": "p"},                                                   // no auth pass
		{"target": "t", "username": "u", "auth_passphrase": "a"},                                                   // no priv pass
		{"target": "t", "username": "u", "auth_passphrase": "a", "priv_passphrase": "p", "auth_protocol": "rot13"}, // bad proto
	} {
		o := New()
		if err := o.Open(context.Background(), sdk.Config{Settings: cfg}); err == nil {
			t.Errorf("case %d: Open(%v) = nil, want error", i, cfg)
		}
	}
}

func indexByName(vars []gosnmp.SnmpPDU) map[string]*gosnmp.SnmpPDU {
	m := make(map[string]*gosnmp.SnmpPDU, len(vars))
	for i := range vars {
		m[vars[i].Name] = &vars[i]
	}
	return m
}
