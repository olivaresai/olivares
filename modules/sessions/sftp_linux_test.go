// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/ssh"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// sftpRig is a folder with a file in it, a symlink out of it and a secret beside it, served by the
// listener, with a real sftp client to talk to it.
type sftpRig struct {
	t       *testing.T
	m       *Module
	st      store.Store
	tenant  model.TenantID
	folder  workspaceDTO
	root    string
	outside string // a directory outside the folder, with secret.txt in it
	beside  string // secret.txt next to the folder, reached by ".."
	known   string // the client's known_hosts, holding the listener's host key
	svc     *sftpService
}

func newSFTPRig(t *testing.T) *sftpRig {
	t.Helper()
	if _, err := exec.LookPath("sftp"); err != nil {
		t.Skip("the sftp client is not installed")
	}
	server := os.Getenv("OLIVARES_TEST_SFTP_SERVER")
	if server == "" {
		found, err := findSFTPServer()
		if err != nil {
			t.Skip("no sftp-server: set OLIVARES_TEST_SFTP_SERVER or install openssh-sftp-server")
		}
		server = found
	}
	saved := sftpServerPaths
	sftpServerPaths = []string{server}
	t.Cleanup(func() { sftpServerPaths = saved })

	base := t.TempDir()
	root := filepath.Join(base, "folder")
	outside := t.TempDir()
	for path, text := range map[string]string{
		filepath.Join(root, "inside.txt"):    "inside the folder\n",
		filepath.Join(base, "secret.txt"):    "beside the folder\n",
		filepath.Join(outside, "secret.txt"): "outside the folder\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	m, st, tenant, _ := newRuntimeHarness(t, WithConfinement([]string{t.TempDir()}, false),
		WithSFTP("127.0.0.1:0", filepath.Join(t.TempDir(), "host-key")))
	folder := mkWorkspace(t, m, tenant, CreateWorkspaceParams{RootPath: root})
	m.startSFTP()
	svc := m.sftp.Load()
	if svc == nil {
		t.Fatal("the listener did not start")
	}
	hostLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(svc.signer.PublicKey())))
	known := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(known, []byte(fmt.Sprintf("[127.0.0.1]:%d %s\n", svc.port(), hostLine)), 0o600); err != nil {
		t.Fatal(err)
	}
	return &sftpRig{t: t, m: m, st: st, tenant: tenant, folder: folder, root: root, outside: outside,
		beside: filepath.Join(base, "secret.txt"), known: known, svc: svc}
}

// person is a caller who holds every right on the folder except those deny returns true for.
func (r *sftpRig) person(deny func(auth.TrusteeRight) bool) api.ModuleContext {
	principal := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID(), AAL: 3}
	resource := auth.ResourceAttrs{Kind: "resource", ID: r.folder.ResourceID.String()}
	return api.ModuleContext{Tenant: r.tenant, Principal: principal, Resource: resource,
		Admission: func(_ context.Context, q auth.Request) bool {
			for _, right := range []auth.TrusteeRight{auth.TrusteeRead, auth.TrusteeWrite, auth.TrusteeCreate, auth.TrusteeErase} {
				canonical, _ := right.Question(principal, r.tenant, q.Resource)
				if q.Route.CedarAction == string(canonical.Permission) && deny(right) {
					return false
				}
			}
			return true
		}}
}

// connect asks for a credential as mc does and writes the private key to a file.
func (r *sftpRig) connect(mc api.ModuleContext) (keyFile string, readOnly bool) {
	r.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("ref", r.folder.WorkspaceRef)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	rec := httptest.NewRecorder()
	r.m.handleSFTPConnect(rec, req, mc)
	if rec.Code != http.StatusCreated {
		r.t.Fatalf("connect = %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		r.t.Fatal(err)
	}
	private, _ := body["private_key"].(string)
	if body["user"] != sftpUser || body["host_key"] != r.svc.fingerprint() || !strings.Contains(private, "PRIVATE KEY") {
		r.t.Fatalf("connect body = %s", rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		r.t.Fatal("the response with the private key can be cached")
	}
	keyFile = filepath.Join(r.t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte(private), 0o600); err != nil {
		r.t.Fatal(err)
	}
	readOnly, _ = body["read_only"].(bool)
	return keyFile, readOnly
}

func (r *sftpRig) command(keyFile string) *exec.Cmd {
	return exec.Command("sftp", "-F", "/dev/null", "-i", keyFile, "-P", strconv.Itoa(r.svc.port()),
		"-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "UserKnownHostsFile="+r.known,
		"-o", "StrictHostKeyChecking=yes", "-b", "-", sftpUser+"@127.0.0.1")
}

// sftp runs one batch of commands through the real client and returns its output and whether it ended well.
func (r *sftpRig) sftp(keyFile, batch string) (string, bool) {
	r.t.Helper()
	cmd := r.command(keyFile)
	cmd.Stdin = strings.NewReader(batch)
	var out []byte
	var err error
	done := make(chan struct{})
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		r.t.Fatalf("sftp did not end for batch %q", batch)
	}
	return string(out), err == nil
}

func (r *sftpRig) openSessions() int {
	r.svc.mu.Lock()
	defer r.svc.mu.Unlock()
	return len(r.svc.open)
}

func TestSFTPFolderAccessThroughRealClient(t *testing.T) {
	r := newSFTPRig(t)
	local := t.TempDir()
	upload := filepath.Join(local, "up.txt")
	if err := os.WriteFile(upload, []byte("uploaded\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	everything := r.person(func(auth.TrusteeRight) bool { return false })
	key, readOnly := r.connect(everything)
	if readOnly {
		t.Fatal("a person with every right got a read-only credential")
	}

	t.Run("copies files in and out of the folder", func(t *testing.T) {
		got := filepath.Join(local, "got.txt")
		if out, ok := r.sftp(key, fmt.Sprintf("put %s up.txt\nget inside.txt %s\nls\n", upload, got)); !ok {
			t.Fatalf("sftp failed: %s", out)
		}
		if b, _ := os.ReadFile(filepath.Join(r.root, "up.txt")); string(b) != "uploaded\n" {
			t.Fatalf("the uploaded file in the folder = %q", b)
		}
		if b, _ := os.ReadFile(got); string(b) != "inside the folder\n" {
			t.Fatalf("the downloaded file = %q", b)
		}
	})

	t.Run("refuses every path outside the folder", func(t *testing.T) {
		for name, remote := range map[string]string{
			"absolute path outside": filepath.Join(r.outside, "secret.txt"),
			"dot dot":               "../secret.txt",
			"symlink out":           "escape/secret.txt",
			"absolute path beside":  r.beside,
		} {
			dst := filepath.Join(local, "stolen-"+strings.ReplaceAll(name, " ", "-"))
			out, ok := r.sftp(key, fmt.Sprintf("get %s %s\n", remote, dst))
			t.Logf("%s: %s", name, strings.TrimSpace(out))
			if ok {
				t.Errorf("%s: the client reported success: %s", name, out)
			}
			if b, err := os.ReadFile(dst); err == nil && len(b) > 0 {
				t.Errorf("%s: the secret reached the client: %q", name, b)
			}
		}
		// Not an assertion: what the policy leaves readable besides the folder, for the record.
		probe, _ := r.sftp(key, fmt.Sprintf("get /etc/hostname %s\nls /usr\n", filepath.Join(local, "hostname")))
		t.Logf("system directories: %s", strings.TrimSpace(probe))
		planted := filepath.Join(r.outside, "planted.txt")
		out, ok := r.sftp(key, fmt.Sprintf("put %s %s\n", upload, planted))
		t.Logf("write outside: %s", strings.TrimSpace(out))
		if ok {
			t.Errorf("a write outside the folder succeeded: %s", out)
		}
		if _, err := os.Stat(planted); err == nil {
			t.Error("a file was planted outside the folder")
		}
	})

	t.Run("a credential without the write rights only reads", func(t *testing.T) {
		readerKey, ro := r.connect(r.person(func(right auth.TrusteeRight) bool { return right != auth.TrusteeRead }))
		if !ro {
			t.Fatal("a person who can only read got a credential that writes")
		}
		if out, ok := r.sftp(readerKey, fmt.Sprintf("get inside.txt %s\n", filepath.Join(local, "read-only-got.txt"))); !ok {
			t.Fatalf("a read-only credential could not read: %s", out)
		}
		if out, ok := r.sftp(readerKey, fmt.Sprintf("put %s ro-up.txt\n", upload)); ok {
			t.Fatalf("a read-only credential wrote: %s", out)
		}
		if _, err := os.Stat(filepath.Join(r.root, "ro-up.txt")); err == nil {
			t.Fatal("a read-only credential created a file")
		}
	})

	t.Run("a key that was never issued, or has expired, is refused", func(t *testing.T) {
		other := filepath.Join(local, "never-issued")
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", other).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v %s", err, out)
		}
		if out, ok := r.sftp(other, "ls\n"); ok {
			t.Fatalf("an unknown key signed in: %s", out)
		}
		expiring, _ := r.connect(everything)
		r.svc.mu.Lock()
		for _, c := range r.svc.creds {
			c.expires = time.Now().Add(-time.Second)
		}
		r.svc.mu.Unlock()
		if out, ok := r.sftp(expiring, "ls\n"); ok {
			t.Fatalf("an expired credential signed in: %s", out)
		}
	})

	t.Run("a revoked grant refuses a new connection and ends an open session", func(t *testing.T) {
		old := sftpRecheckInterval
		sftpRecheckInterval = 200 * time.Millisecond
		t.Cleanup(func() { sftpRecheckInterval = old })
		var revoked atomic.Bool
		revokedKey, _ := r.connect(r.person(func(auth.TrusteeRight) bool { return revoked.Load() }))

		// An open session: the client waits on its input, which stays open.
		open := r.command(revokedKey)
		in, err := open.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := open.Start(); err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintln(in, "ls")
		waitFor(t, "the session to open", func() bool { return r.openSessions() == 1 })

		revoked.Store(true)
		// The server ends the session at its next rights check. The client, waiting on its input,
		// learns it with its next command, which fails.
		waitFor(t, "the revoked session to end", func() bool { return r.openSessions() == 0 })
		_, _ = fmt.Fprintln(in, "ls")
		ended := make(chan error, 1)
		go func() { ended <- open.Wait() }()
		select {
		case err := <-ended:
			if err == nil {
				t.Fatal("the client of a revoked session ended well")
			}
		case <-time.After(10 * time.Second):
			_ = open.Process.Kill()
			t.Fatal("the client of a revoked session stayed connected")
		}
		if out, ok := r.sftp(revokedKey, "ls\n"); ok {
			t.Fatalf("a revoked grant still signed in: %s", out)
		}
	})

	t.Run("the ledger records the issue, the start and the end", func(t *testing.T) {
		waitFor(t, "every session to end", func() bool { return r.openSessions() == 0 })
		waitFor(t, "the end entries", func() bool {
			actions := auditActions(t, r.st, r.tenant)
			return len(actions["sessions.sftp.end"]) > 0 && len(actions["sessions.sftp.end"]) == len(actions["sessions.sftp.start"])
		})
		actions := auditActions(t, r.st, r.tenant)
		if len(actions["sessions.sftp.issue"]) < 4 || len(actions["sessions.sftp.start"]) == 0 {
			t.Fatalf("ledger actions = issue %d, start %d, end %d", len(actions["sessions.sftp.issue"]), len(actions["sessions.sftp.start"]), len(actions["sessions.sftp.end"]))
		}
	})
}

// sftpMeta is the one thing the sign-in callback reads of a connection: the user name.
type sftpMeta struct {
	ssh.ConnMetadata
	user string
}

func (m sftpMeta) User() string { return m.user }

// The sign-in callback runs for a query about a key and again for the signed request, and a client may
// ask about another key in between. The connection must serve the credential of the key that signed,
// never the one that was asked about last.
func TestSFTPSignInServesTheCredentialOfTheKeyThatSigned(t *testing.T) {
	r := newSFTPRig(t)
	public := func(keyFile string) ssh.PublicKey {
		t.Helper()
		b, err := os.ReadFile(keyFile)
		if err != nil {
			t.Fatal(err)
		}
		signer, err := ssh.ParsePrivateKey(b)
		if err != nil {
			t.Fatal(err)
		}
		return signer.PublicKey()
	}
	person := func() api.ModuleContext { return r.person(func(auth.TrusteeRight) bool { return false }) }
	keyA, _ := r.connect(person())
	keyV, _ := r.connect(person())
	a, v := public(keyA), public(keyV)
	want := r.svc.credential(a)
	if want == nil || want == r.svc.credential(v) {
		t.Fatal("the two keys must open two different credentials")
	}

	conf := r.svc.config()
	var signed *ssh.Permissions
	for i, key := range []ssh.PublicKey{a, v} { // the client asks about a, then about v, then signs with a
		perms, err := conf.PublicKeyCallback(sftpMeta{user: sftpUser}, key)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			signed = perms // what the library hands back when a signs
		}
	}
	if got := r.svc.credentialOf(signed); got != want {
		t.Fatal("the connection would serve the credential of the key that was asked about last, not of the key that signed")
	}
}
