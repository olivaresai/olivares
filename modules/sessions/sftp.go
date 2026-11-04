// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// sftp.go lets a person copy files in and out of a folder with sftp and scp.
//
// The engine speaks SSH (golang.org/x/crypto/ssh) and nothing of SFTP: the protocol is
// served by the operating system's sftp-server, started as a child confined to the folder
// by the Landlock policy a terminal in that folder gets, so a path outside the folder is
// refused by the kernel. The person's right to the folder is asked of the same door as the
// file API, when the credential is issued, when a connection is made and every
// sftpRecheckInterval while a session is open. The design record is
// docs/design/workspace-sftp-v1.md.

const (
	// sftpUser is the one user name a client sends; the key names the folder.
	sftpUser = "olivares"
	// sftpCredentialTTL is how long a credential can open a connection.
	sftpCredentialTTL = 10 * time.Minute
	// sftpMaxPerPerson bounds the open sessions and the live credentials of one person.
	sftpMaxPerPerson = 5
	// sftpMaxConnections bounds the connections the listener holds, signed in or not.
	sftpMaxConnections = 64
	// sftpHandshakeLimit is how long a connection has to sign in and open a session.
	sftpHandshakeLimit = 20 * time.Second
)

// ponytail: the limits below are fixed, not settings; upgrade trigger: an administrator
// who needs another idle time or lifetime than 15 minutes and 8 hours.
var (
	sftpRecheckInterval = 30 * time.Second
	sftpIdleTimeout     = 15 * time.Minute
	sftpMaxLifetime     = 8 * time.Hour
)

// sftpServerPaths are where the operating system's sftp-server lives, by distribution.
var sftpServerPaths = []string{
	"/usr/lib/openssh/sftp-server",
	"/usr/libexec/openssh/sftp-server",
	"/usr/lib/ssh/sftp-server",
	"/usr/libexec/sftp-server",
}

var errSFTPRefused = errors.New("sftp: access refused")

// sftpConfig is what the composition root hands the module: the address to listen on and
// the file that keeps the host key. An empty address leaves the subsystem off.
type sftpConfig struct{ addr, hostKeyPath string }

// WithSFTP turns the SFTP and SCP access to a folder on, listening on addr (host:port) with
// its host key kept in hostKeyPath. Unset, nothing listens and the Connect card stays hidden.
func WithSFTP(addr, hostKeyPath string) Option {
	return func(m *Module) { m.sftpCfg = sftpConfig{addr: addr, hostKeyPath: hostKeyPath} }
}

// sftpCredential is what one Connect left in the engine: who may open which folder, how, until
// when. mc holds only what the rights check reads, never the request.
type sftpCredential struct {
	mc        api.ModuleContext
	who       terminalWho
	folderRef string
	folderID  model.ID
	readOnly  bool
	expires   time.Time
}

func (c *sftpCredential) mode() string {
	if c.readOnly {
		return "read"
	}
	return "write"
}

// sftpChild is the confined sftp-server of one session.
type sftpChild struct {
	cmd         *exec.Cmd
	stdin       *os.File
	stdout      *os.File
	cancel      context.CancelFunc
	release     func()
	confinement confine.State
}

// sftpService is the listener and what it holds: the live credentials and the open sessions.
type sftpService struct {
	m       *Module
	program string
	signer  ssh.Signer
	ln      net.Listener

	mu      sync.Mutex
	creds   map[string]*sftpCredential // by the marshaled public key
	open    map[*sftpSession]struct{}
	stopped bool
	conns   int
	wg      sync.WaitGroup
}

func (m *Module) startSFTP() {
	cfg := m.sftpCfg
	if cfg.addr == "" {
		return
	}
	s, err := newSFTPService(m, cfg)
	if err != nil {
		m.warnf("sessions: SFTP is not available on this server; the Connect card stays hidden", "addr", cfg.addr, "err", redactErr(err))
		return
	}
	m.sftp.Store(s)
}

func (m *Module) stopSFTP() {
	if s := m.sftp.Swap(nil); s != nil {
		s.stop()
	}
}

func newSFTPService(m *Module, cfg sftpConfig) (*sftpService, error) {
	program, err := findSFTPServer()
	if err != nil {
		return nil, err
	}
	if state := confine.Probe(); state.Mode != confine.ModeLandlock {
		return nil, errors.New("this host cannot confine a child to a folder: " + state.Reason)
	}
	signer, err := loadOrCreateHostKey(cfg.hostKeyPath)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return nil, err
	}
	s := &sftpService{m: m, program: program, signer: signer, ln: ln,
		creds: map[string]*sftpCredential{}, open: map[*sftpSession]struct{}{}}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

func findSFTPServer() (string, error) {
	for _, p := range sftpServerPaths {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	return "", errors.New("sftp-server is not installed (the openssh-sftp-server package)")
}

// loadOrCreateHostKey reads the Ed25519 host key, or makes it on first use. It is kept in the
// data directory, which no confined child reaches.
func loadOrCreateHostKey(path string) (ssh.Signer, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("the SFTP host key needs an absolute path")
	}
	if b, err := os.ReadFile(path); err == nil {
		return ssh.ParsePrivateKey(b)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "olivares sftp host key")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(pem.EncodeToMemory(block)); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}

func (s *sftpService) port() int {
	if a, ok := s.ln.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

func (s *sftpService) fingerprint() string { return ssh.FingerprintSHA256(s.signer.PublicKey()) }

// issue makes the credential of one Connect: a key pair whose private half is returned and
// never kept. The ledger records the issue before the credential can open anything.
// Directory- or CA-signed credentials of Business replace this one call.
func (s *sftpService) issue(ctx context.Context, mc api.ModuleContext, ws *resolvedWorkspace, readOnly bool) (*sftpCredential, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "olivares "+ws.ref)
	if err != nil {
		return nil, "", err
	}
	who := terminalWhoOf(mc)
	now := time.Now()
	cred := &sftpCredential{
		mc:  api.ModuleContext{Principal: mc.Principal, Tenant: mc.Tenant, Resource: mc.Resource, Admission: mc.Admission},
		who: who, folderRef: ws.ref, folderID: ws.id, readOnly: readOnly, expires: now.Add(sftpCredentialTTL),
	}
	if err := s.m.sealSFTP(ctx, cred, "issue", nil); err != nil {
		return nil, "", &runErr{http.StatusServiceUnavailable, "no credential was issued because the audit ledger could not record it"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil, "", &runErr{http.StatusServiceUnavailable, "the server is stopping; no credential was issued"}
	}
	live := 0
	for k, c := range s.creds {
		switch {
		case now.After(c.expires):
			delete(s.creds, k)
		case c.mc.Tenant == cred.mc.Tenant && c.who == who:
			live++
		}
	}
	if live >= sftpMaxPerPerson {
		return nil, "", &runErr{http.StatusTooManyRequests, "you have " + strconv.Itoa(sftpMaxPerPerson) + " unused SFTP credentials; use one or wait for it to expire"}
	}
	s.creds[string(sshPub.Marshal())] = cred
	return cred, string(pem.EncodeToMemory(block)), nil
}

// credential is the live credential the key opens, or nil.
func (s *sftpService) credential(key ssh.PublicKey) *sftpCredential {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.creds[string(key.Marshal())]
	if c == nil || time.Now().After(c.expires) {
		return nil
	}
	return c
}

// sftpKeyExtension names the Permissions entry that carries the key a connection signed in with.
const sftpKeyExtension = "olivares-sftp-key"

// config is the server configuration of a connection. The key that signs in travels in the
// Permissions the library returns for that key: the callback also runs for a query about a key, and
// a client can ask about another key before it signs, so nothing the callback sets aside is
// the credential of the key that signed.
func (s *sftpService) config() *ssh.ServerConfig {
	conf := &ssh.ServerConfig{
		MaxAuthTries: 3,
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() != sftpUser {
				return nil, errSFTPRefused
			}
			found := s.credential(key)
			if found == nil || s.m.sftpRights(found) != nil {
				return nil, errSFTPRefused
			}
			return &ssh.Permissions{Extensions: map[string]string{sftpKeyExtension: string(key.Marshal())}}, nil
		},
	}
	conf.AddHostKey(s.signer)
	return conf
}

// credentialOf is the live credential of the key a connection signed in with, or nil when it
// is gone since (the folder was removed or the engine is stopping).
func (s *sftpService) credentialOf(perms *ssh.Permissions) *sftpCredential {
	if perms == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creds[perms.Extensions[sftpKeyExtension]]
}

func (s *sftpService) accept() {
	defer s.wg.Done()
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		refuse := s.stopped || s.conns >= sftpMaxConnections
		if !refuse {
			s.conns++
			s.wg.Add(1)
		}
		s.mu.Unlock()
		if refuse {
			_ = nc.Close()
			continue
		}
		go func() {
			defer s.wg.Done()
			defer func() { s.mu.Lock(); s.conns--; s.mu.Unlock() }()
			s.serveConn(nc)
		}()
	}
}

func (s *sftpService) serveConn(nc net.Conn) {
	_ = nc.SetDeadline(time.Now().Add(sftpHandshakeLimit))
	sconn, chans, reqs, err := ssh.NewServerConn(nc, s.config())
	if err != nil {
		_ = nc.Close()
		return
	}
	_ = nc.SetDeadline(time.Time{})
	defer sconn.Close()
	cred := s.credentialOf(sconn.Permissions)
	if cred == nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	var channels sync.WaitGroup
	var opened atomic.Int32
	// A connection that opens no session is not kept.
	idle := time.AfterFunc(sftpHandshakeLimit, func() {
		if opened.Load() == 0 {
			_ = sconn.Close()
		}
	})
	defer idle.Stop()
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only sftp sessions are served")
			continue
		}
		ch, chReqs, err := newChannel.Accept()
		if err != nil {
			continue
		}
		opened.Add(1)
		channels.Add(1)
		go func() {
			defer channels.Done()
			s.serveChannel(cred, ch, chReqs)
		}()
	}
	channels.Wait()
}

// serveChannel answers the requests of one session channel. Only the sftp subsystem is served: a
// shell, a command, a terminal, forwarding and an agent are refused.
func (s *sftpService) serveChannel(cred *sftpCredential, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	var sess *sftpSession
	for req := range reqs {
		if req.Type != "subsystem" || sess != nil {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		var name struct{ Name string }
		if ssh.Unmarshal(req.Payload, &name) != nil || name.Name != "sftp" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		started, err := s.start(cred, ch)
		if err != nil {
			_, _ = io.WriteString(ch.Stderr(), "olivares: "+sftpMessage(err)+"\n")
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			return
		}
		sess = started
		_ = req.Reply(true, nil)
		go sess.run()
	}
	if sess != nil {
		sess.close("client_closed")
		<-sess.done
	}
}

func sftpMessage(err error) string {
	var re *runErr
	if errors.As(err, &re) {
		return re.msg
	}
	return "the session could not start"
}

func (s *sftpService) stop() {
	s.mu.Lock()
	s.stopped = true
	open := make([]*sftpSession, 0, len(s.open))
	for ss := range s.open {
		open = append(open, ss)
	}
	s.creds = map[string]*sftpCredential{}
	s.mu.Unlock()
	_ = s.ln.Close()
	for _, ss := range open {
		ss.close("shutdown")
	}
	s.wg.Wait()
}

// closeFolder ends the sessions in a folder that was removed and forgets its credentials.
func (s *sftpService) closeFolder(tenant model.TenantID, ref, reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	var open []*sftpSession
	for ss := range s.open {
		if ss.cred.mc.Tenant == tenant && ss.cred.folderRef == ref {
			open = append(open, ss)
		}
	}
	for k, c := range s.creds {
		if c.mc.Tenant == tenant && c.folderRef == ref {
			delete(s.creds, k)
		}
	}
	s.mu.Unlock()
	for _, ss := range open {
		ss.close(reason)
	}
}

type sftpNeed struct {
	permission auth.Permission
	right      auth.TrusteeRight
}

// sftpRights asks, for a credential, what the file API asks for the same person: the folder is
// still there, the person can still read it and, for a credential that writes, still write,
// create and erase in it. Any failure to ask is a refusal.
func (m *Module) sftpRights(cred *sftpCredential) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := m.loadWorkspaceRec(ctx, cred.mc.Tenant, cred.folderRef); err != nil {
		return err
	}
	needs := []sftpNeed{{permWsRead, auth.TrusteeRead}}
	if !cred.readOnly {
		needs = append(needs, sftpNeed{permWsWrite, auth.TrusteeWrite}, sftpNeed{permWsWrite, auth.TrusteeCreate}, sftpNeed{permWsWrite, auth.TrusteeErase})
	}
	for _, n := range needs {
		if err := m.authorizeWorkspaceRight(ctx, cred.mc, n.permission, n.right, cred.mc.Resource); err != nil {
			return err
		}
	}
	return nil
}

// sftpSession is one open sftp-server for one person in one folder.
type sftpSession struct {
	s     *sftpService
	cred  *sftpCredential
	ch    ssh.Channel
	child *sftpChild
	begun time.Time

	last           atomic.Int64 // unix nanoseconds of the last byte either way
	in, out        atomic.Int64
	closeOnce      sync.Once
	reasonMu       sync.Mutex
	reason         string
	done           chan struct{}
	watchStop      chan struct{}
	releaseHandles func()
}

func (s *sftpService) start(cred *sftpCredential, ch ssh.Channel) (*sftpSession, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.m.sftpRights(cred); err != nil {
		return nil, &runErr{403, "you no longer have access to this folder"}
	}
	ws, err := s.m.resolveLaunchWorkspace(ctx, cred.mc.Tenant, cred.folderRef)
	if err != nil || ws == nil {
		return nil, &runErr{404, "the folder is not available on this server"}
	}
	child, err := s.m.startSFTPChild(ws, s.program, cred.readOnly || ws.mountMode == mountRO)
	if err != nil {
		ws.closeReadOnlyHandles()
		return nil, &runErr{502, "the session could not start: " + redactErr(err)}
	}
	ss := &sftpSession{s: s, cred: cred, ch: ch, child: child, begun: time.Now(),
		done: make(chan struct{}), watchStop: make(chan struct{}), releaseHandles: ws.closeReadOnlyHandles}
	ss.last.Store(time.Now().UnixNano())

	s.mu.Lock()
	open := 0
	for o := range s.open {
		if o.cred.mc.Tenant == cred.mc.Tenant && o.cred.who == cred.who {
			open++
		}
	}
	switch {
	case s.stopped:
		err = &runErr{503, "the server is stopping"}
	case open >= sftpMaxPerPerson:
		err = &runErr{429, "you have " + strconv.Itoa(sftpMaxPerPerson) + " SFTP sessions open; close one first"}
	default:
		s.open[ss] = struct{}{}
	}
	s.mu.Unlock()
	if err == nil {
		// The ledger records the start before the first byte moves.
		if aerr := s.m.sealSFTP(ctx, cred, "start", map[string]any{"confinement": string(child.confinement.Mode)}); aerr != nil {
			s.forget(ss)
			err = &runErr{503, "the session was not started because the audit ledger could not record it"}
		}
	}
	if err != nil {
		child.cancel()
		_ = child.stdin.Close()
		_ = child.stdout.Close()
		_ = child.cmd.Wait()
		child.release()
		ws.closeReadOnlyHandles()
		return nil, err
	}
	go ss.watch()
	return ss, nil
}

func (s *sftpService) forget(ss *sftpSession) {
	s.mu.Lock()
	delete(s.open, ss)
	s.mu.Unlock()
}

// run carries the SFTP stream between the channel and the child until either ends.
func (ss *sftpSession) run() {
	go func() {
		_, _ = io.Copy(sftpCount{w: ss.child.stdin, n: &ss.in, ss: ss}, ss.ch)
		_ = ss.child.stdin.Close()
	}()
	_, _ = io.Copy(sftpCount{w: ss.ch, n: &ss.out, ss: ss}, ss.child.stdout)
	status := 0
	if err := ss.child.cmd.Wait(); err != nil {
		status = 1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			status = exit.ExitCode()
		}
	}
	_, _ = ss.ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(status)}))
	ss.finish()
}

// sftpCount counts what passes and notes the time, which the idle limit reads.
type sftpCount struct {
	w  io.Writer
	n  *atomic.Int64
	ss *sftpSession
}

func (c sftpCount) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	c.ss.last.Store(time.Now().UnixNano())
	return n, err
}

// watch ends the session when the person loses a right, when it has been idle too long and when it
// has lasted its maximum.
func (ss *sftpSession) watch() {
	t := time.NewTicker(sftpRecheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ss.watchStop:
			return
		case <-t.C:
		}
		switch {
		case time.Since(ss.begun) > sftpMaxLifetime:
			ss.close("max_lifetime")
		case time.Since(time.Unix(0, ss.last.Load())) > sftpIdleTimeout:
			ss.close("idle")
		case ss.s.m.sftpRights(ss.cred) != nil:
			ss.close("access_revoked")
		}
	}
}

// close ends the session for reason; the first reason stands.
func (ss *sftpSession) close(reason string) {
	ss.closeOnce.Do(func() {
		ss.reasonMu.Lock()
		ss.reason = reason
		ss.reasonMu.Unlock()
		ss.child.cancel()
		_ = ss.ch.Close()
	})
}

func (ss *sftpSession) finish() {
	ss.close("ended")
	close(ss.watchStop)
	ss.s.forget(ss)
	ss.reasonMu.Lock()
	reason := ss.reason
	ss.reasonMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := ss.s.m.sealSFTP(ctx, ss.cred, "end", map[string]any{
		"reason": reason, "seconds": int(time.Since(ss.begun).Seconds()),
		"bytes_in": ss.in.Load(), "bytes_out": ss.out.Load(),
	}); err != nil {
		ss.s.m.warnf("sessions: an SFTP session ended without its ledger entry", "err", redactErr(err))
	}
	cancel()
	_ = ss.child.stdout.Close()
	ss.child.release()
	ss.releaseHandles()
	_ = ss.ch.Close()
	close(ss.done)
}

// sealSFTP appends one SFTP event to the audit ledger in its own transaction. Names and contents of
// files are never recorded: the entry says who, which folder, in which mode and why it ended.
func (m *Module) sealSFTP(ctx context.Context, cred *sftpCredential, op string, extra map[string]any) error {
	if m.Data == nil {
		return errors.New("sessions: no data handle for the audit ledger")
	}
	meta := map[string]any{"workspace_ref": cred.folderRef, "op": op, "mode": cred.mode()}
	for k, v := range extra {
		meta[k] = v
	}
	return m.Data.Mutate(ctx, cred.mc.Tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor:      orSystem(cred.who.Actor),
			ActorKind:  orSystemKind(cred.who.Kind),
			Action:     "sessions.sftp." + op,
			TargetKind: workspaceKind,
			TargetID:   cred.folderID,
			Meta:       meta,
		})
		return err
	})
}
