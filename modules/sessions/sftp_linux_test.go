// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
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
	f       *streamConfinementFixture
	admin   string
	people  *int // shared by the copies of the rig, so every person has a new email
}

// useTestSFTPServer requires the real OpenSSH tools in CI, skips locally when they are
// missing, and points the listener at the server it found.
func useTestSFTPServer(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sftp"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("the sftp client is required in CI: install openssh-client")
		}
		t.Skip("the sftp client is not installed")
	}
	server := os.Getenv("OLIVARES_TEST_SFTP_SERVER")
	if server == "" {
		found, err := findSFTPServer()
		if err != nil {
			if os.Getenv("CI") != "" {
				t.Fatal("sftp-server is required in CI: install openssh-sftp-server")
			}
			t.Skip("no sftp-server: set OLIVARES_TEST_SFTP_SERVER or install openssh-sftp-server")
		}
		server = found
	}
	saved := sftpServerPaths
	sftpServerPaths = []string{server}
	t.Cleanup(func() { sftpServerPaths = saved })
}

// TestSFTPToolsRequiredInCI runs the real prerequisite check with no external tools.
func TestSFTPToolsRequiredInCI(t *testing.T) {
	if os.Getenv("OLIVARES_TEST_MISSING_SFTP") == "1" {
		t.Setenv("CI", "true")
		t.Setenv("PATH", t.TempDir())
		useTestSFTPServer(t)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestSFTPToolsRequiredInCI$", "-test.v")
	cmd.Env = append(os.Environ(), "OLIVARES_TEST_MISSING_SFTP=1")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "sftp client is required in CI") {
		t.Fatalf("missing tools must fail in CI, got %v:\n%s", err, out)
	}
}

// knownHostsOf is a client's known_hosts file holding the listener's host key.
func knownHostsOf(t *testing.T, svc *sftpService) string {
	t.Helper()
	hostLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(svc.signer.PublicKey())))
	known := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(known, []byte(fmt.Sprintf("[127.0.0.1]:%d %s\n", svc.port(), hostLine)), 0o600); err != nil {
		t.Fatal(err)
	}
	return known
}

func newSFTPRig(t *testing.T) *sftpRig {
	t.Helper()
	useTestSFTPServer(t)

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

	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	m := f.m
	WithConfinement([]string{t.TempDir()}, false)(m)
	WithSFTP("127.0.0.1:0", filepath.Join(t.TempDir(), "host-key"))(m)
	m.CommunicationAuthority = NewCommunicationRequestAuthority(f.authr, f.authz)
	admin := f.adminLogin()
	tenant := f.createOrg(admin, "sftp-rig")
	folder := mkWorkspace(t, m, tenant, CreateWorkspaceParams{RootPath: root})
	m.startSFTP()
	svc := m.sftp.Load()
	if svc == nil {
		t.Fatal("the listener did not start")
	}
	return &sftpRig{t: t, m: m, st: f.st, tenant: tenant, folder: folder, root: root, outside: outside,
		beside: filepath.Join(base, "secret.txt"), known: knownHostsOf(t, svc), svc: svc, f: f, admin: admin, people: new(int)}
}

// person is a signed-in member who holds every right on the folder except those deny returns true for.
func (r *sftpRig) person(deny func(auth.TrusteeRight) bool) api.ModuleContext {
	r.t.Helper()
	*r.people++
	token := r.f.member(r.t, r.admin, r.tenant, fmt.Sprintf("sftp-rig-%d@test.io", *r.people), model.ID(""))
	principal, err := r.f.authr.Authenticate(context.Background(), token)
	if err != nil {
		r.t.Fatal(err)
	}
	principal.AAL = 3 // the handler is called directly, past the route that asks for the step-up
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

// ask calls one SFTP handler of the folder as mc does.
func (r *sftpRig) ask(handler func(http.ResponseWriter, *http.Request, api.ModuleContext), mc api.ModuleContext) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("ref", r.folder.WorkspaceRef)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	rec := httptest.NewRecorder()
	handler(rec, req, mc)
	return rec
}

// connect asks for a credential as mc does and writes the private key to a file.
func (r *sftpRig) connect(mc api.ModuleContext) (keyFile string, readOnly bool) {
	r.t.Helper()
	rec := r.ask(r.m.handleSFTPConnect, mc)
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

	t.Run("a person who can only read gets no SFTP", func(t *testing.T) {
		// sftp-server reads the host's system files and skips the file API's size limit and DLP
		// posture, so it is offered only to a person who could open a terminal in the folder.
		reader := r.person(func(right auth.TrusteeRight) bool { return right != auth.TrusteeRead })
		for name, handler := range map[string]func(http.ResponseWriter, *http.Request, api.ModuleContext){
			"status": r.m.handleSFTPStatus, "connect": r.m.handleSFTPConnect,
		} {
			if rec := r.ask(handler, reader); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "PRIVATE KEY") {
				t.Errorf("%s for a person who can only read = %d; want 403 and no credential", name, rec.Code)
			}
		}
	})

	t.Run("a key for a folder mounted read-only reads and never writes", func(t *testing.T) {
		ro := *r
		ro.t = t
		ro.folder = mkWorkspace(t, r.m, r.tenant, CreateWorkspaceParams{MountMode: mountRO})
		ro.root = ro.folder.RootPath
		if err := os.WriteFile(filepath.Join(ro.root, "inside.txt"), []byte("read only\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var lostWrite atomic.Bool
		roKey, readOnly := ro.connect(ro.person(func(right auth.TrusteeRight) bool { return lostWrite.Load() && right == auth.TrusteeWrite }))
		if !readOnly {
			t.Fatal("a folder mounted read-only gave a credential that writes")
		}
		if out, ok := ro.sftp(roKey, fmt.Sprintf("get inside.txt %s\n", filepath.Join(local, "ro-got.txt"))); !ok {
			t.Fatalf("a read-only credential could not read: %s", out)
		}
		if out, ok := ro.sftp(roKey, fmt.Sprintf("put %s ro-up.txt\n", upload)); ok {
			t.Fatalf("a read-only credential wrote: %s", out)
		}
		if _, err := os.Stat(filepath.Join(ro.root, "ro-up.txt")); err == nil {
			t.Fatal("a read-only credential created a file")
		}
		// The key reads only, yet it was issued for the rights a terminal needs: losing one ends it.
		lostWrite.Store(true)
		if out, ok := ro.sftp(roKey, "ls\n"); ok {
			t.Fatalf("a read-only credential signed in after its holder lost the write right: %s", out)
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
		// Four credentials were issued above: every right, the read-only folder, the one made to expire, the one revoked.
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

// A person's "My files" opens over sftp and scp with no path typed, the client cannot reach
// another person's folder beside it, and an editor or the administrator is told the folder's
// SFTP does not exist, as they are told for its files.
func TestSFTPOpensInThePersonalFolder(t *testing.T) {
	useTestSFTPServer(t)
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	if err := f.m.UsePersonalFilesRoot(filepath.Join(t.TempDir(), "files")); err != nil {
		t.Fatal(err)
	}
	WithConfinement([]string{t.TempDir()}, false)(f.m)
	f.m.sftpCfg = sftpConfig{addr: "127.0.0.1:0", hostKeyPath: filepath.Join(t.TempDir(), "host-key")}
	f.m.CommunicationAuthority = NewCommunicationRequestAuthority(f.authr, f.authz)
	f.m.startSFTP()
	svc := f.m.sftp.Load()
	if svc == nil {
		t.Fatal("the listener did not start")
	}
	admin := f.adminLogin()
	tenant := f.createOrg(admin, "my-files-sftp")
	ann, bob := f.member(t, admin, tenant, "ann@test.io", model.ID("")), f.member(t, admin, tenant, "bob@test.io", model.ID(""))
	mine := func(token string) (ref, root string) {
		t.Helper()
		got := f.do("GET", "/v1/m/sessions/workspaces", token, tenantHdr(tenant))
		items, _ := got.body["items"].([]any)
		if got.code != http.StatusOK || len(items) != 1 {
			t.Fatalf("list folders: %d %s; want the person's own folder", got.code, got.raw)
		}
		item := items[0].(map[string]any)
		return item["workspace_ref"].(string), item["root_path"].(string)
	}
	annRef, annRoot := mine(ann)
	_, bobRoot := mine(bob)
	if err := os.WriteFile(filepath.Join(annRoot, "note.txt"), []byte("in my files\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bobRoot, "private.txt"), []byte("bob's\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sftpPath := "/v1/m/sessions/workspaces/" + annRef + "/sftp"

	for _, tc := range []struct{ who, token string }{{"editor", bob}, {"administrator", admin}} {
		for _, method := range []string{"GET", "POST"} {
			if got := f.do(method, sftpPath, tc.token, tenantHdr(tenant)); got.code != http.StatusNotFound {
				t.Errorf("%s %s %s = %d %s; want 404", tc.who, method, sftpPath, got.code, got.raw)
			}
		}
	}
	if t.Failed() {
		return
	}

	got := f.do("POST", sftpPath, ann, tenantHdr(tenant))
	private, _ := got.body["private_key"].(string)
	if got.code != http.StatusCreated || !strings.Contains(private, "PRIVATE KEY") {
		t.Fatalf("owner connect = %d %s; want a credential", got.code, got.raw)
	}
	if readOnly, _ := got.body["read_only"].(bool); readOnly {
		t.Fatal("the owner's credential is read-only; she holds every right on her own folder")
	}
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte(private), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &sftpRig{t: t, svc: svc, known: knownHostsOf(t, svc)}
	local := t.TempDir()
	up := filepath.Join(local, "up.txt")
	if err := os.WriteFile(up, []byte("from the client\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, ok := r.sftp(key, "pwd\nls\nget note.txt "+filepath.Join(local, "note.txt")+"\nput "+up+" up.txt\n")
	if !ok || !strings.Contains(out, annRoot) || !strings.Contains(out, "note.txt") {
		t.Fatalf("sftp did not open in My files %s (ok=%v):\n%s", annRoot, ok, out)
	}
	if b, err := os.ReadFile(filepath.Join(local, "note.txt")); err != nil || string(b) != "in my files\n" {
		t.Errorf("downloaded note = %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(annRoot, "up.txt")); err != nil || string(b) != "from the client\n" {
		t.Errorf("uploaded file in My files = %q, %v", b, err)
	}

	// scp rides the same listener: a relative name is a file of the folder.
	scp := exec.Command("scp", "-F", "/dev/null", "-i", key, "-P", strconv.Itoa(svc.port()),
		"-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "UserKnownHostsFile="+r.known,
		"-o", "StrictHostKeyChecking=yes", sftpUser+"@127.0.0.1:note.txt", filepath.Join(local, "scp-note.txt"))
	if b, err := scp.CombinedOutput(); err != nil {
		t.Fatalf("scp of a file of My files: %v\n%s", err, b)
	}
	if b, err := os.ReadFile(filepath.Join(local, "scp-note.txt")); err != nil || string(b) != "in my files\n" {
		t.Errorf("scp note = %q, %v", b, err)
	}

	// Bob's folder is the next directory over: the kernel, not a path check, refuses it.
	if out, ok := r.sftp(key, "get ../"+filepath.Base(bobRoot)+"/private.txt "+filepath.Join(local, "stolen.txt")+"\n"); ok || !strings.Contains(out, "Permission denied") {
		t.Errorf("sftp in ann's folder reading bob's (ok=%v), want Permission denied:\n%s", ok, out)
	}
	if _, err := os.Stat(filepath.Join(local, "stolen.txt")); err == nil {
		t.Error("bob's file reached ann's client")
	}
}

// The rights a credential is asked again at every sign-in, session start and recheck are the
// person's current ones, not the ones read when Connect issued it: once the person signs out,
// is disabled or leaves the organization, neither an unused credential nor an open session opens
// the folder any more.
func TestSFTPRightsReadThePersonAgain(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	if err := f.m.UsePersonalFilesRoot(filepath.Join(t.TempDir(), "files")); err != nil {
		t.Fatal(err)
	}
	WithConfinement([]string{t.TempDir()}, false)(f.m)
	f.m.CommunicationAuthority = NewCommunicationRequestAuthority(f.authr, f.authz)
	// The listener and its host key, without the sftp-server child: the rights check is the
	// subject here, and it needs neither the program nor Landlock.
	signer, err := loadOrCreateHostKey(filepath.Join(t.TempDir(), "host-key"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	svc := &sftpService{m: f.m, signer: signer, ln: ln, creds: map[string]*sftpCredential{}, open: map[*sftpSession]struct{}{}}
	f.m.sftp.Store(svc)
	admin := f.adminLogin()
	tenant := f.createOrg(admin, "sftp-current-person")
	ctx := context.Background()
	userOf := func(t *testing.T, token string) model.ID {
		t.Helper()
		p, err := f.authr.Authenticate(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		return p.UserID
	}

	for i, tc := range []struct {
		name string
		lose func(t *testing.T, token string)
	}{
		{"signed out", func(t *testing.T, token string) {
			if got := f.do("POST", "/v1/auth/logout", token, nil); got.code != http.StatusNoContent {
				t.Fatalf("logout = %d %s", got.code, got.raw)
			}
		}},
		{"account disabled", func(t *testing.T, token string) {
			user := userOf(t, token)
			if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
				u, err := as.Users().Get(ctx, user)
				if err != nil {
					return err
				}
				u.Status = model.StatusInactive
				_, err = as.Users().Update(ctx, u)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{"membership removed", func(t *testing.T, token string) {
			user := userOf(t, token)
			if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
				all, _, err := as.Memberships().List(ctx, model.Query{Limit: 100})
				if err != nil {
					return err
				}
				for _, ms := range all {
					if ms.UserID == user && ms.TargetTenantID == tenant {
						return as.Memberships().Delete(ctx, ms.ID)
					}
				}
				return fmt.Errorf("no membership of %s in the tenant", user)
			}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := f.member(t, admin, tenant, fmt.Sprintf("sftp-person-%d@test.io", i), model.ID(""))
			listed := f.do("GET", "/v1/m/sessions/workspaces", token, tenantHdr(tenant))
			items, _ := listed.body["items"].([]any)
			if listed.code != http.StatusOK || len(items) != 1 {
				t.Fatalf("list folders: %d %s; want the person's own folder", listed.code, listed.raw)
			}
			ref := items[0].(map[string]any)["workspace_ref"].(string)
			got := f.do("POST", "/v1/m/sessions/workspaces/"+ref+"/sftp", token, tenantHdr(tenant))
			private, _ := got.body["private_key"].(string)
			if got.code != http.StatusCreated {
				t.Fatalf("connect = %d %s", got.code, got.raw)
			}
			key, err := ssh.ParsePrivateKey([]byte(private))
			if err != nil {
				t.Fatal(err)
			}
			cred := svc.credential(key.PublicKey())
			if cred == nil {
				t.Fatal("the issued key opens no credential")
			}
			if err := f.m.sftpRights(cred); err != nil {
				t.Fatalf("before the change the person's credential is refused: %v", err)
			}
			tc.lose(t, token)
			if err := f.m.sftpRights(cred); err == nil {
				t.Fatalf("%s: the credential still opens the folder with the rights read when it was issued", tc.name)
			}
		})
	}
}
