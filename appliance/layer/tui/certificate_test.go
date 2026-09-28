// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/portal/auth"
	"github.com/olivaresai/olivares/appliance/layer/repair"
)

// startNamedHelper stands in for the helper name on dir/<name>.sock until the test ends: it keeps
// each document it is sent and answers each with answer.
func startNamedHelper(t *testing.T, dir, name, answer string) *powerHelper {
	t.Helper()
	l, err := net.Listen("unix", filepath.Join(dir, name+".sock"))
	if err != nil {
		t.Fatal(err)
	}
	h := &powerHelper{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			var document map[string]any
			data, _ := io.ReadAll(conn)
			_ = json.Unmarshal(data, &document)
			h.mu.Lock()
			h.received = append(h.received, document)
			h.mu.Unlock()
			_, _ = io.WriteString(conn, answer)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		<-done
	})
	return h
}

// documents returns the documents the helper received, in order.
func (h *powerHelper) documents() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]map[string]any(nil), h.received...)
}

func TestConsole_CertificateVerbGeneratesAndShowsTheFingerprint(t *testing.T) {
	before, after := strings.Repeat("ab", 32), strings.Repeat("cd", 32)
	certificate := verbPosition(t, auth.VerbCertificate)

	setup := func(t *testing.T, answer string, readableAfter bool) (*fixture, *powerHelper, Console) {
		t.Helper()
		f := newFixture(t, onTty1, e12Record{human: recordedHuman})
		cert := startNamedHelper(t, f.helperDir, helperschema.HelperCert, answer)
		fingerprint := func() (string, bool, string) {
			if len(cert.documents()) == 0 {
				return before, true, ""
			}
			if !readableAfter {
				return "", false, "stored certificate cannot be read"
			}
			return after, true, ""
		}
		return f, cert, f.console.WithCertificateFingerprint(fingerprint)
	}

	t.Run("generate asks the certificate helper once and shows the new fingerprint as the public certificate reader measures it", func(t *testing.T) {
		_, cert, console := setup(t, `{"result": "performed"}`+"\n", true)
		code, out := session(t, console, "s", "olivares", certificate, "generate", "q")
		if code != 0 {
			t.Fatalf("the console exited %d:\n%s", code, out)
		}
		documents := cert.documents()
		if len(documents) != 1 || len(documents[0]) != 2 || documents[0]["op"] != helperschema.CertGenerate {
			t.Fatalf("the certificate helper received %v, want one generate document", documents)
		}
		if id, _ := documents[0]["operation_id"].(string); len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
			t.Fatalf("the generate document carries the operation id %q", id)
		}
		if !strings.Contains(out, before) || !strings.Contains(out, after) {
			t.Fatalf("the console did not show the fingerprint before and after:\n%s", out)
		}
		if !strings.Contains(out, "stored certificate") || strings.Contains(out, "Custody verifies") || strings.Contains(out, "serves it") || strings.Contains(out, "has the certificate") {
			t.Fatalf("the display claimed more than a stored public fingerprint:\n%s", out)
		}
		if strings.Index(out, after) < strings.Index(out, "performed generate") {
			t.Fatalf("the new fingerprint was shown before the helper answered:\n%s", out)
		}
	})

	t.Run("anything but generate shows the fingerprint and asks nothing", func(t *testing.T) {
		_, cert, console := setup(t, `{"result": "performed"}`+"\n", true)
		_, out := session(t, console, "s", "olivares", certificate, "GENERATE", "q")
		if len(cert.documents()) != 0 || !strings.Contains(out, before) || !strings.Contains(out, "Nothing was asked of the certificate helper.") {
			t.Fatalf("the helper was asked %v:\n%s", cert.documents(), out)
		}
	})

	t.Run("without a qualified sign-in nothing is shown or asked", func(t *testing.T) {
		_, cert, console := setup(t, `{"result": "performed"}`+"\n", true)
		_, out := session(t, console, certificate, "generate", "q")
		if len(cert.documents()) != 0 || strings.Contains(out, before) || !strings.Contains(out, "needs its qualified sign-in") {
			t.Fatalf("the helper was asked %v:\n%s", cert.documents(), out)
		}
	})

	t.Run("a refusal is stated with its code and no fingerprint is claimed", func(t *testing.T) {
		_, _, console := setup(t, `{"result": "refused", "code": "pidfd_unproven"}`+"\n", true)
		_, out := session(t, console, "s", "olivares", certificate, "generate", "q")
		if !strings.Contains(out, "refused generate (pidfd_unproven)") || strings.Contains(out, after) {
			t.Fatalf("a refusal read as:\n%s", out)
		}
	})

	t.Run("an unreadable certificate is reported without a fingerprint", func(t *testing.T) {
		_, _, console := setup(t, `{"result": "performed"}`+"\n", false)
		_, out := session(t, console, "s", "olivares", certificate, "generate", "q")
		if !strings.Contains(out, "cannot be read") || strings.Contains(out, after) {
			t.Fatalf("an unreadable certificate read as:\n%s", out)
		}
	})
}

func TestConsole_RepairPortalPamRestoresThePristineFileAndMeasuresTheStack(t *testing.T) {
	shipped, err := os.ReadFile(filepath.Join("..", "portal", "units", "pam.d", "olivares-portal"))
	if err != nil {
		t.Fatal(err)
	}
	broken := []byte("auth sufficient pam_permit.so\naccount sufficient pam_permit.so\n")
	login := []byte("auth include system-auth\n")
	repairPAM := verbPosition(t, auth.VerbRepairPortalPAM)

	setup := func(t *testing.T, group error) (Console, string, string) {
		t.Helper()
		root := t.TempDir()
		share := filepath.Join(root, "usr-share-olivares-portal-pam.d")
		etc := filepath.Join(root, "etc-pam.d")
		for _, dir := range []string{share, etc} {
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(share, auth.PAMService), shipped, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(etc, auth.PAMService), broken, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(etc, "login"), login, 0o644); err != nil {
			t.Fatal(err)
		}
		f := newFixture(t, onTty1, e12Record{human: recordedHuman})
		restorer := repair.PortalPAM{Pristine: filepath.Join(share, auth.PAMService), Dir: etc, Name: auth.PAMService,
			GroupExists: func(string) error { return group }}
		return f.console.WithPortalPAM(restorer), etc, share
	}
	read := func(t *testing.T, path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	t.Run("repair restores the pristine file, measures the stack and changes nothing else", func(t *testing.T) {
		console, etc, _ := setup(t, nil)
		_, out := session(t, console, "s", "olivares", repairPAM, "repair", "q")
		if got := read(t, filepath.Join(etc, auth.PAMService)); string(got) != string(shipped) {
			t.Fatalf("the stack is not the pristine copy after repair:\n%s", out)
		}
		if info, err := os.Lstat(filepath.Join(etc, auth.PAMService)); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
			t.Fatalf("the restored stack is %v (%v), want a regular file mode 0644", info.Mode(), err)
		}
		if got := read(t, filepath.Join(etc, "login")); string(got) != string(login) {
			t.Fatal("the repair changed the local-console service's stack")
		}
		entries, err := os.ReadDir(etc)
		if err != nil || len(entries) != 2 {
			t.Fatalf("the repair left %v in the directory (%v)", entries, err)
		}
		if !strings.Contains(out, "restored") || !strings.Contains(out, "usable") {
			t.Fatalf("the console does not report the restore and the measurement:\n%s", out)
		}
	})

	t.Run("a stack that measures unusable after the restore is reported as such", func(t *testing.T) {
		console, etc, _ := setup(t, os.ErrNotExist)
		_, out := session(t, console, "s", "olivares", repairPAM, "repair", "q")
		if got := read(t, filepath.Join(etc, auth.PAMService)); string(got) != string(shipped) {
			t.Fatal("the pristine copy was not restored")
		}
		if !strings.Contains(out, "not usable") || !strings.Contains(out, auth.AdministratorsGroup) {
			t.Fatalf("an absent administrators group was not reported:\n%s", out)
		}
	})

	t.Run("without a qualified sign-in, or without the typed word, nothing changes", func(t *testing.T) {
		for name, lines := range map[string][]string{
			"no sign-in":      {repairPAM, "repair", "q"},
			"another answer":  {"s", "olivares", repairPAM, "yes", "q"},
			"the input ended": {"s", "olivares", repairPAM},
		} {
			console, etc, _ := setup(t, nil)
			session(t, console, lines...)
			if got := read(t, filepath.Join(etc, auth.PAMService)); string(got) != string(broken) {
				t.Errorf("%s: the stack changed", name)
			}
		}
	})

	t.Run("an absent or altered pristine copy restores nothing", func(t *testing.T) {
		console, etc, share := setup(t, nil)
		if err := os.Remove(filepath.Join(share, auth.PAMService)); err != nil {
			t.Fatal(err)
		}
		_, out := session(t, console, "s", "olivares", repairPAM, "repair", "q")
		if got := read(t, filepath.Join(etc, auth.PAMService)); string(got) != string(broken) || !strings.Contains(out, "refused") {
			t.Fatalf("an absent pristine copy changed the stack:\n%s", out)
		}
		if err := os.Symlink(filepath.Join(etc, "login"), filepath.Join(share, auth.PAMService)); err != nil {
			t.Fatal(err)
		}
		session(t, console, "s", "olivares", repairPAM, "repair", "q")
		if got := read(t, filepath.Join(etc, auth.PAMService)); string(got) != string(broken) {
			t.Fatal("a linked pristine copy was restored")
		}
	})
}
