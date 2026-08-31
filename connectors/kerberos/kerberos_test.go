// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kerberos

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// captureSink collects the observations a connector emits.
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

// run writes lines to a temp log, opens the connector in batch mode and gathers.
func run(t *testing.T, format string, lines ...string) []model.FindingReport {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "kdc.log")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	s := New()
	s.now = func() time.Time { return time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC) }
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"log_path": path, "format": format, "follow": "false",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return sink.findings()
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestKerberoastingFromWinEvent(t *testing.T) {
	// 4769 (TGS-REQ) for a user service account answered with RC4 (0x17) => roasting.
	f := run(t, formatWinEventJSON,
		`{"EventID":4769,"EventData":{"ServiceName":"svc_mssql","TargetUserName":"attacker","TicketEncryptionType":"0x17","Status":"0x0","IpAddress":"10.0.0.9"}}`,
	)
	if len(f) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(f), f)
	}
	got := f[0]
	if got.Kind != "kerberoasting" || got.Severity != model.SeverityHigh {
		t.Errorf("finding = (%s, %s), want (kerberoasting, high)", got.Kind, got.Severity)
	}
	if got.SubjectRef != "svc_mssql" {
		t.Errorf("SubjectRef = %q, want svc_mssql (the NHI it converges on)", got.SubjectRef)
	}
	if got.SubjectKind != "nhi.service_account" {
		t.Errorf("SubjectKind = %q, want nhi.service_account", got.SubjectKind)
	}
	// Minimal data: the detail is a SHA-256 hash, never a raw ticket/hash/keytab.
	if !sha256Hex.MatchString(got.DetailHash) {
		t.Errorf("DetailHash = %q, want a sha-256 hex digest", got.DetailHash)
	}
	for _, bad := range []string{"keytab", "BEGIN", "ticket=", "krbtgt"} {
		if strings.Contains(strings.ToLower(got.Title), bad) {
			t.Errorf("Title leaked %q: %s", bad, got.Title)
		}
	}
}

func TestStrongCipherNoFinding(t *testing.T) {
	// AES256 (0x12) is the expected strong cipher — no finding.
	f := run(t, formatWinEventJSON,
		`{"EventID":4769,"EventData":{"ServiceName":"svc_mssql","TicketEncryptionType":"0x12","Status":"0x0"}}`,
	)
	if len(f) != 0 {
		t.Fatalf("AES ticket produced findings: %+v", f)
	}
}

func TestMachineAndKrbtgtExcluded(t *testing.T) {
	f := run(t, formatWinEventJSON,
		// A computer account (ends in $) with RC4 is not the roasting target.
		`{"EventID":4769,"EventData":{"ServiceName":"WIN-SQL$","TicketEncryptionType":"0x17","Status":"0x0"}}`,
		// krbtgt TGS (a TGT renewal) with RC4 is routine, not roasting.
		`{"EventID":4769,"EventData":{"ServiceName":"krbtgt","TicketEncryptionType":"0x17","Status":"0x0"}}`,
	)
	if len(f) != 0 {
		t.Fatalf("machine/krbtgt produced roasting findings: %+v", f)
	}
}

func TestFailedRequestNoIssuance(t *testing.T) {
	// A failed TGS (non-zero status) was not issued a ticket — no roasting finding.
	f := run(t, formatWinEventJSON,
		`{"EventID":4769,"EventData":{"ServiceName":"svc_mssql","TicketEncryptionType":"0x17","Status":"0x6"}}`,
	)
	if len(f) != 0 {
		t.Fatalf("failed request produced findings: %+v", f)
	}
}

func TestDESWeakCipherFinding(t *testing.T) {
	// Legacy DES (0x03) on a TGT request is a weak-cipher posture finding (Medium).
	f := run(t, formatWinEventJSON,
		`{"EventID":4768,"EventData":{"TargetUserName":"legacyhost","ServiceName":"krbtgt","TicketEncryptionType":"0x3","Status":"0x0"}}`,
	)
	if len(f) != 1 || f[0].Kind != "weak_kerberos_cipher" || f[0].Severity != model.SeverityMedium {
		t.Fatalf("want one weak_kerberos_cipher Medium, got %+v", f)
	}
}

func TestRC4TGTWeakCipherFinding(t *testing.T) {
	// A TGT request (4768) issued with RC4 (0x17) is a weak-cipher posture finding
	// (the AS-side downgrade), even though it is not Kerberoasting (that is TGS).
	f := run(t, formatWinEventJSON,
		`{"EventID":4768,"EventData":{"TargetUserName":"svc_legacy","ServiceName":"krbtgt","TicketEncryptionType":"0x17","Status":"0x0"}}`,
	)
	if len(f) != 1 || f[0].Kind != "weak_kerberos_cipher" || f[0].Severity != model.SeverityMedium {
		t.Fatalf("want one weak_kerberos_cipher Medium for RC4 TGT, got %+v", f)
	}
}

func TestKerberoastingFromMITLog(t *testing.T) {
	// MIT krb5kdc: issued ticket cipher tkt=23 (RC4) for a user service principal.
	line := `Jun 06 12:00:00 kdc krb5kdc[42](info): TGS_REQ (3 etypes {23 18 17}) 10.0.0.9: ISSUE: authtime 1780000000, etypes {rep=18 tkt=23 ses=18}, attacker@CORP.EXAMPLE for svc_mssql@CORP.EXAMPLE`
	f := run(t, formatMITKDC, line)
	if len(f) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(f), f)
	}
	if f[0].Kind != "kerberoasting" || f[0].SubjectRef != "svc_mssql" {
		t.Errorf("MIT finding = (%s, %s), want (kerberoasting, svc_mssql)", f[0].Kind, f[0].SubjectRef)
	}
}

func TestMITStrongTicketNoFinding(t *testing.T) {
	line := `Jun 06 12:00:00 kdc krb5kdc[42](info): TGS_REQ (2 etypes {18 17}) 10.0.0.1: ISSUE: authtime 1780000000, etypes {rep=18 tkt=18 ses=18}, alice@CORP for svc_mssql@CORP`
	if f := run(t, formatMITKDC, line); len(f) != 0 {
		t.Fatalf("strong MIT ticket produced findings: %+v", f)
	}
}

func TestUnknownFormatRefused(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"format": "bogus"}}); err == nil {
		t.Fatal("Open accepted an unknown format")
	}
}

func TestNoLogPathIsQuietNoOp(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if len(sink.obs) != 0 {
		t.Fatalf("no-op Gather emitted: %+v", sink.obs)
	}
}
