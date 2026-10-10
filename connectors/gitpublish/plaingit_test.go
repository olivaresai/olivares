// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParsePlainRemote(t *testing.T) {
	for _, c := range []struct {
		name, raw string
		want      PlainRemote
		refuse    bool
	}{
		{name: "ssh with port", raw: "ssh://git@lan.example:2222/srv/git/tools.git", want: PlainRemote{URL: "ssh://git@lan.example:2222/srv/git/tools.git", Scheme: "ssh", Host: "lan.example", Path: "srv/git/tools.git", User: "git"}},
		{name: "ssh ip literal", raw: "ssh://git@10.0.0.7/repo/acme.git", want: PlainRemote{Scheme: "ssh", Host: "10.0.0.7", Path: "repo/acme.git", User: "git"}},
		{name: "ssh default port", raw: "ssh://git@host.example/srv/repo.git", want: PlainRemote{Scheme: "ssh", Host: "host.example", Path: "srv/repo.git", User: "git"}},
		{name: "uppercase host", raw: "ssh://git@Lan.Example/srv/tools.git", want: PlainRemote{Scheme: "ssh", Host: "lan.example", Path: "srv/tools.git", User: "git"}},
		{name: "https", raw: "https://git.example.com/acme/tools.git", want: PlainRemote{Scheme: "https", Host: "git.example.com", Path: "acme/tools.git"}},
		{name: "ssh without user", raw: "ssh://lan.example/srv/git/tools.git", refuse: true},
		{name: "ssh with password", raw: "ssh://git:pw@lan.example/srv/git/tools.git", refuse: true},
		{name: "ssh user as an option", raw: "ssh://-oProxyCommand=x@lan.example/srv/git/tools.git", refuse: true},
		{name: "ssh user with an at sign", raw: "ssh://git@evil@lan.example/srv/git/tools.git", refuse: true},
		{name: "uppercase scheme", raw: "SSH://git@lan.example/srv/git/tools.git", refuse: true},
		{name: "force query", raw: "ssh://git@lan.example/srv/git/tools.git?", refuse: true},
		{name: "ssh port zero", raw: "ssh://git@lan.example:0/srv/git/tools.git", refuse: true},
		{name: "ssh port too high", raw: "ssh://git@lan.example:70000/srv/git/tools.git", refuse: true},
		{name: "ssh query", raw: "ssh://git@lan.example/srv/git/tools.git?x=1", refuse: true},
		{name: "ssh one-segment path", raw: "ssh://git@lan.example/repo.git", refuse: true},
		{name: "https one-segment path", raw: "https://git.example.com/repo.git", refuse: true},
		{name: "ssh tilde in path", raw: "ssh://git@lan.example/~git/tools.git", refuse: true},
		{name: "ssh at sign in path", raw: "ssh://git@lan.example/srv/@tools.git", refuse: true},
		{name: "ssh plus in path", raw: "ssh://git@lan.example/srv/tools+x.git", refuse: true},
		{name: "ssh empty path", raw: "ssh://git@lan.example/", refuse: true},
		{name: "double slash in path", raw: "ssh://git@lan.example/srv//tools.git", refuse: true},
		{name: "dot segment", raw: "ssh://git@lan.example/srv/../tools.git", refuse: true},
		{name: "trailing dot segment", raw: "https://git.example.com/acme/.", refuse: true},
		{name: "https userinfo", raw: "https://u:p@git.example.com/acme/tools.git", refuse: true},
		{name: "https ip literal", raw: "https://10.0.0.7/acme/tools.git", refuse: true},
		{name: "https other port", raw: "https://git.example.com:8443/acme/tools.git", refuse: true},
		{name: "http", raw: "http://git.example.com/acme/tools.git", refuse: true},
		{name: "file", raw: "file:///srv/git/tools.git", refuse: true},
		{name: "scp-like", raw: "git@lan.example:srv/git/tools.git", refuse: true},
		{name: "empty", raw: "", refuse: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParsePlainRemote(c.raw)
			if c.refuse {
				if err == nil || !errors.Is(err, ErrEndpoint) {
					t.Fatalf("err = %v, want ErrEndpoint", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c.want.URL = c.raw
			if got != c.want {
				t.Fatalf("remote = %+v, want %+v", got, c.want)
			}
		})
	}
}

// newPlainFixture is one plain-git adapter over the file test transport, with
// the gitFixture's populated server and target repositories.
func newPlainFixture(t *testing.T) (*gitFixture, *PlainGit, Token) {
	t.Helper()
	f := newGitFixture(t)
	// The bare target's HEAD must name the branch that exists: a git whose
	// default initial branch differs leaves HEAD unborn, and the adapter then
	// rightly refuses to name a default branch.
	run(t, f.target, "symbolic-ref", "HEAD", "refs/heads/main")
	p, err := NewPlainGit(PlainGitConfig{Remote: "file://" + f.target, Credential: NewSecret("test-credential")}, f.x)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := p.Mint(context.Background(), EffectPush)
	if err != nil {
		t.Fatal(err)
	}
	return f, p, tok
}

func TestPlainGitReadsTheRemote(t *testing.T) {
	f, p, tok := newPlainFixture(t)
	ctx := context.Background()
	info, err := p.Branch(ctx, tok, "olivares/agent")
	if err != nil || info.Default != "main" || info.Exists {
		t.Fatalf("branch = %+v %v, want default main and no such branch", info, err)
	}
	o, err := p.Ref(ctx, tok, "main")
	if err != nil || o.State != RefPresent || o.SHA != f.base {
		t.Fatalf("ref main = %+v %v, want %s", o, err, f.base)
	}
	o, err = p.Ref(ctx, tok, "olivares/agent")
	if err != nil || o.State != RefNotFoundOrHidden {
		t.Fatalf("absent ref = %+v %v", o, err)
	}
	if info, err := p.Branch(ctx, tok, "main"); err != nil || !info.Exists || info.Default != "main" {
		t.Fatalf("branch main = %+v %v, want existing default", info, err)
	}
}

func TestPlainGitRefusesDefaultBranchItCannotName(t *testing.T) {
	f := newGitFixture(t)
	dir := t.TempDir()
	unborn := filepath.Join(dir, "unborn.git")
	if err := f.x.InitManaged(context.Background(), unborn); err != nil {
		t.Fatal(err)
	}
	p, err := NewPlainGit(PlainGitConfig{Remote: "file://" + unborn, Credential: NewSecret("test-credential")}, f.x)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := p.Mint(context.Background(), EffectPush)
	if err != nil {
		t.Fatal(err)
	}
	// An empty remote advertises no HEAD symref: the default branch cannot be
	// named, and the read refuses instead of answering "unprotected".
	if _, err := p.Branch(context.Background(), tok, "olivares/agent"); !errors.Is(err, ErrLookupIncomplete) {
		t.Fatalf("err = %v, want ErrLookupIncomplete", err)
	}
}

func TestPlainGitChangeEffectsNotOffered(t *testing.T) {
	_, p, tok := newPlainFixture(t)
	if _, err := p.CommitTree(context.Background(), tok, "x"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("commit tree err = %v", err)
	}
	if _, err := p.GetChange(context.Background(), tok, 1); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("get change err = %v", err)
	}
	if _, err := p.OpenChanges(context.Background(), tok, "a", "b"); !errors.Is(err, ErrLookupIncomplete) {
		t.Fatalf("open changes err = %v", err)
	}
	if _, res := p.CreateChange(context.Background(), tok, ChangeSpec{Head: "a", Base: "b", Title: "t"}); res.Class != Rejected || res.Reason != "unsupported_effect" {
		t.Fatalf("create change = %+v, want rejected unsupported_effect", res)
	}
	if _, res := p.MergeChange(context.Background(), tok, 1, "x", "merge"); res.Class != Rejected || res.Reason != "unsupported_effect" {
		t.Fatalf("merge change = %+v, want rejected unsupported_effect", res)
	}
}

func TestPlainGitPushTarget(t *testing.T) {
	f := newGitFixture(t)
	https, err := NewPlainGit(PlainGitConfig{Remote: "https://git.example.com/acme/tools.git", Credential: NewSecret("user:token")}, f.x)
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := https.Mint(context.Background(), EffectPush)
	u, scheme, hdr := https.PushTarget(tok)
	if u != "https://git.example.com/acme/tools.git" || scheme != "https" || hdr.IsZero() {
		t.Fatalf("https target = %q %q %v", u, scheme, hdr)
	}
	// dXNlcjp0b2tlbg== is base64("user:token").
	if got := hdr.Reveal(); got != "Authorization: Basic dXNlcjp0b2tlbg==" {
		t.Fatalf("header = %q", got)
	}

	ssh, err := NewPlainGit(PlainGitConfig{Remote: "ssh://git@lan.example:2222/srv/git/tools.git", Credential: NewSecret("-----BEGIN " + "OPENSSH PRIVATE KEY-----")}, f.x)
	if err != nil {
		t.Fatal(err)
	}
	u, scheme, hdr = ssh.PushTarget(tok)
	if u != "ssh://git@lan.example:2222/srv/git/tools.git" || scheme != "ssh" || !hdr.IsZero() {
		t.Fatalf("ssh target = %q %q %v", u, scheme, hdr)
	}
}

func TestNewPlainGitRefusesBadBindings(t *testing.T) {
	f := newGitFixture(t)
	key := NewSecret("-----BEGIN " + "OPENSSH PRIVATE KEY-----")
	for _, c := range []struct {
		name string
		cfg  PlainGitConfig
		x    *Executor
	}{
		{"no executor", PlainGitConfig{Remote: "ssh://git@h.example/r.git", Credential: key}, nil},
		{"no credential", PlainGitConfig{Remote: "ssh://git@h.example/r.git"}, f.x},
		{"https credential with control characters", PlainGitConfig{Remote: "https://git.example.com/acme/tools.git", Credential: NewSecret("user:tok\r\nX: y")}, f.x},
		{"https credential without user", PlainGitConfig{Remote: "https://git.example.com/acme/tools.git", Credential: NewSecret("tokenonly")}, f.x},
		{"ssh credential is not a key", PlainGitConfig{Remote: "ssh://git@h.example/r.git", Credential: NewSecret("user:token")}, f.x},
		{"unknown scheme", PlainGitConfig{Remote: "git://git@h.example/r.git", Credential: key}, f.x},
		{"file remote with query", PlainGitConfig{Remote: "file:///r.git?x=1", Credential: NewSecret("t")}, f.x},
		{"file remote with dot segment", PlainGitConfig{Remote: "file:///r/../r.git", Credential: NewSecret("t")}, f.x},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewPlainGit(c.cfg, c.x); err == nil {
				t.Fatal("binding accepted")
			}
		})
	}
}

func TestExecutorLsRemoteAdmission(t *testing.T) {
	f := newGitFixture(t)
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		r      RemoteRef
		refuse bool
	}{
		{"file read", RemoteRef{URL: "file://" + f.target, Scheme: "file"}, false},
		{"ssh without key", RemoteRef{URL: "ssh://git@h.example/r.git", Scheme: "ssh"}, true},
		{"ssh url without user", RemoteRef{URL: "ssh://h.example/r.git", Scheme: "ssh", Key: NewSecret("-----BEGIN " + "OPENSSH PRIVATE KEY-----")}, true},
		{"scheme mismatch", RemoteRef{URL: "file://" + f.target, Scheme: "https"}, true},
		{"unadmitted scheme", RemoteRef{URL: "git://h.example/r.git", Scheme: "git"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := f.x.LsRemote(ctx, c.r, false, "refs/heads/main")
			if c.refuse {
				if !errors.Is(err, ErrDestination) {
					t.Fatalf("err = %v, want ErrDestination", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, f.base) {
				t.Fatalf("ls-remote output = %q, want the main head %s", out, f.base)
			}
		})
	}
}

func TestExecutorPushSSHURLAdmission(t *testing.T) {
	f := newGitFixture(t)
	key := NewSecret("-----BEGIN " + "OPENSSH PRIVATE KEY-----")
	for _, c := range []struct{ name, url string }{
		{"ssh url without user", "ssh://h.example/r.git"},
		{"ssh url with password", "ssh://git:pw@h.example/r.git"},
		{"ssh url user as an option", "ssh://-oProxyCommand=x@h.example/r.git"},
		{"ssh port zero", "ssh://git@h.example:0/r.git"},
		{"ssh query", "ssh://git@h.example/r.git?q=1"},
		{"ssh dot segment", "ssh://git@h.example/r/../r.git"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := f.req("refs/heads/olivares/admission", "")
			r.URL, r.Scheme, r.Key = c.url, "ssh", key
			if _, err := f.x.Push(context.Background(), r); !errors.Is(err, ErrDestination) {
				t.Fatalf("err = %v, want ErrDestination", err)
			}
			if got := f.refAt(f.target, "refs/heads/olivares/admission"); got != "" {
				t.Fatalf("ref moved to %s after a refused push", got)
			}
		})
	}
	// A push over ssh without a key never dispatches, whatever the URL.
	r := f.req("refs/heads/olivares/nokey", "")
	r.URL, r.Scheme = "ssh://git@h.example/r.git", "ssh"
	if _, err := f.x.Push(context.Background(), r); !errors.Is(err, ErrDestination) {
		t.Fatalf("keyless ssh push err = %v, want ErrDestination", err)
	}
}

// sshServer is one user-mode sshd on a loopback port, with the authorized
// key and the user key of the journey. restartWithFreshHostKey replaces the
// server with one holding a different host key on the same port: exactly what
// a host-key change looks like to the engine.
type sshServer struct {
	prefix, keyPEM string
	t              *testing.T
	sshd, dir      string
	port           int
	authKeys       string
	cmd            *exec.Cmd
}

func (s *sshServer) start(hostKey string) {
	s.t.Helper()
	cmd := exec.Command(s.sshd,
		"-p", strconv.Itoa(s.port), "-h", hostKey,
		"-o", "AuthorizedKeysFile="+s.authKeys,
		"-o", "StrictModes=no",
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "PubkeyAuthentication=yes",
		"-o", "UsePAM=no",
		"-o", "PidFile="+filepath.Join(s.dir, "sshd.pid"),
		"-o", "LogLevel=ERROR",
		"-D",
	)
	if err := cmd.Start(); err != nil {
		s.t.Skipf("sshd did not start: %v", err)
	}
	s.cmd = cmd
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", s.port), time.Second); err == nil {
			c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.t.Fatal("sshd never answered on its port")
}

func (s *sshServer) restartWithFreshHostKey() {
	s.t.Helper()
	if err := s.cmd.Process.Kill(); err != nil {
		s.t.Fatal(err)
	}
	_ = s.cmd.Wait()
	next := filepath.Join(s.dir, "host_next_ed25519")
	out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "rotated", "-f", next).CombinedOutput()
	if err != nil {
		s.t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	s.start(next)
}

// sshJourney starts one user-mode sshd on a loopback port with a fresh host
// key and an authorized key for the current user. It skips when the ssh
// server, client or key generator is not installed on this host.
func sshJourney(t *testing.T, remoteGit string) *sshServer {
	t.Helper()
	sshd := ""
	if p, err := exec.LookPath("sshd"); err == nil {
		sshd = p
	} else {
		for _, p := range []string{"/usr/sbin/sshd", "/usr/local/sbin/sshd"} {
			if st, e := os.Stat(p); e == nil && !st.IsDir() {
				sshd = p
				break
			}
		}
	}
	if sshd == "" {
		t.Skip("no sshd on this host; the ssh transport leg runs where one is installed (the builder)")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh client on this host")
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen on this host")
	}
	u, err := user.Current()
	if err != nil || u.Username == "" {
		t.Skip("cannot name the current user for the ssh journey")
	}
	dir := t.TempDir()
	mustRun := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	key, hostKey := filepath.Join(dir, "id_ed25519"), filepath.Join(dir, "host_ed25519")
	mustRun("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "gitpublish-test", "-f", key)
	mustRun("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "gitpublish-test-host", "-f", hostKey)
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	authKeys := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(authKeys, pub, 0o600); err != nil {
		t.Fatal(err)
	}
	mustRun("git", "init", "-q", "--bare", "-b", "main", remoteGit)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	pem, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	s := &sshServer{t: t, sshd: sshd, dir: dir, authKeys: authKeys, port: port,
		prefix: "ssh://" + u.Username + "@127.0.0.1:" + strconv.Itoa(port), keyPEM: string(pem)}
	s.start(hostKey)
	t.Cleanup(func() {
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
	})
	return s
}

// TestExecutorAdmitsOnlyANamedSSHHost: the closed boundary itself refuses a
// host that is not a DNS name or an IP literal (a leading dash would read as
// an ssh option), whoever the caller is; a mixed-case name still passes.
func TestExecutorAdmitsOnlyANamedSSHHost(t *testing.T) {
	for _, c := range []struct {
		url string
		ok  bool
	}{
		{"ssh://git@lan.example/srv/tools.git", true},
		{"ssh://git@Lan.Example:2222/srv/tools.git", true},
		{"ssh://git@10.0.0.7/srv/tools.git", true},
		{"ssh://git@[::1]/srv/tools.git", true},
		{"ssh://git@-lan.example/srv/tools.git", false},
		{"ssh://git@lan_host.example/srv/tools.git", false},
		{"ssh://git@lan..example/srv/tools.git", false},
	} {
		err := admittedRemote("ssh", c.url)
		if c.ok != (err == nil) {
			t.Errorf("admittedRemote(ssh, %q) = %v, want ok=%v", c.url, err, c.ok)
		}
	}
}

// keyFilesLeft reports the ssh key files an executor home still holds.
func keyFilesLeft(home string) []string {
	matches, _ := filepath.Glob(filepath.Join(home, "key-*"))
	return matches
}

// TestPlainGitPushesToABareRepoOverSSH is the exit journey: an agent branch
// pushed to a bare repository on an SSH host through the closed executor,
// with the binding's own key and a first-use host key. The wrong key never
// authenticates, a changed host key is refused, and no key file outlives an
// invocation.
func TestPlainGitPushesToABareRepoOverSSH(t *testing.T) {
	f := newGitFixture(t)
	remoteGit := filepath.Join(t.TempDir(), "remote.git")
	srv := sshJourney(t, remoteGit)
	// The remote starts at the base commit, as a real one would.
	run(t, f.server, "push", "-q", remoteGit, f.base+":refs/heads/main")
	ctx := context.Background()
	p, err := NewPlainGit(PlainGitConfig{Remote: srv.prefix + remoteGit, Credential: NewSecret(srv.keyPEM)}, f.x)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := p.Mint(ctx, EffectPush)
	if err != nil {
		t.Fatal(err)
	}
	info, err := p.Branch(ctx, tok, "olivares/ssh")
	if err != nil || info.Default != "main" || info.Exists {
		t.Fatalf("branch = %+v %v, want default main, no olivares/ssh", info, err)
	}
	o, err := p.Ref(ctx, tok, "main")
	if err != nil || o.State != RefPresent || o.SHA != f.base {
		t.Fatalf("ref main = %+v %v, want %s", o, err, f.base)
	}
	// The product stores a key the way `secrets set --value-file` does: the
	// trailing newline trimmed. OpenSSH rejects a private key without one, so
	// the executor must restore it.
	trimmed, err := NewPlainGit(PlainGitConfig{Remote: srv.prefix + remoteGit, Credential: NewSecret(strings.TrimRight(srv.keyPEM, "\r\n"))}, f.x)
	if err != nil {
		t.Fatal(err)
	}
	trimmedTok, _ := trimmed.Mint(ctx, EffectPush)
	if _, err := trimmed.Branch(ctx, trimmedTok, "olivares/ssh"); err != nil {
		t.Fatalf("key stored without its trailing newline = %v, want a read", err)
	}
	// The engine home is a data directory like any other: whatever it holds
	// must reach neither the shell git runs the ssh line through nor ssh's own
	// option parser.
	for _, name := range []string{"data dir", "dollar $HOME", "tick `x`", "quote \"q\"", "percent %h", "back\\slash"} {
		t.Run("engine home "+name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), name)
			if err := os.Mkdir(home, 0o700); err != nil {
				t.Fatal(err)
			}
			x, err := NewExecutor(gitPath(t), home)
			if err != nil {
				t.Fatal(err)
			}
			odd, err := NewPlainGit(PlainGitConfig{Remote: srv.prefix + remoteGit, Credential: NewSecret(srv.keyPEM)}, x)
			if err != nil {
				t.Fatal(err)
			}
			oddTok, _ := odd.Mint(ctx, EffectPush)
			if _, err := odd.Branch(ctx, oddTok, "olivares/ssh"); err != nil {
				t.Fatalf("read = %v, want ok", err)
			}
			// ssh must have pinned the host key in THAT home, not split the path.
			if _, err := os.Stat(filepath.Join(home, "known_hosts")); err != nil {
				t.Fatalf("host key not pinned under the engine home: %v", err)
			}
		})
	}
	res, err := f.x.Push(ctx, PushRequest{RepoPath: f.server, URL: srv.prefix + remoteGit, Scheme: "ssh", Key: tok.Value(), Ref: "refs/heads/olivares/ssh", Commit: f.commit})
	if err != nil || res.Class != Applied {
		t.Fatalf("ssh push = %+v %v", res, err)
	}
	if got := f.refAt(remoteGit, "refs/heads/olivares/ssh"); got != f.commit {
		t.Fatalf("remote branch = %s, want %s", got, f.commit)
	}
	if left := keyFilesLeft(f.x.home); len(left) != 0 {
		t.Fatalf("key files left behind: %v", left)
	}
	// A key the server does not know never reads the remote.
	wrong := NewSecret("-----BEGIN " + "OPENSSH PRIVATE KEY-----\nnot-the-binding-key\n-----END OPENSSH PRIVATE KEY-----\n")
	bad, err := NewPlainGit(PlainGitConfig{Remote: srv.prefix + remoteGit, Credential: wrong}, f.x)
	if err != nil {
		t.Fatal(err)
	}
	badTok, _ := bad.Mint(ctx, EffectPush)
	if _, err := bad.Branch(ctx, badTok, "main"); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("wrong key = %v, want ErrHostUnavailable", err)
	}
	// The first contact pinned the host key; a changed one is refused.
	if _, err := os.Stat(filepath.Join(f.x.home, "known_hosts")); err != nil {
		t.Fatalf("host key not pinned: %v", err)
	}
	srv.restartWithFreshHostKey()
	if _, err := p.Branch(ctx, tok, "main"); !errors.Is(err, ErrHostUnavailable) {
		t.Fatalf("changed host key = %v, want ErrHostUnavailable", err)
	}
	if left := keyFilesLeft(f.x.home); len(left) != 0 {
		t.Fatalf("key files left behind: %v", left)
	}
}
