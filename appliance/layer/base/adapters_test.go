// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// hostTools answers the two host commands these adapters run: the service account lookup and
// the product unit's state.
func hostTools(uid int, active string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "id" && len(args) == 2 && args[0] == "-u" && args[1] == productAccount:
			return []byte(strconv.Itoa(uid) + "\n"), nil
		case name == "systemctl" && len(args) == 2 && args[0] == "is-active" && args[1] == productUnit:
			return []byte(active + "\n"), nil
		}
		return nil, errors.New("unexpected command " + name)
	}
}

func TestStorage_RecordsOnlyWhatItMeasured(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, ProductDataDir)
	if err := os.MkdirAll(data, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(data, 0o750); err != nil {
		t.Fatal(err)
	}
	storage := Storage{Host: Host{Root: root, Run: hostTools(os.Getuid(), "inactive")}}
	effect, err := storage.Apply(context.Background(), Input{})
	if err != nil {
		t.Fatalf("a data directory private to the service account was refused: %v", err)
	}
	if strings.Contains(string(effect), "created") || !strings.Contains(string(effect), "readiness") {
		t.Fatalf("initialize-storage claims a store the product has not created: %q", effect)
	}
	if err := storage.Verify(context.Background(), Input{}, effect); err != nil {
		t.Fatalf("the recorded storage effect does not verify: %v", err)
	}
}

// The account lookup is the runner's `id -u`, which the fake answers, so this case reaches the
// effect whatever accounts the test host has: it can fail only on what the stage records.
func TestStorage_RecordsTheOwnerAndModeItMeasuredThroughTheInjectedLookup(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, ProductDataDir)
	if err := os.MkdirAll(data, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(data, 0o750); err != nil {
		t.Fatal(err)
	}
	uid := os.Getuid()
	effect, err := Storage{Host: Host{Root: root, Run: hostTools(uid, "inactive")}}.Apply(context.Background(), Input{})
	if err != nil {
		t.Fatalf("the lookup answered and the directory is private, yet the stage refused: %v", err)
	}
	for _, measured := range []string{"uid " + strconv.Itoa(uid), "mode 0750"} {
		if !strings.Contains(string(effect), measured) {
			t.Fatalf("initialize-storage does not record what it measured (%s): %q", measured, effect)
		}
	}
	if strings.Contains(string(effect), "created") {
		t.Fatalf("initialize-storage claims a store the product has not created: %q", effect)
	}
	if _, err := (Storage{Host: Host{Root: root, Run: hostTools(uid+1, "inactive")}}).Apply(context.Background(), Input{}); err == nil {
		t.Fatal("a data directory owned by another account than the looked-up one was accepted")
	}
}

// The product unit is Type=simple: systemd reports it active at fork, before the product has
// created its keys, store and certificate. Readiness waits for the product's own /readyz
// before judging those files.
func TestReadiness_WaitsForTheProductsOwnReadinessBeforeJudgingItsFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ProductDataDir), 0o750); err != nil {
		t.Fatal(err)
	}
	files := []string{"tls.key", "audit-signing.key", "catalog-signing.key", "policy-signing.key", "olivares.db"}
	// A certificate the product would write, so the probe runs; nothing answers on its address.
	unanswered := httptest.NewTLSServer(http.NotFoundHandler())
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: unanswered.Certificate().Raw})
	unanswered.Close()
	created := make(chan struct{})
	go func() {
		defer close(created)
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(root, ProductDataDir, "tls.crt"), certificate, 0o644)
		for _, name := range files {
			_ = os.WriteFile(filepath.Join(root, ProductDataDir, name), []byte("instance\n"), 0o600)
		}
	}()
	readiness := ProductReadiness{Host: Host{Root: root, Run: hostTools(os.Getuid(), "active")}, Poll: 10 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	got, err := readiness.Measure(ctx, Input{})
	<-created
	if err != nil || got.Health || got.Identity {
		t.Fatalf("no readiness endpoint answered, yet: %+v %v", got, err)
	}
	for _, name := range files {
		if strings.Contains(got.Detail, name) {
			t.Fatalf("readiness judged %s while the product was still creating it: %q", name, got.Detail)
		}
	}
	if !strings.Contains(got.Detail, "readiness endpoint") {
		t.Fatalf("the pending reason does not name the product's readiness endpoint: %q", got.Detail)
	}
}

// productReadyz answers the product's /readyz ok on the address readiness probes, and returns
// the PEM of the certificate it serves, the one the product writes as tls.crt.
func productReadyz(t *testing.T) []byte {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:8443")
	if err != nil {
		t.Fatalf("readiness probes 127.0.0.1:8443, which this case needs free: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status": "ok", "store": "up", "leader": true}`)
	}))
	_ = server.Listener.Close()
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
}

// Once /readyz answers ok over the instance certificate, a missing store or a missing or
// exposed identity file is a refusal with its reason, not a wait.
func TestReadiness_JudgesTheIdentityFilesAndTheStoreOnceReadyzAnswersOk(t *testing.T) {
	certificate := productReadyz(t)
	in := answersFixture(t, "olivares.example.test")
	private := map[string]os.FileMode{"tls.key": 0o600, "audit-signing.key": 0o600, "catalog-signing.key": 0o600, "policy-signing.key": 0o600}
	with := func(changes map[string]os.FileMode) map[string]os.FileMode {
		files := map[string]os.FileMode{}
		for name, mode := range private {
			files[name] = mode
		}
		for name, mode := range changes {
			files[name] = mode
		}
		return files
	}
	cases := []struct {
		name     string
		files    map[string]os.FileMode
		identity bool
		detail   string
	}{
		{"the store is missing", private, false, "olivares.db"},
		{"a signing key is exposed", with(map[string]os.FileMode{"audit-signing.key": 0o640, "olivares.db": 0o600}), false, "audit-signing.key"},
		{"every file present and private", with(map[string]os.FileMode{"olivares.db": 0o600}), true, "store and identity files present"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			place(t, root, filepath.Join(ProductDataDir, "tls.crt"), string(certificate))
			for name, mode := range tc.files {
				if err := os.Chmod(place(t, root, filepath.Join(ProductDataDir, name), "instance\n"), mode); err != nil {
					t.Fatal(err)
				}
			}
			readiness := ProductReadiness{Host: Host{Root: root, Run: hostTools(os.Getuid(), "active")}, Poll: 10 * time.Millisecond}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got, err := readiness.Measure(ctx, Input{})
			if err != nil || !got.Health || got.Identity != tc.identity || !strings.Contains(got.Detail, tc.detail) {
				t.Fatalf("after an ok: %+v %v", got, err)
			}
			dir, h := t.TempDir(), newFakeHost()
			seams := h.seams()
			seams.Readiness = readiness
			rec, err := newMachine(dir, h, &in, seams).Run(context.Background())
			want := Refused
			if tc.identity {
				want = Ready
			}
			if err != nil || rec.State != want || (want == Refused && (rec.Stage != StageReadiness || !strings.Contains(rec.Reason, tc.detail))) {
				t.Fatalf("recorded as %+v %v, want %s", rec, err, want)
			}
		})
	}
}

func TestNewInput_TheAdvisorySourceLabelIsNotPartOfTheDigest(t *testing.T) {
	doc, err := os.ReadFile("../../answers/testdata/cloud-init.json")
	if err != nil {
		t.Fatal(err)
	}
	relabeled := bytes.Replace(doc, []byte(`"source": "file"`), []byte(`"source": "guestinfo"`), 1)
	renamed := bytes.Replace(doc, []byte(`"hostname": "olivares.example.test"`), []byte(`"hostname": "renamed.example.test"`), 1)
	digests := map[string]string{}
	for name, d := range map[string][]byte{"file": doc, "relabeled": relabeled, "renamed": renamed} {
		in, err := NewInput(name+":ref", "", d)
		if err != nil {
			t.Fatal(err)
		}
		digests[name] = in.Digest
	}
	if digests["file"] != digests["relabeled"] {
		t.Fatal("the advisory source label changed the restart digest")
	}
	if digests["file"] == digests["renamed"] {
		t.Fatal("a changed answer left the restart digest unchanged")
	}
}
