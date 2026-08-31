// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aaa

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

type captureSink struct{ obs []model.Observation }

func (c *captureSink) Emit(_ context.Context, o model.Observation) error {
	c.obs = append(c.obs, o)
	return nil
}

func (c *captureSink) findings() []model.FindingReport {
	var out []model.FindingReport
	for _, o := range c.obs {
		if f, ok := o.(model.FindingReport); ok {
			out = append(out, f)
		}
	}
	return out
}

func tailRun(t *testing.T, mode, body string) []model.FindingReport {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "aaa.log")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	s := New()
	s.now = func() time.Time { return time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode": mode, "log_path": path, "follow": "false",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.findings()
}

func TestRADIUSDetailDeviceAdmin(t *testing.T) {
	body := "Tue Jun  6 12:00:00 2026\n" +
		"\tUser-Name = \"netadmin\"\n" +
		"\tNAS-IP-Address = 10.0.0.1\n" +
		"\tService-Type = Administrative-User\n" +
		"\tAcct-Status-Type = Start\n" +
		"\tUser-Password = \"sup3rs3cr3t\"\n\n"
	f := tailRun(t, modeRADIUSDetail, body)
	if len(f) != 1 || f[0].Kind != "aaa_device_admin" || f[0].Severity != model.SeverityInfo {
		t.Fatalf("want one aaa_device_admin Info, got %+v", f)
	}
	if f[0].SubjectRef != "netadmin" {
		t.Errorf("SubjectRef = %q, want netadmin", f[0].SubjectRef)
	}
	for _, x := range f {
		if strings.Contains(x.Title, "sup3rs3cr3t") {
			t.Errorf("password leaked into Title: %s", x.Title)
		}
	}
}

func TestRADIUSDetailReject(t *testing.T) {
	body := "Tue Jun  6 12:00:01 2026\n" +
		"\tPacket-Type = Access-Reject\n" +
		"\tUser-Name = \"baduser\"\n" +
		"\tNAS-IP-Address = 10.0.0.2\n\n"
	f := tailRun(t, modeRADIUSDetail, body)
	if len(f) != 1 || f[0].Kind != "aaa_auth_reject" || f[0].Severity != model.SeverityMedium {
		t.Fatalf("want one aaa_auth_reject Medium, got %+v", f)
	}
}

func TestRADIUSPlainAccountingNoFinding(t *testing.T) {
	// Ordinary network-access accounting (no admin service type) is high-volume
	// telemetry, not a finding (left to SIEM export).
	body := "Tue Jun  6 12:00:02 2026\n" +
		"\tUser-Name = \"laptop42\"\n" +
		"\tAcct-Status-Type = Start\n\n"
	if f := tailRun(t, modeRADIUSDetail, body); len(f) != 0 {
		t.Fatalf("plain accounting produced findings: %+v", f)
	}
}

func TestTACACSDeviceAdminRedactsCommand(t *testing.T) {
	// A tac_plus stop record whose command embeds a secret; the device-admin
	// finding must surface the session WITHOUT the raw command/secret.
	line := "Tue Jun  6 12:00:03 2026\t10.0.0.5\trouteradmin\ttty0\t192.0.2.9\tstop\ttask_id=7\tservice=shell\tpriv-lvl=15\tcmd=enable secret 0 hunter2pass\n"
	f := tailRun(t, modeTACACS, line)
	if len(f) != 1 || f[0].Kind != "aaa_device_admin" {
		t.Fatalf("want one aaa_device_admin, got %+v", f)
	}
	if f[0].SubjectRef != "routeradmin" {
		t.Errorf("SubjectRef = %q, want routeradmin", f[0].SubjectRef)
	}
	if strings.Contains(f[0].Title, "hunter2pass") || strings.Contains(f[0].Title, "cmd=") {
		t.Errorf("raw command leaked into Title: %s", f[0].Title)
	}
	// The redaction also keeps the secret out of the hashed detail input.
	ev, ok := parseTACACSLine(line)
	if !ok {
		t.Fatal("parseTACACSLine failed")
	}
	if strings.Contains(ev.detail, "hunter2pass") {
		t.Errorf("secret survived into the hash input: %s", ev.detail)
	}
}

// --- inbound RADIUS receiver ---

func tlv(typ byte, val []byte) []byte { return append([]byte{typ, byte(len(val) + 2)}, val...) }

func u32(n int) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(n))
	return b
}

// radiusAcctPacket builds a RADIUS Accounting-Request with the given attributes,
// optionally including a (to-be-ignored) User-Password to prove it is never read.
func radiusAcctPacket(statusType, serviceType int, user string, withPassword bool) []byte {
	var attrs []byte
	attrs = append(attrs, tlv(attrUserName, []byte(user))...)
	attrs = append(attrs, tlv(attrAcctStatusType, u32(statusType))...)
	if serviceType != 0 {
		attrs = append(attrs, tlv(attrServiceType, u32(serviceType))...)
	}
	if withPassword {
		attrs = append(attrs, tlv(attrUserPassword, []byte("0123456789abcdef"))...)
	}
	pkt := make([]byte, radiusHeaderLen)
	pkt[0] = radiusAccountingReq
	pkt[1] = 7 // identifier
	binary.BigEndian.PutUint16(pkt[2:4], uint16(radiusHeaderLen+len(attrs)))
	return append(pkt, attrs...)
}

func TestRADIUSUDPReceiver(t *testing.T) {
	s := New()
	s.now = func() time.Time { return time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode": modeRADIUSUDP, "listen_addr": "127.0.0.1:0",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close(context.Background()) }()
	addr := s.conn.LocalAddr().String()

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan model.FindingReport, 4)
	go func() { _ = s.Gather(ctx, chanSink{results}) }()

	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	// Device-admin accounting Start with a User-Password that MUST be ignored.
	if _, err := conn.Write(radiusAcctPacket(acctStart, svcAdministrativeUser, "udpadmin", true)); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case f := <-results:
		if f.Kind != "aaa_device_admin" || f.SubjectRef != "udpadmin" {
			t.Errorf("finding = (%s, %s), want (aaa_device_admin, udpadmin)", f.Kind, f.SubjectRef)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no finding from inbound RADIUS packet within 3s")
	}
	cancel()
}

type chanSink struct{ ch chan model.FindingReport }

func (c chanSink) Emit(_ context.Context, o model.Observation) error {
	if f, ok := o.(model.FindingReport); ok {
		select {
		case c.ch <- f:
		default:
		}
	}
	return nil
}

func TestReceiverRefusesNonLoopbackBind(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode": modeRADIUSUDP, "listen_addr": "0.0.0.0:0", // wildcard is NOT loopback
	}})
	if err == nil {
		_ = s.Close(context.Background())
		t.Fatal("Open accepted a non-loopback bind without allow_public_bind (weakened)")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("error did not cite the loopback default: %v", err)
	}
}

func TestUnknownModeRefused(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"mode": "bogus"}}); err == nil {
		t.Fatal("Open accepted an unknown mode")
	}
}

func TestDecodeRADIUSNeverReadsPassword(t *testing.T) {
	pkt := radiusAcctPacket(acctStop, svcNASPromptUser, "decadmin", true)
	ev, err := decodeRADIUS(pkt)
	if err != nil {
		t.Fatalf("decodeRADIUS: %v", err)
	}
	if ev.user != "decadmin" || !ev.deviceAdmin {
		t.Errorf("ev = %+v, want user=decadmin deviceAdmin=true", ev)
	}
	if strings.Contains(ev.detail, "0123456789abcdef") {
		t.Errorf("password value leaked into detail: %s", ev.detail)
	}
}
