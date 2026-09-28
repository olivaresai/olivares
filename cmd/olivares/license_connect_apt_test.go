// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

// license_connect_apt_test.go drives `olivares license connect apt-refresh` through the real command tree
// against the protocol-unit stub of license_connect_test.go, extended with the apt-refresh route. The same
// caveat holds: the stub is not the license Worker and not D1. It follows the Interface's wire (Q3 r2
// §3.1.1.1) so the client's cycle, pending-operation, answer-check and handoff rules can be observed.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/license/aptrefresh"
	"github.com/olivaresai/olivares/core/license/connectv1"
)

const (
	aptTestInvocation = "4f1c0a2b9e8d7c6b5a4f3e2d1c0b9a88"
	aptTestContext    = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
	aptHandoffFields  = "at,class,code,context,credential,cycle,exp,invocation,outcome,schema,set,suites"
)

// aptCycle is the n-th test cycle: 32 lowercase hex characters, as the helper mints them.
func aptCycle(n int) string { return fmt.Sprintf("%032x", n) }

// aptStub is connectStub with POST /connect/apt-refresh: exactly {deployment_id, public_key}, a proof for
// operation apt-refresh on that route by the bound key at the bound epoch, the stored result for a repeated
// operation, and one download credential per committed operation.
type aptStub struct {
	*connectStub
	minted     []string                           // every download credential committed
	sends      int                                // apt-refresh requests received
	editClaims func(map[string]any)               // changes the claims of the next credential
	editAnswer func(map[string]any)               // changes the next answer
	refuseWhy  string                             // the reason a refusal of s.refuse["apt-refresh"] carries, "" for none
	onSend     func(r *http.Request, body []byte) // observes an apt-refresh request before it is answered
}

func newAptStub(t *testing.T) *aptStub {
	t.Helper()
	a := &aptStub{connectStub: newConnectStub(t)}
	a.srv.Close()
	a.srv = httptest.NewServer(http.HandlerFunc(a.serveApt))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *aptStub) serveApt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/connect/apt-refresh" {
		a.serve(w, r)
		return
	}
	s := a.connectStub
	s.mu.Lock()
	defer s.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	s.seen = append(s.seen, stubSeen{method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery, header: r.Header.Clone(), body: body})
	a.sends++
	if a.onSend != nil {
		a.onSend(r, body)
	}
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
	d := s.deps[dep]
	kid, _ := kidOf(s.t, pub)
	if d == nil || d.status != "active" || d.kid != kid {
		s.refuseWith(w, http.StatusForbidden, "binding_denied")
		return
	}
	ch, _, ok := s.verifyProof(r, body, pub, "apt-refresh")
	if !ok {
		s.refuseWith(w, http.StatusUnauthorized, "proof_invalid")
		return
	}
	if ch.epoch != d.epoch {
		s.refuseWith(w, http.StatusForbidden, "binding_denied")
		return
	}
	key := d.id + "|apt-refresh|" + ch.idem
	if s.replay(w, key, ch.digest) {
		return
	}
	if rf, ok := s.refuse["apt-refresh"]; ok {
		// Q3-S1's refusal body: the connect-v1 error body plus the resolver's reason.
		body := map[string]any{"error": rf.code, "action": "server text the client must not echo"}
		if a.refuseWhy != "" {
			body["reason"] = a.refuseWhy
		}
		w.Header().Set(connectv1.HeaderError, rf.code)
		s.write(w, rf.status, body)
		return
	}
	s.commit(w, key, ch.digest, "apt-refresh", a.mintApt(d))
}

// mintApt is the service's answer for d: a download credential of class current for set biz. Its audience is
// the literal adapter root Q3-S1 mints for every origin (r2 §3.1.1.4), not this stub's loopback origin.
func (a *aptStub) mintApt(d *stubDep) map[string]any {
	s := a.connectStub
	now := time.Now().Unix()
	exp := now + 86400
	claims := map[string]any{
		"s": "olivares.ai/apt-download/v1", "aud": "https://licenses.olivares.ai/apt/v1/", "cls": "current", "h": "sub_1", "d": d.id, "k": d.kid,
		"ep": d.epoch, "lin": map[string]any{"serial": fmt.Sprintf("conn_production_%s_%d", d.id, d.seq), "issue_seq": d.seq},
		"set": "biz", "su": []string{"entitled-security", "entitled-stable"}, "ctx": aptTestContext, "rsn": "current",
		"iat": now, "exp": exp, "jti": s.next("jti"),
	}
	if a.editClaims != nil {
		a.editClaims(claims)
	}
	payload, _ := json.Marshal(claims)
	p := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte("stub K_apt"))
	mac.Write([]byte("oad1." + p))
	cred := "oad1." + p + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	a.minted = append(a.minted, cred)
	s.issued = append(s.issued, cred)
	answer := map[string]any{
		"deployment_id": d.id, "binding_epoch": d.epoch, "class": "current", "set": "biz",
		"suites": []string{"entitled-security", "entitled-stable"}, "context": aptTestContext, "apt_credential": cred,
		"exp": exp, "reason": "current",
	}
	if a.editAnswer != nil {
		a.editAnswer(answer)
	}
	return answer
}

func (a *aptStub) counts() (sends, minted int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sends, len(a.minted)
}

func (a *aptStub) mintedCredential(i int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i >= len(a.minted) {
		return ""
	}
	return a.minted[i]
}

// aptHandoffForTest stands in for the fixed handoff directory: a 0700 directory of this uid, set
// through the unexported variable only tests replace.
func aptHandoffForTest(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "olivares-apt-handoff")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	saved := aptHandoffDir
	aptHandoffDir = dir
	t.Cleanup(func() { aptHandoffDir = saved })
	return dir
}

// readAptHandoff reads the handoff of cycle as the helper does: a regular 0600 file, at most 4096 bytes,
// strict JSON with exactly the Interface's fields (r2 §3.10 I5, r3 §3.1.5.5).
func readAptHandoff(t *testing.T, dir, cycle string) map[string]any {
	t.Helper()
	path := filepath.Join(dir, cycle+".json")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("no handoff for cycle %s: %v", cycle, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > aptrefresh.MaxHandoffBytes {
		t.Fatalf("handoff %s is %s %04o with %d bytes", path, info.Mode().Type(), info.Mode().Perm(), info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("handoff %s is not a JSON object: %v", path, err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if got := strings.Join(keys, ","); got != aptHandoffFields {
		t.Fatalf("handoff fields %s, want %s", got, aptHandoffFields)
	}
	if m["schema"] != "olivares.ai/apt-credential-handoff/v2" || m["cycle"] != cycle {
		t.Fatalf("handoff schema %v, cycle %v", m["schema"], m["cycle"])
	}
	return m
}

// assertAptOutcome checks that the handoff of cycle names outcome and code and, unless the outcome is
// issued, nulls class, set, suites, context, exp and credential.
func assertAptOutcome(t *testing.T, dir, cycle, outcome string, code any) map[string]any {
	t.Helper()
	m := readAptHandoff(t, dir, cycle)
	if m["outcome"] != outcome || m["code"] != code {
		t.Fatalf("handoff outcome %v, code %v; want %s, %v", m["outcome"], m["code"], outcome, code)
	}
	if outcome != "issued" {
		for _, k := range []string{"class", "set", "suites", "context", "exp", "credential"} {
			if m[k] != nil {
				t.Fatalf("a %s handoff carries %s", outcome, k)
			}
		}
	}
	return m
}

// aptCLI is a bound data directory, its apt stub and a handoff directory.
type aptCLI struct {
	*connectCLI
	apt     *aptStub
	handoff string
}

func newAptCLI(t *testing.T) *aptCLI {
	t.Helper()
	stub := newAptStub(t)
	c := newConnectCLI(t, stub.connectStub)
	c.bind()
	handoff := aptHandoffForTest(t)
	t.Setenv("INVOCATION_ID", aptTestInvocation)
	return &aptCLI{connectCLI: c, apt: stub, handoff: handoff}
}

// aptRefresh runs apt-refresh for cycle and returns its exit code.
func (c *aptCLI) aptRefresh(cycle string) int {
	c.t.Helper()
	code, _ := c.run("apt-refresh", "--cycle", cycle)
	return code
}

func (c *aptCLI) assertOutcome(cycle, outcome string, code any) map[string]any {
	c.t.Helper()
	return assertAptOutcome(c.t, c.handoff, cycle, outcome, code)
}

func (c *aptCLI) handoffFiles() []string {
	c.t.Helper()
	entries, err := os.ReadDir(c.handoff)
	if err != nil {
		c.t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// editState rewrites state.json through edit, as a stand-in for a state an earlier command left.
func (c *aptCLI) editState(edit func(*connectState)) {
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

// replaceIdentity puts a valid identity of another key in identity.key.
func (c *aptCLI) replaceIdentity() {
	c.t.Helper()
	pub, priv := keyFromSeedForTest(0x42)
	kid, err := connectv1.KID(pub)
	if err != nil {
		c.t.Fatal(err)
	}
	data, err := json.MarshalIndent(connectIdentityWire{Schema: connectIdentitySchema, KID: kid,
		PublicKey: connectv1.EncodePublicKey(pub), Seed: base64.RawURLEncoding.EncodeToString(priv.Seed())}, "", "  ")
	if err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.dir, connectDirName, connectIdentityFileName), append(data, '\n'), 0o600); err != nil {
		c.t.Fatal(err)
	}
}

// holdLease holds the data directory's lease for the rest of the test, as a concurrent command would.
func (c *aptCLI) holdLease() {
	c.t.Helper()
	lease, err := acquireConnectLease(filepath.Join(c.dir, connectDirName))
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() { _ = lease.Close() })
}

// assertCredentialOnlyInHandoff: no download credential appears in any command output or in any file of
// the data directory; the handoff is the only place that holds one (r2 §3.1.4.2, r3 §3.1.5.5).
func (c *aptCLI) assertCredentialOnlyInHandoff() {
	c.t.Helper()
	c.apt.mu.Lock()
	minted := append([]string(nil), c.apt.minted...)
	c.apt.mu.Unlock()
	for _, out := range c.outputs {
		if strings.Contains(out, aptrefresh.CredentialPrefix) {
			c.t.Errorf("a command output carries a download credential prefix: %.80q", out)
		}
		for _, m := range minted {
			if strings.Contains(out, m) {
				c.t.Error("a download credential appeared in command output")
			}
		}
	}
	err := filepath.WalkDir(c.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range minted {
			if bytes.Contains(b, []byte(m)) {
				c.t.Errorf("%s holds a download credential", path)
			}
		}
		return nil
	})
	if err != nil {
		c.t.Fatal(err)
	}
}

// ---- tests ------------------------------------------------------------------------------

// TestConnectAptRefreshCommandSurface: `license connect apt-refresh` takes --data-dir and --cycle
// (Interface Q3 r3 §3.10 I4). The handoff directory is compiled in, so r2's --handoff-dir is an unknown
// flag; the help names the fixed directory and says the command prints no credential and writes only the
// handoff of its cycle (r2 §3.11).
func TestConnectAptRefreshCommandSurface(t *testing.T) {
	code, stdout, _, err := runCLIExit(t, "license", "connect", "apt-refresh", "--help")
	if err != nil || code != exitcode.OK {
		t.Fatalf("apt-refresh --help = %d %v", code, err)
	}
	for _, want := range []string{"--data-dir", "--cycle", "prints no credential", "writes only the handoff of its cycle", aptrefresh.HandoffDir} {
		if !strings.Contains(stdout, want) {
			t.Errorf("apt-refresh --help does not say %q:\n%s", want, stdout)
		}
	}
	for _, absent := range []string{"--handoff-dir", "--endpoint", "--timeout"} {
		if strings.Contains(stdout, absent) {
			t.Errorf("apt-refresh offers %s:\n%s", absent, stdout)
		}
	}
	dir := filepath.Join(t.TempDir(), "data")
	code, _, _, err = runCLIExit(t, "license", "connect", "apt-refresh", "--data-dir", dir, "--cycle", aptCycle(1), "--handoff-dir", t.TempDir())
	if code != exitcode.Usage || err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("--handoff-dir = %d %v, want a usage error for an unknown flag", code, err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused invocation created the data directory: %v", err)
	}
}

// TestConnectAptRefreshRefusesAnInvalidCycleBeforeAnyEffect: each invalid --cycle exits 2 with
// cycle_invalid before any network call, state read, operation change or file write (r3 §3.1.5.1). The
// state is made unreadable to the client first, so a run that read it before checking the cycle would
// answer that instead; the data directory, the stub's requests and the handoff directory must not move.
func TestConnectAptRefreshRefusesAnInvalidCycleBeforeAnyEffect(t *testing.T) {
	c := newAptCLI(t)
	statePath := filepath.Join(c.dir, connectDirName, connectStateFileName)
	if err := os.Chmod(statePath, 0o640); err != nil {
		t.Fatal(err)
	}
	before := aptSnapshotDir(t, c.dir)
	seenBefore, _ := c.stub.snapshot()
	valid := aptCycle(1)
	for _, tc := range []struct{ name, cycle string }{
		{"31 characters", valid[:31]},
		{"33 characters", valid + "0"},
		{"upper case", "0123456789ABCDEF0123456789abcdef"},
		{"a slash", "0123456789abcdef/123456789abcdef"},
		{"empty", ""},
		{"a dot-dot segment", "../3456789abcdef0123456789abcdef"},
	} {
		code, _ := c.run("apt-refresh", "--cycle", tc.cycle)
		if code != exitcode.Usage || !strings.HasPrefix(c.lastError, aptrefresh.CodeCycleInvalid) {
			t.Errorf("%s: exit %d %q, want %d and %s", tc.name, code, c.lastError, exitcode.Usage, aptrefresh.CodeCycleInvalid)
		}
		if seen, _ := c.stub.snapshot(); len(seen) != len(seenBefore) {
			t.Errorf("%s: an invalid cycle reached the service (%d requests)", tc.name, len(seen)-len(seenBefore))
		}
		if after := aptSnapshotDir(t, c.dir); !maps.Equal(after, before) {
			t.Errorf("%s: an invalid cycle changed the data directory", tc.name)
		}
		if names := c.handoffFiles(); len(names) != 0 {
			t.Errorf("%s: an invalid cycle wrote %v", tc.name, names)
		}
	}

	// A data directory that does not exist yet is not created: the cycle is refused before the store opens.
	fresh := filepath.Join(t.TempDir(), "fresh")
	code, _, _, err := runCLIExit(t, "license", "connect", "apt-refresh", "--cycle", "not-a-cycle", "--data-dir", fresh)
	if code != exitcode.Usage || err == nil || !strings.HasPrefix(err.Error(), aptrefresh.CodeCycleInvalid) {
		t.Errorf("fresh data directory: exit %d %v, want %d and %s", code, err, exitcode.Usage, aptrefresh.CodeCycleInvalid)
	}
	if _, err := os.Lstat(fresh); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an invalid cycle created the data directory: %v", err)
	}

	// Control: with the state readable again, a valid cycle reaches the service and is issued.
	if err := os.Chmod(statePath, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := c.aptRefresh(valid); code != exitcode.OK {
		t.Fatalf("valid cycle: exit %d (%s)", code, c.lastError)
	}
	if sends, _ := c.apt.counts(); sends != 1 {
		t.Fatalf("valid cycle: %d apt-refresh requests, want 1", sends)
	}
	c.assertOutcome(valid, "issued", nil)
}

// TestConnectAptRefreshPublishesTheCredentialOnlyInTheHandoff: an issued answer is checked, published as
// <cycle>.json and then the pending operation is cleared; exit 0. The licence file, ota.token and the last
// licence credential are untouched, and no download credential reaches stdout, stderr or a file of the data
// directory (r2 §3.1.4.2, §3.1.1.5; r3 §3.1.5.5).
func TestConnectAptRefreshPublishesTheCredentialOnlyInTheHandoff(t *testing.T) {
	c := newAptCLI(t)
	lic, tok, last := c.license(), c.token(), *c.state().Last
	cycle := aptCycle(1)
	code, rep := c.run("apt-refresh", "--cycle", cycle)
	if code != exitcode.OK || rep["status"] != "issued" {
		t.Fatalf("apt-refresh = %d %v (%s)", code, rep, c.lastError)
	}
	if sends, minted := c.apt.counts(); sends != 1 || minted != 1 {
		t.Fatalf("%d requests and %d credentials, want one of each", sends, minted)
	}
	m := c.assertOutcome(cycle, "issued", nil)
	if m["credential"] != c.apt.mintedCredential(0) || m["invocation"] != aptTestInvocation || m["class"] != "current" ||
		m["set"] != "biz" || m["context"] != aptTestContext {
		t.Fatalf("issued handoff %v", m)
	}
	if su, _ := m["suites"].([]any); len(su) != 2 || su[0] != "entitled-security" || su[1] != "entitled-stable" {
		t.Fatalf("suites %v", m["suites"])
	}
	if n, ok := m["exp"].(json.Number); !ok || n.String() == "" {
		t.Fatalf("exp %v", m["exp"])
	}
	if names := c.handoffFiles(); strings.Join(names, ",") != cycle+".json" {
		t.Fatalf("the handoff directory holds %v, want only %s.json", names, cycle)
	}
	st := c.state()
	if st.Pending != nil || st.Last == nil || *st.Last != last {
		t.Fatalf("after issue: pending %+v, last %+v (was %+v)", st.Pending, st.Last, last)
	}
	if !bytes.Equal(c.license(), lic) || !bytes.Equal(c.token(), tok) {
		t.Fatal("apt-refresh changed the licence file or ota.token")
	}
	c.assertCredentialOnlyInHandoff()
	c.assertNoSecretsLeaked()
}

// TestConnectAptRefreshCopiesOnlyAValidInvocationID: $INVOCATION_ID of exactly 32 lowercase hex characters
// is the handoff's invocation; absent or any other form is null (r3 §3.1.5.4).
func TestConnectAptRefreshCopiesOnlyAValidInvocationID(t *testing.T) {
	c := newAptCLI(t)
	for i, tc := range []struct {
		name  string
		set   bool
		value string
		want  any
	}{
		{"valid", true, aptTestInvocation, aptTestInvocation},
		{"absent", false, "", nil},
		{"upper case", true, strings.ToUpper(aptTestInvocation), nil},
		{"31 characters", true, aptTestInvocation[:31], nil},
		{"a UUID with dashes", true, "4f1c0a2b-9e8d-7c6b-5a4f-3e2d1c0b9a88", nil},
	} {
		if tc.set {
			t.Setenv("INVOCATION_ID", tc.value)
		} else if err := os.Unsetenv("INVOCATION_ID"); err != nil {
			t.Fatal(err)
		}
		cycle := aptCycle(i + 1)
		if code := c.aptRefresh(cycle); code != exitcode.OK {
			t.Fatalf("%s: exit %d (%s)", tc.name, code, c.lastError)
		}
		if got := c.assertOutcome(cycle, "issued", nil)["invocation"]; got != tc.want {
			t.Errorf("%s: invocation %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestConnectAptRefreshPersistsThePendingOperationAndRepeatsItAfterALostAnswer: the operation is on disk
// before it is sent; a lost answer is unknown and keeps it; the next cycle repeats the same operation and
// receives the stored result — the same credential, no second mint (r2 §3.1.4.2, row R9).
func TestConnectAptRefreshPersistsThePendingOperationAndRepeatsItAfterALostAnswer(t *testing.T) {
	c := newAptCLI(t)
	statePath := filepath.Join(c.dir, connectDirName, connectStateFileName)
	var onDisk []*connectPendingOp
	var keys, digests []string
	c.apt.mu.Lock()
	c.apt.onSend = func(r *http.Request, body []byte) {
		var st connectState
		data, err := os.ReadFile(statePath)
		if err == nil {
			err = json.Unmarshal(data, &st)
		}
		if err != nil {
			t.Errorf("read the state when the request arrived: %v", err)
		}
		onDisk = append(onDisk, st.Pending)
		keys = append(keys, r.Header.Get(connectv1.HeaderIdempotencyKey))
		digests = append(digests, connectv1.BodyDigest(body))
	}
	c.apt.lose["apt-refresh"] = 1
	c.apt.mu.Unlock()

	first := aptCycle(1)
	if code := c.aptRefresh(first); code != exitcode.Indeterminate {
		t.Fatalf("a lost answer: exit %d, want %d (%s)", code, exitcode.Indeterminate, c.lastError)
	}
	c.assertOutcome(first, "unknown", nil)
	kept := c.state().Pending
	if kept == nil || kept.Intent != "apt-refresh" || kept.Operation != "apt-refresh" || kept.Method != "POST" ||
		kept.Path != "/connect/apt-refresh" || kept.SignerRole != "current" {
		t.Fatalf("a lost answer must keep the apt-refresh operation: %+v", kept)
	}

	second := aptCycle(2)
	if code := c.aptRefresh(second); code != exitcode.OK {
		t.Fatalf("the repeat: exit %d (%s)", code, c.lastError)
	}
	m := c.assertOutcome(second, "issued", nil)
	if _, minted := c.apt.counts(); minted != 1 || m["credential"] != c.apt.mintedCredential(0) {
		t.Fatalf("the repeat minted %d credentials or published another one", minted)
	}
	if c.state().Pending != nil {
		t.Fatal("the issued repeat did not clear the pending operation")
	}
	c.assertOutcome(first, "unknown", nil) // a later cycle never rewrites an earlier cycle's handoff

	c.apt.mu.Lock()
	defer c.apt.mu.Unlock()
	if len(onDisk) != 2 {
		t.Fatalf("%d apt-refresh requests, want the original and one repeat", len(onDisk))
	}
	for i, p := range onDisk {
		if p == nil || p.Intent != "apt-refresh" || p.IdempotencyKey != keys[i] || p.IdempotencyKey != kept.IdempotencyKey ||
			p.BodySHA256 != digests[i] || p.Attempts != i+1 {
			t.Fatalf("request %d: the persisted operation %+v is not the one sent (key %s, body %s)", i, p, keys[i], digests[i])
		}
	}
}

// TestConnectAptRefreshAnswerThatFailsItsChecksIsUnknown: an answer that fails a check of r2 §3.1.4.2 is
// unknown: it keeps the pending operation, publishes no credential and changes no licence or token.
// Repeating it receives the same stored answer, never a second mint.
func TestConnectAptRefreshAnswerThatFailsItsChecksIsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name         string
		claims, ansr func(map[string]any)
	}{
		{"an extra answer field", nil, func(a map[string]any) { a["ota"] = "bearer" }},
		{"another deployment_id", nil, func(a map[string]any) { a["deployment_id"] = "dep_other" }},
		{"another binding_epoch", nil, func(a map[string]any) { a["binding_epoch"] = int64(9) }},
		{"a credential outside the grammar", nil, func(a map[string]any) { a["apt_credential"] = "oad1.short.mac" }},
		{"another audience", func(c map[string]any) { c["aud"] = "https://evil.example/apt/v1/" }, nil},
		{"another deployment in the credential", func(c map[string]any) { c["d"] = "dep_other" }, nil},
		{"another pop kid", func(c map[string]any) { c["k"] = "kid_other" }, nil},
		{"another epoch in the credential", func(c map[string]any) { c["ep"] = int64(9) }, nil},
		{"ctx unlike context", func(c map[string]any) { c["ctx"] = "ffffffffffffffffffffffffffffffff" }, nil},
		{"exp − now above 86700 s", func(c map[string]any) { c["exp"] = time.Now().Unix() + 90000 },
			func(a map[string]any) { a["exp"] = time.Now().Unix() + 90000 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newAptCLI(t)
			lic, tok := c.license(), c.token()
			c.apt.mu.Lock()
			c.apt.editClaims, c.apt.editAnswer = tc.claims, tc.ansr
			c.apt.mu.Unlock()
			first := aptCycle(1)
			if code := c.aptRefresh(first); code != exitcode.Indeterminate {
				t.Fatalf("exit %d, want %d (%s)", code, exitcode.Indeterminate, c.lastError)
			}
			c.assertOutcome(first, "unknown", nil)
			p := c.state().Pending
			if p == nil || p.Intent != "apt-refresh" {
				t.Fatalf("a failed check must keep the operation: %+v", p)
			}
			second := aptCycle(2)
			if code := c.aptRefresh(second); code != exitcode.Indeterminate {
				t.Fatalf("the repeat: exit %d, want %d", code, exitcode.Indeterminate)
			}
			c.assertOutcome(second, "unknown", nil)
			if q := c.state().Pending; q == nil || q.IdempotencyKey != p.IdempotencyKey {
				t.Fatalf("the repeat is not the same operation: %+v", q)
			}
			if sends, minted := c.apt.counts(); sends != 2 || minted != 1 {
				t.Fatalf("%d requests and %d credentials, want 2 and 1", sends, minted)
			}
			if !bytes.Equal(c.license(), lic) || !bytes.Equal(c.token(), tok) {
				t.Fatal("the licence file or ota.token changed")
			}
			c.assertCredentialOnlyInHandoff()
		})
	}
}

// TestConnectAptRefreshOutcomeRows: rows R1 and R6-R12 of Interface Q3 r2 §3.1.4.1, R7 as r3 amends it.
// Each publishes its outcome and code for this cycle and keeps or clears the pending operation as its row
// says; only issued exits 0. No row touches the licence file or ota.token. authority_denied is R7 only
// with reason revoked, refunded or absent (r3 §3.1.5.4); otherwise it is unknown and kept, and the helper
// removes nothing. A pending feature or owner operation still answers pending_other_operation.
func TestConnectAptRefreshOutcomeRows(t *testing.T) {
	refuse := func(status int, code, reason string) func(*aptCLI) {
		return func(c *aptCLI) {
			c.apt.mu.Lock()
			c.apt.refuse["apt-refresh"] = stubRefusal{status, code}
			c.apt.refuseWhy = reason
			c.apt.mu.Unlock()
		}
	}
	pendingOp := func(intent, phase, operation, path string) func(*aptCLI) {
		return func(c *aptCLI) {
			c.editState(func(st *connectState) {
				st.Pending = &connectPendingOp{Intent: intent, Phase: phase, Operation: operation, Method: "POST", Path: path,
					Target: st.Binding.DeploymentID, BindingEpoch: st.Binding.BindingEpoch, SignerRole: "current", Body: "e30=",
					BodySHA256: connectv1.BodyDigest([]byte("{}")), IdempotencyKey: intent + "-idempotency", CreatedAt: "2026-09-27T00:00:00Z"}
			})
		}
	}
	rows := []struct {
		name    string
		arrange func(*aptCLI)
		outcome string
		code    any
		sent    bool // whether an apt-refresh request reached the service
		pending bool // whether an apt-refresh operation is pending afterwards
	}{
		{"issued, the control", func(*aptCLI) {}, "issued", nil, true, false},
		{"issued with the P6 multi-token reason", func(c *aptCLI) {
			const p6 = "refunded_renewal refunded_expansion_excluded held_set_single_source"
			c.apt.mu.Lock()
			c.apt.editClaims = func(cl map[string]any) {
				cl["cls"], cl["su"], cl["rsn"] = "security-only", []string{"entitled-security"}, p6
			}
			c.apt.editAnswer = func(a map[string]any) {
				a["class"], a["suites"], a["reason"] = "security-only", []string{"entitled-security"}, p6
			}
			c.apt.mu.Unlock()
		}, "issued", nil, true, false},
		{"R1 binding deleted locally", func(c *aptCLI) { c.editState(func(st *connectState) { st.Binding.Status = "deleted" }) },
			"not_bound", nil, false, false},
		{"R6 rotation pending", func(c *aptCLI) {
			c.editState(func(st *connectState) {
				st.Pending = &connectPendingOp{Intent: "rotate", Phase: "rotate", Operation: "rotate-key", Method: "POST",
					Path: "/connect/deployments/" + st.Binding.DeploymentID + "/rotate-key", Target: st.Binding.DeploymentID,
					BindingEpoch: st.Binding.BindingEpoch, SignerRole: "current", NewKeyProof: true, Body: "e30=",
					BodySHA256: connectv1.BodyDigest([]byte("{}")), IdempotencyKey: "rotate-key-idempotency", CreatedAt: "2026-09-27T00:00:00Z"}
			})
		}, "pending_other_operation", nil, false, false},
		{"R6 feature refresh pending", pendingOp("refresh", "refresh", "refresh", "/connect/refresh"), "pending_other_operation", nil, false, false},
		{"R6 recover request pending", pendingOp("recover", "request", "bind_pending", "/connect/deployments"), "pending_other_operation", nil, false, false},
		{"R7 binding denied", refuse(http.StatusForbidden, "binding_denied", "binding_denied"), "refused", "binding_denied", true, false},
		{"R7 authority denied, revoked", refuse(http.StatusForbidden, "authority_denied", "revoked"), "refused", "authority_denied", true, false},
		{"R7 authority denied, refunded", refuse(http.StatusForbidden, "authority_denied", "refunded"), "refused", "authority_denied", true, false},
		{"R7 authority denied, absent", refuse(http.StatusForbidden, "authority_denied", "absent"), "refused", "authority_denied", true, false},
		{"authority denied without a reason is not R7", refuse(http.StatusForbidden, "authority_denied", ""), "unknown", nil, true, true},
		{"authority denied with another reason is not R7", refuse(http.StatusForbidden, "authority_denied", "provenance_unproven"), "unknown", nil, true, true},
		{"R7 deployment deleted at the service", func(c *aptCLI) {
			c.apt.mu.Lock()
			for _, d := range c.apt.deps {
				d.status = "deleted"
			}
			c.apt.mu.Unlock()
		}, "refused", "binding_denied", true, false},
		{"R8 credential reissue required", refuse(http.StatusForbidden, "credential_reissue_required", ""), "refused", "credential_reissue_required", true, false},
		{"R8 generation stale", refuse(http.StatusConflict, "generation_stale", ""), "refused", "generation_stale", true, false},
		{"R8 operation conflict", refuse(http.StatusConflict, "operation_conflict", ""), "refused", "operation_conflict", true, false},
		{"R9 lost response", func(c *aptCLI) {
			c.apt.mu.Lock()
			c.apt.lose["apt-refresh"] = 1
			c.apt.mu.Unlock()
		}, "unknown", nil, true, true},
		{"R10 ledger outage", refuse(http.StatusServiceUnavailable, "authority_unavailable", ""), "unavailable", "authority_unavailable", true, true},
		{"R10 service unreachable", func(c *aptCLI) { c.apt.srv.Close() }, "unavailable", nil, false, true},
		{"R11 set unresolved", refuse(http.StatusForbidden, "security_set_unresolved", "security_set_unresolved"), "refused", "security_set_unresolved", true, false},
		{"R12 identity mismatch", func(c *aptCLI) { c.replaceIdentity() }, "identity_mismatch", nil, false, false},
		{"R12 busy data directory", func(c *aptCLI) { c.holdLease() }, "busy", nil, false, false},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			c := newAptCLI(t)
			lic, tok := c.license(), c.token()
			tc.arrange(c)
			cycle := aptCycle(7)
			code := c.aptRefresh(cycle)
			if (code == exitcode.OK) != (tc.outcome == "issued") {
				t.Fatalf("exit %d for outcome %s: exit 0 is for issued only (%s)", code, tc.outcome, c.lastError)
			}
			c.assertOutcome(cycle, tc.outcome, tc.code)
			if sends, _ := c.apt.counts(); (sends > 0) != tc.sent {
				t.Fatalf("%d apt-refresh requests; sent should be %v", sends, tc.sent)
			}
			if p := c.state().Pending; (p != nil && p.Intent == "apt-refresh") != tc.pending {
				t.Fatalf("pending %+v; an apt-refresh operation should be pending: %v", p, tc.pending)
			}
			if !bytes.Equal(c.license(), lic) || !bytes.Equal(c.token(), tok) {
				t.Fatal("the licence file or ota.token changed")
			}
			c.assertCredentialOnlyInHandoff()
		})
	}
}

// TestConnectAptRefreshWithoutConnectedStateIsNotBound: row R1 on a data directory that was never
// connected: nothing is sent and the handoff says not_bound.
func TestConnectAptRefreshWithoutConnectedStateIsNotBound(t *testing.T) {
	stub := newAptStub(t)
	handoff := aptHandoffForTest(t)
	dir := t.TempDir()
	cycle := aptCycle(3)
	code, _, _, err := runCLIExit(t, "license", "connect", "apt-refresh", "--cycle", cycle, "--data-dir", dir)
	if code == exitcode.OK || err == nil {
		t.Fatalf("exit %d %v: an unbound data directory must not exit 0", code, err)
	}
	assertAptOutcome(t, handoff, cycle, "not_bound", nil)
	if sends, _ := stub.counts(); sends != 0 {
		t.Fatalf("%d apt-refresh requests from an unbound data directory", sends)
	}
}

// keepAptRefresh leaves an apt-refresh operation pending, as a lost answer does (row R9), and returns it.
func (c *aptCLI) keepAptRefresh(cycle string) *connectPendingOp {
	c.t.Helper()
	c.apt.mu.Lock()
	c.apt.lose["apt-refresh"] = 1
	c.apt.mu.Unlock()
	if code := c.aptRefresh(cycle); code != exitcode.Indeterminate {
		c.t.Fatalf("a lost answer: exit %d (%s)", code, c.lastError)
	}
	p := c.state().Pending
	if p == nil || p.Intent != "apt-refresh" {
		c.t.Fatalf("no apt-refresh operation is pending: %+v", p)
	}
	return p
}

// lastAptKey is the Idempotency-Key of the last apt-refresh request the stub received.
func (c *aptCLI) lastAptKey() string {
	c.t.Helper()
	seen, _ := c.stub.snapshot()
	for i := len(seen) - 1; i >= 0; i-- {
		if seen[i].path == "/connect/apt-refresh" {
			return seen[i].header.Get(connectv1.HeaderIdempotencyKey)
		}
	}
	return ""
}

// TestConnectAptRefreshAudienceIsTheLiteralNotTheOrigin (F2): the stub is a loopback origin and mints the
// literal https://licenses.olivares.ai/apt/v1/ as Q3-S1 does; that answer is issued. An audience derived
// from the bound origin is not the adapter's root: unknown, and the operation is kept.
func TestConnectAptRefreshAudienceIsTheLiteralNotTheOrigin(t *testing.T) {
	c := newAptCLI(t)
	first := aptCycle(1)
	if code := c.aptRefresh(first); code != exitcode.OK {
		t.Fatalf("the literal audience through a loopback origin: exit %d (%s)", code, c.lastError)
	}
	c.assertOutcome(first, "issued", nil)
	origin := c.apt.srv.URL
	c.apt.mu.Lock()
	c.apt.editClaims = func(cl map[string]any) { cl["aud"] = origin + "/apt/v1/" }
	c.apt.mu.Unlock()
	second := aptCycle(2)
	if code := c.aptRefresh(second); code != exitcode.Indeterminate {
		t.Fatalf("the bound origin's audience: exit %d, want %d (%s)", code, exitcode.Indeterminate, c.lastError)
	}
	c.assertOutcome(second, "unknown", nil)
	if p := c.state().Pending; p == nil || p.Intent != "apt-refresh" {
		t.Fatalf("a failed audience check must keep the operation: %+v", p)
	}
}

// TestConnectAptRefreshNeverBlocksAnotherOperation (F4): refresh, upgrade --connect, rotate-key, recover and
// deactivate supersede a pending apt-refresh. Each drops it and records that in the state (saved without it)
// and in the output (the report's superseded entry and a notice) before it starts; the next cycle starts a
// new apt-refresh operation. An apt-refresh is idempotent, so nothing is lost.
func TestConnectAptRefreshNeverBlocksAnotherOperation(t *testing.T) {
	superseded := func(t *testing.T, rep map[string]any) {
		t.Helper()
		if sup, _ := rep["superseded"].(map[string]any); sup["intent"] != "apt-refresh" {
			t.Fatalf("the report does not record the superseded apt-refresh: %v", rep)
		}
	}
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, c *aptCLI)
		next string // the outcome of the next apt-refresh cycle
	}{
		{"refresh", func(t *testing.T, c *aptCLI) {
			code, rep := c.run("refresh")
			if code != exitcode.OK || rep["status"] != "refreshed" {
				t.Fatalf("refresh = %d %v (%s)", code, rep, c.lastError)
			}
			superseded(t, rep)
		}, "issued"},
		{"upgrade --connect", func(t *testing.T, c *aptCLI) {
			var out bytes.Buffer
			token, _, err := connectRefreshForUpgrade(context.Background(),
				&upgradeOptions{dataDir: c.dir, channel: "stable", timeout: 30 * time.Second}, &out)
			c.outputs = append(c.outputs, out.String())
			if err != nil || token == "" {
				t.Fatalf("the connect step of upgrade --connect: %v", err)
			}
		}, "issued"},
		{"rotate-key", func(t *testing.T, c *aptCLI) {
			code, rep := c.run("rotate-key")
			if code != exitcode.OK || rep["status"] != "rotated" {
				t.Fatalf("rotate-key = %d %v (%s)", code, rep, c.lastError)
			}
			superseded(t, rep)
		}, "issued"},
		{"recover", func(t *testing.T, c *aptCLI) {
			code, rep := c.run("recover", "--evidence", c.purchaseEvidence())
			if code != exitcode.OK || rep["status"] != "approval_pending" {
				t.Fatalf("recover = %d %v (%s)", code, rep, c.lastError)
			}
			superseded(t, rep)
		}, "pending_other_operation"},
		{"deactivate", func(t *testing.T, c *aptCLI) {
			// deactivate's confirmation runs only when no step is pending, so the run that drops the
			// apt-refresh stops there and asks to be run again; that run confirms as usual.
			seenBefore, _ := c.stub.snapshot()
			if code, _ := c.run("deactivate", "--yes"); code != exitcode.Usage || !strings.Contains(c.lastError, "superseded") {
				t.Fatalf("deactivate over a pending apt-refresh = %d (%s)", code, c.lastError)
			}
			if seen, _ := c.stub.snapshot(); len(seen) != len(seenBefore) {
				t.Fatal("the run that superseded the apt-refresh sent a request")
			}
			if p := c.state().Pending; p != nil {
				t.Fatalf("the apt-refresh was not dropped: %+v", p)
			}
			if code, rep := c.run("deactivate", "--yes"); code != exitcode.OK || rep["status"] != "deactivated" {
				t.Fatalf("deactivate, run again = %d %v (%s)", code, rep, c.lastError)
			}
		}, "not_bound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newAptCLI(t)
			var notices bytes.Buffer
			saved := connectNoticeOut
			connectNoticeOut = &notices
			t.Cleanup(func() { connectNoticeOut = saved })
			kept := c.keepAptRefresh(aptCycle(1))

			tc.run(t, c)
			if p := c.state().Pending; p != nil && p.Intent == "apt-refresh" {
				t.Fatalf("the apt-refresh operation is still pending after %s", tc.name)
			}
			if !strings.Contains(notices.String(), "superseded the pending apt-refresh operation") {
				t.Fatalf("no notice records the superseded apt-refresh: %q", notices.String())
			}

			next := aptCycle(2)
			code := c.aptRefresh(next)
			if (code == exitcode.OK) != (tc.next == "issued") {
				t.Fatalf("the next cycle: exit %d for outcome %s (%s)", code, tc.next, c.lastError)
			}
			c.assertOutcome(next, tc.next, nil)
			if tc.next == "issued" && c.lastAptKey() == kept.IdempotencyKey {
				t.Fatal("the next cycle repeated the superseded operation instead of starting a new one")
			}
			c.assertCredentialOnlyInHandoff()
		})
	}
}
