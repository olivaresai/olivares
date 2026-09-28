// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

// license_connect_dnf_test.go drives `olivares license connect dnf-refresh` through the real command
// tree against the connect stub. The stub is not the license Worker. These tests are owed to the
// public candidate CI.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/license/connectv1"
	"github.com/olivaresai/olivares/core/license/dnfrefresh"
)

const dnfTestContext = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"

func dnfCycle(n int) string { return fmt.Sprintf("%032x", n) }

// dnfStub answers POST /connect/dnf-refresh and leaves every other route to the connect stub.
type dnfStub struct {
	*connectStub
	minted     []string
	sends      int
	editClaims func(map[string]any)
	editAnswer func(map[string]any)
	refuseWhy  string
}

func newDnfStub(t *testing.T) *dnfStub {
	t.Helper()
	d := &dnfStub{connectStub: newConnectStub(t)}
	d.srv.Close()
	d.srv = httptest.NewServer(http.HandlerFunc(d.serveDnf))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *dnfStub) serveDnf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/connect/dnf-refresh" {
		d.serve(w, r)
		return
	}
	s := d.connectStub
	s.mu.Lock()
	defer s.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	s.seen = append(s.seen, stubSeen{method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery, header: r.Header.Clone(), body: body})
	d.sends++
	if r.Header.Get(connectv1.HeaderProtocol) != connectv1.Protocol {
		s.refuseWith(w, http.StatusUnprocessableEntity, "protocol_invalid")
		return
	}
	obj, err := connectv1.ParseStrictObject(body, connectv1.BodyMax)
	if err != nil || connectv1.RequireExactFields(obj, []string{"deployment_id", "public_key"}, nil) != nil {
		s.refuseWith(w, http.StatusUnprocessableEntity, "body_invalid")
		return
	}
	dep, _ := obj["deployment_id"].(string)
	pub, _ := obj["public_key"].(string)
	depRow := s.deps[dep]
	kid, _ := kidOf(s.t, pub)
	if depRow == nil || depRow.status != "active" || depRow.kid != kid {
		s.refuseWith(w, http.StatusForbidden, "binding_denied")
		return
	}
	ch, _, ok := s.verifyProof(r, body, pub, "dnf-refresh")
	if !ok {
		s.refuseWith(w, http.StatusUnauthorized, "proof_invalid")
		return
	}
	if ch.epoch != depRow.epoch {
		s.refuseWith(w, http.StatusForbidden, "binding_denied")
		return
	}
	key := depRow.id + "|dnf-refresh|" + ch.idem
	if s.replay(w, key, ch.digest) {
		return
	}
	if rf, ok := s.refuse["dnf-refresh"]; ok {
		payload := map[string]any{"error": rf.code, "action": "server text the client must not echo"}
		if d.refuseWhy != "" {
			payload["reason"] = d.refuseWhy
		}
		w.Header().Set(connectv1.HeaderError, rf.code)
		s.write(w, rf.status, payload)
		return
	}
	s.commit(w, key, ch.digest, "dnf-refresh", d.mintDnf(depRow))
}

func (d *dnfStub) mintDnf(depRow *stubDep) map[string]any {
	s := d.connectStub
	now := time.Now().Unix()
	exp := now + 86400
	claims := map[string]any{
		"s": "olivares.ai/apt-download/v1", "aud": dnfrefresh.CredentialAudience, "cls": "current", "h": "sub_1",
		"d": depRow.id, "k": depRow.kid, "ep": depRow.epoch,
		"lin": map[string]any{"serial": fmt.Sprintf("conn_production_%s_%d", depRow.id, depRow.seq), "issue_seq": depRow.seq},
		"set": "biz", "su": []string{"entitled-security", "entitled-stable"}, "ctx": dnfTestContext, "rsn": "current",
		"iat": now, "exp": exp, "jti": s.next("jti"),
	}
	if d.editClaims != nil {
		d.editClaims(claims)
	}
	payload, _ := json.Marshal(claims)
	p := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte("stub K_apt"))
	mac.Write([]byte("oad1." + p))
	cred := "oad1." + p + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	d.minted = append(d.minted, cred)
	s.issued = append(s.issued, cred)
	answer := map[string]any{
		"deployment_id": depRow.id, "binding_epoch": depRow.epoch, "class": "current", "set": "biz",
		"suites": []string{"entitled-security", "entitled-stable"}, "context": dnfTestContext,
		"dnf_credential": cred, "exp": exp, "reason": "current",
	}
	if d.editAnswer != nil {
		d.editAnswer(answer)
	}
	return answer
}

func (d *dnfStub) counts() (sends, minted int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sends, len(d.minted)
}

func dnfHandoffForTest(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "olivares-dnf-handoff")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	saved := dnfHandoffDir
	dnfHandoffDir = dir
	t.Cleanup(func() { dnfHandoffDir = saved })
	return dir
}

type dnfCLI struct {
	*connectCLI
	dnf     *dnfStub
	handoff string
}

func newDnfCLI(t *testing.T) *dnfCLI {
	t.Helper()
	stub := newDnfStub(t)
	c := newConnectCLI(t, stub.connectStub)
	c.bind()
	return &dnfCLI{connectCLI: c, dnf: stub, handoff: dnfHandoffForTest(t)}
}

func (c *dnfCLI) dnfRefresh(cycle string) (int, map[string]any) {
	c.t.Helper()
	return c.run("dnf-refresh", "--cycle", cycle)
}

func (c *dnfCLI) readHandoff(cycle string) map[string]any {
	c.t.Helper()
	data, err := os.ReadFile(filepath.Join(c.handoff, cycle+".json"))
	if err != nil {
		c.t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		c.t.Fatal(err)
	}
	return m
}

func (c *dnfCLI) assertNoCredentialInOutput() {
	c.t.Helper()
	c.dnf.mu.Lock()
	minted := append([]string(nil), c.dnf.minted...)
	c.dnf.mu.Unlock()
	for _, out := range c.outputs {
		if strings.Contains(out, dnfrefresh.CredentialPrefix) {
			c.t.Errorf("command output carries a download credential prefix: %.80q", out)
		}
		for _, m := range minted {
			if strings.Contains(out, m) {
				c.t.Error("a download credential appeared in command output or in an error")
			}
		}
	}
}

func TestConnectDnfRefreshCommandSurface(t *testing.T) {
	code, stdout, _, err := runCLIExit(t, "license", "connect", "dnf-refresh", "--help")
	if err != nil || code != exitcode.OK {
		t.Fatalf("dnf-refresh --help = %d %v", code, err)
	}
	for _, want := range []string{"--data-dir", "--cycle", "prints no credential", "writes only the handoff of its cycle", dnfrefresh.HandoffDir} {
		if !strings.Contains(stdout, want) {
			t.Errorf("dnf-refresh --help does not say %q:\n%s", want, stdout)
		}
	}
	for _, absent := range []string{"--handoff-dir", "--endpoint", "--timeout"} {
		if strings.Contains(stdout, absent) {
			t.Errorf("dnf-refresh offers %s", absent)
		}
	}
}

func TestConnectDnfRefreshRefusesAnInvalidCycleBeforeAnyEffect(t *testing.T) {
	c := newDnfCLI(t)
	seenBefore, _ := c.stub.snapshot()
	code, _ := c.run("dnf-refresh", "--cycle", "not-a-cycle")
	if code != exitcode.Usage || !strings.HasPrefix(c.lastError, dnfrefresh.CodeCycleInvalid) {
		t.Fatalf("exit %d %q, want %d and %s", code, c.lastError, exitcode.Usage, dnfrefresh.CodeCycleInvalid)
	}
	if seen, _ := c.stub.snapshot(); len(seen) != len(seenBefore) {
		t.Fatal("an invalid cycle reached the service")
	}
	fresh := filepath.Join(t.TempDir(), "fresh")
	code, _, _, err := runCLIExit(t, "license", "connect", "dnf-refresh", "--cycle", "not-a-cycle", "--data-dir", fresh)
	if code != exitcode.Usage || err == nil || !strings.HasPrefix(err.Error(), dnfrefresh.CodeCycleInvalid) {
		t.Fatalf("fresh data directory: exit %d %v", code, err)
	}
	if _, err := os.Lstat(fresh); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("an invalid cycle created the data directory: %v", err)
	}
}

func TestConnectDnfRefreshPublishesTheCredentialOnlyInTheHandoff(t *testing.T) {
	c := newDnfCLI(t)
	lic, tok := c.license(), c.token()
	cycle := dnfCycle(1)
	code, rep := c.dnfRefresh(cycle)
	if code != exitcode.OK {
		t.Fatalf("dnf-refresh = %d (%s)", code, c.lastError)
	}
	dnf, _ := rep["dnf"].(map[string]any)
	if dnf["cycle"] != cycle || dnf["class"] != "current" {
		t.Fatalf("report dnf = %v", rep["dnf"])
	}
	if _, ok := dnf["credential"]; ok {
		t.Fatal("the report names the credential")
	}
	m := c.readHandoff(cycle)
	if m["schema"] != dnfrefresh.Schema || m["outcome"] != "issued" || m["credential"] == nil {
		t.Fatalf("handoff = %v", m)
	}
	if !bytes.Equal(c.license(), lic) || !bytes.Equal(c.token(), tok) {
		t.Fatal("dnf-refresh changed the licence file or ota.token")
	}
	c.assertNoCredentialInOutput()
	if p := c.state().Pending; p != nil {
		t.Fatalf("an issued answer left an operation pending: %+v", p)
	}
}

// TestConnectDnfRefreshAudienceIsNotTheDnfRootStaysPending: an answer whose audience is the APT root
// stays pending. The operation is kept and no credential is published.
func TestConnectDnfRefreshAudienceIsNotTheDnfRootStaysPending(t *testing.T) {
	c := newDnfCLI(t)
	c.dnf.mu.Lock()
	c.dnf.editClaims = func(cl map[string]any) { cl["aud"] = "https://licenses.olivares.ai/apt/v1/" }
	c.dnf.mu.Unlock()
	cycle := dnfCycle(1)
	code, _ := c.dnfRefresh(cycle)
	if code != exitcode.Indeterminate {
		t.Fatalf("exit %d, want %d (%s)", code, exitcode.Indeterminate, c.lastError)
	}
	m := c.readHandoff(cycle)
	if m["outcome"] != "unknown" || m["credential"] != nil {
		t.Fatalf("handoff = %v, want unknown and no credential", m)
	}
	p := c.state().Pending
	if p == nil || p.Intent != "dnf-refresh" || p.Path != "/connect/dnf-refresh" {
		t.Fatalf("a wrong audience must keep the dnf-refresh operation: %+v", p)
	}
	c.assertNoCredentialInOutput()
}

// TestConnectDnfRefreshReasonRevokedIsRefusedAndEndsTheOperation: reason revoked is refused and the
// pending operation is cleared. The credential is not published.
func TestConnectDnfRefreshReasonRevokedIsRefusedAndEndsTheOperation(t *testing.T) {
	c := newDnfCLI(t)
	c.dnf.mu.Lock()
	c.dnf.refuse["dnf-refresh"] = stubRefusal{status: http.StatusForbidden, code: "authority_denied"}
	c.dnf.refuseWhy = "revoked"
	c.dnf.mu.Unlock()
	cycle := dnfCycle(2)
	code, _ := c.dnfRefresh(cycle)
	if code == exitcode.OK {
		t.Fatal("revoked exited 0")
	}
	m := c.readHandoff(cycle)
	if m["outcome"] != "refused" || m["code"] != "authority_denied" || m["credential"] != nil {
		t.Fatalf("handoff = %v, want refused/authority_denied", m)
	}
	if p := c.state().Pending; p != nil {
		t.Fatalf("revoked left an operation pending: %+v", p)
	}
	c.assertNoCredentialInOutput()
}

func (c *dnfCLI) keepDnf(cycle string) *connectPendingOp {
	c.t.Helper()
	c.dnf.mu.Lock()
	c.dnf.lose["dnf-refresh"] = 1
	c.dnf.mu.Unlock()
	code, _ := c.dnfRefresh(cycle)
	if code != exitcode.Indeterminate {
		c.t.Fatalf("a lost answer: exit %d (%s)", code, c.lastError)
	}
	p := c.state().Pending
	if p == nil || p.Intent != connectv1.OpDnfRefresh {
		c.t.Fatalf("no dnf-refresh operation is pending: %+v", p)
	}
	return p
}

func (c *dnfCLI) editState(edit func(*connectState)) {
	c.t.Helper()
	st := c.state()
	edit(st)
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.dir, connectDirName, connectStateFileName), append(data, '\n'), 0o600); err != nil {
		c.t.Fatal(err)
	}
}

// TestConnectPendingDnfRefreshDoesNotBlockAnotherOperation: refresh and deactivate replace a
// pending dnf-refresh, and dnf-refresh replaces a pending apt-refresh. A download refresh blocks none
// of those operations.
func TestConnectPendingDnfRefreshDoesNotBlockAnotherOperation(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, c *dnfCLI)
	}{
		{"refresh", func(t *testing.T, c *dnfCLI) {
			code, rep := c.run("refresh")
			if code != exitcode.OK || rep["status"] != "refreshed" {
				t.Fatalf("refresh = %d %v (%s)", code, rep, c.lastError)
			}
			sup, _ := rep["superseded"].(map[string]any)
			if sup["intent"] != connectv1.OpDnfRefresh {
				t.Fatalf("superseded = %v", rep["superseded"])
			}
		}},
		{"deactivate", func(t *testing.T, c *dnfCLI) {
			code, _ := c.run("deactivate", "--yes")
			if code != exitcode.Usage || !strings.Contains(c.lastError, "superseded") {
				t.Fatalf("deactivate = %d (%s)", code, c.lastError)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newDnfCLI(t)
			var notices bytes.Buffer
			saved := connectNoticeOut
			connectNoticeOut = &notices
			t.Cleanup(func() { connectNoticeOut = saved })
			c.keepDnf(dnfCycle(1))
			tc.run(t, c)
			if p := c.state().Pending; p != nil && p.Intent == connectv1.OpDnfRefresh {
				t.Fatalf("dnf-refresh still pending after %s: %+v", tc.name, p)
			}
			if !strings.Contains(notices.String(), "superseded the pending dnf-refresh operation") {
				t.Fatalf("notice %q", notices.String())
			}
		})
	}

	t.Run("dnf-refresh replaces apt-refresh", func(t *testing.T) {
		c := newDnfCLI(t)
		c.editState(func(st *connectState) {
			st.Pending = &connectPendingOp{Intent: connectv1.OpAptRefresh, Phase: connectv1.OpAptRefresh,
				Operation: connectv1.OpAptRefresh, Method: "POST", Path: connectv1.PathAptRefresh,
				Target: st.Binding.DeploymentID, BindingEpoch: st.Binding.BindingEpoch, SignerRole: "current",
				Body: "e30=", BodySHA256: connectv1.BodyDigest([]byte("{}")), IdempotencyKey: "apt-refresh-idempotency",
				CreatedAt: "2026-09-27T00:00:00Z"}
		})
		var notices bytes.Buffer
		saved := connectNoticeOut
		connectNoticeOut = &notices
		t.Cleanup(func() { connectNoticeOut = saved })
		code, rep := c.dnfRefresh(dnfCycle(4))
		if code != exitcode.OK {
			t.Fatalf("dnf-refresh over a pending apt-refresh = %d (%s)", code, c.lastError)
		}
		sup, _ := rep["superseded"].(map[string]any)
		if sup["intent"] != connectv1.OpAptRefresh {
			t.Fatalf("superseded = %v", rep["superseded"])
		}
		if !strings.Contains(notices.String(), "superseded the pending apt-refresh operation") {
			t.Fatalf("notice %q", notices.String())
		}
	})
}

func TestConnectDnfRefreshCredentialIsNotAnArgument(t *testing.T) {
	c := newDnfCLI(t)
	cycle := dnfCycle(3)
	if code, _ := c.dnfRefresh(cycle); code != exitcode.OK {
		t.Fatalf("dnf-refresh = %d (%s)", code, c.lastError)
	}
	c.dnf.mu.Lock()
	defer c.dnf.mu.Unlock()
	if len(c.dnf.minted) != 1 {
		t.Fatalf("minted %d credentials", len(c.dnf.minted))
	}
	cred := c.dnf.minted[0]
	for _, seen := range c.dnf.seen {
		if bytes.Contains(seen.body, []byte(cred)) && seen.path != "/connect/dnf-refresh" {
			t.Fatalf("the credential left on %s", seen.path)
		}
		if seen.header.Get("Authorization") != "" && strings.Contains(seen.header.Get("Authorization"), cred) {
			t.Fatal("the credential was an Authorization argument")
		}
	}
	// The request body is the proof's body, which is {deployment_id, public_key}, never the credential.
	for _, seen := range c.dnf.seen {
		if seen.path == "/connect/dnf-refresh" && bytes.Contains(seen.body, []byte(cred)) {
			t.Fatal("the credential was in the dnf-refresh request body")
		}
	}
}
