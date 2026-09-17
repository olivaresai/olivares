// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/webaddr"
)

// seedConsoleState writes a data directory that looks like one an engine has
// booted in: the recorded console address, and optionally a pending setup token.
func seedConsoleState(t *testing.T, pending bool) string {
	t.Helper()
	dir := t.TempDir()
	addr := resolveConsoleAddress(webaddr.Address{}, ":8443", false).withPlan(webAuthnPlan{Source: "per-request"})
	if err := writeConsoleState(dir, newConsoleState(addr, ":8443", ":8444", false)); err != nil {
		t.Fatalf("writeConsoleState: %v", err)
	}
	if pending {
		if _, created, err := secure.NewSetupToken(filepath.Join(dir, "setup.token")).Ensure(); err != nil || !created {
			t.Fatalf("mint setup token: created=%v err=%v", created, err)
		}
	}
	return dir
}

// A DATA DIRECTORY NO ENGINE HAS BOOTED IN IS A USAGE ERROR THAT NAMES ITSELF.
// The likeliest cause is the wrong --data-dir, so the message has to say which
// directory was looked at and offer both readings.
func TestFirstBootRefusesADirectoryNoEngineHasRecorded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var out bytes.Buffer
	err := runFirstBoot(&out, dir, false)
	if err == nil {
		t.Fatal("an unrecorded data directory reported success")
	}
	if code := exitcode.From(err); code != exitcode.Usage {
		t.Errorf("exit code = %d, want %d (the invocation is what is wrong)", code, exitcode.Usage)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the refusal does not name the directory it looked at: %v", err)
	}
}

// PENDING SETUP: THE REPORT SAYS WHERE TO GO, SAYS THE TOKEN CANNOT BE SHOWN, AND
// PRINTS NO TOKEN. The last clause is the one that matters: a command that leaked
// a recoverable token would mean the engine had stopped hashing it.
func TestFirstBootPendingSetupShowsTheAddressAndNoToken(t *testing.T) {
	t.Parallel()
	dir := seedConsoleState(t, true)
	var out bytes.Buffer
	if err := runFirstBoot(&out, dir, false); err != nil {
		t.Fatalf("runFirstBoot: %v", err)
	}
	report := out.String()
	for _, want := range []string{"Setup is PENDING", "cannot be shown again", "--new-token", "Console:"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not contain %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "olst_") {
		t.Errorf("the report printed a setup token:\n%s", report)
	}
	// The address list is printed ONCE. The banner's paragraph carries its own
	// copy of the list, and recording that copy here would render it twice.
	if n := strings.Count(report, "https://127.0.0.1:8443"); n != 1 {
		t.Errorf("loopback address printed %d times, want exactly 1:\n%s", n, report)
	}
}

// --new-token MINTS ONE AND RETIRES THE PREVIOUS ONE, in that order, so no moment
// exists in which two tokens are valid. The running engine reads the file on
// every Verify, so this takes effect without a restart.
func TestFirstBootNewTokenReplacesThePreviousOne(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	addr := resolveConsoleAddress(webaddr.Address{}, ":8443", false).withPlan(webAuthnPlan{Source: "per-request"})
	if err := writeConsoleState(dir, newConsoleState(addr, ":8443", ":8444", false)); err != nil {
		t.Fatalf("writeConsoleState: %v", err)
	}
	tok := secure.NewSetupToken(filepath.Join(dir, "setup.token"))
	first, created, err := tok.Ensure()
	if err != nil || !created {
		t.Fatalf("mint the first token: created=%v err=%v", created, err)
	}

	var out bytes.Buffer
	if err := runFirstBoot(&out, dir, true); err != nil {
		t.Fatalf("runFirstBoot --new-token: %v", err)
	}
	report := out.String()
	i := strings.Index(report, "olst_")
	if i < 0 {
		t.Fatalf("no replacement token was printed:\n%s", report)
	}
	second := strings.Fields(report[i:])[0]
	if second == first {
		t.Fatal("the 'replacement' is the previous token")
	}
	if !tok.Verify(second) {
		t.Error("the engine would not accept the replacement token")
	}
	if tok.Verify(first) {
		t.Error("the previous token still verifies; it must be retired by the reissue")
	}
}

// ONCE AN ADMINISTRATOR EXISTS THE REPORT SAYS SO, AND --new-token IS REFUSED.
// The token gates the creation of the FIRST administrator; minting one afterwards
// would be a way to make a second.
func TestFirstBootAfterSetupRefusesToMintAToken(t *testing.T) {
	t.Parallel()
	dir := seedConsoleState(t, false)
	var out bytes.Buffer
	if err := runFirstBoot(&out, dir, false); err != nil {
		t.Fatalf("runFirstBoot: %v", err)
	}
	if !strings.Contains(out.String(), "Setup is COMPLETE") {
		t.Errorf("a data directory with no setup token was not reported as set up:\n%s", out.String())
	}

	out.Reset()
	err := runFirstBoot(&out, dir, true)
	if err == nil {
		t.Fatal("--new-token was accepted after setup")
	}
	if code := exitcode.From(err); code != exitcode.Err {
		t.Errorf("exit code = %d, want %d", code, exitcode.Err)
	}
	if !strings.Contains(out.String(), "refused") {
		t.Errorf("the refusal is not stated on stdout:\n%s", out.String())
	}
	if _, statErr := os.Stat(filepath.Join(dir, "setup.token")); !os.IsNotExist(statErr) {
		t.Error("a setup token was created after setup was complete")
	}
}

// AN UNREADABLE RECORD DOES NOT SUPPRESS THE HALF THAT MATTERS. Somebody who
// cannot get in needs the setup state; the address is the part that failed to
// load, and only that part is reported as missing.
func TestFirstBootReportsSetupStateWhenTheRecordIsUnreadable(t *testing.T) {
	t.Parallel()
	dir := seedConsoleState(t, true)
	if err := os.WriteFile(filepath.Join(dir, consoleStateFile), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt the record: %v", err)
	}
	var out bytes.Buffer
	if err := runFirstBoot(&out, dir, false); err != nil {
		t.Fatalf("runFirstBoot: %v", err)
	}
	report := out.String()
	if !strings.Contains(report, "could not be read") {
		t.Errorf("the unreadable record is not reported:\n%s", report)
	}
	if !strings.Contains(report, "Setup is PENDING") {
		t.Errorf("the setup state was suppressed by an unrelated failure:\n%s", report)
	}
}

// A RECORD FROM ANOTHER SCHEMA VERSION IS NAMED, NOT GUESSED AT. This file lives
// in a volume that outlives the binary that wrote it.
func TestConsoleStateRefusesAnUnknownSchemaVersion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body, err := json.Marshal(map[string]any{"version": consoleStateVersion + 7, "browse": "https://x:8443"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, consoleStateFile), body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := readConsoleState(dir); err == nil || !strings.Contains(err.Error(), "schema version") {
		t.Errorf("readConsoleState accepted a foreign schema version: %v", err)
	}
}

// THE RECORD ROUND-TRIPS, IS 0600, AND CARRIES NO SECRET. The last clause is
// checked against the token that exists in the same directory: the record must
// not be a second place the token lives.
func TestConsoleStateRoundTripAndPermissions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	token, _, err := secure.NewSetupToken(filepath.Join(dir, "setup.token")).Ensure()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	addr := resolveConsoleAddress(webaddr.Address{}, ":8443", false).withPlan(webAuthnPlan{Source: "per-request"})
	want := newConsoleState(addr, ":8443", ":8444", false)
	if err := writeConsoleState(dir, want); err != nil {
		t.Fatalf("writeConsoleState: %v", err)
	}
	got, err := readConsoleState(dir)
	if err != nil {
		t.Fatalf("readConsoleState: %v", err)
	}
	if got.Browse != want.Browse || got.Listen != want.Listen || got.GRPCListen != want.GRPCListen ||
		len(got.Addresses) != len(want.Addresses) || got.Container != want.Container {
		t.Errorf("round trip lost fields:\n got %+v\nwant %+v", got, want)
	}
	info, err := os.Stat(filepath.Join(dir, consoleStateFile))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("console state mode = %04o, want 0600", mode)
	}
	body, err := os.ReadFile(filepath.Join(dir, consoleStateFile))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if bytes.Contains(body, []byte(token)) {
		t.Error("the recorded console state contains the setup token")
	}
	// The recorded advice must not repeat the address list: first-boot prints the
	// addresses itself from the Addresses field.
	if strings.Contains(got.Advice, "answers\nat each of these") {
		t.Errorf("the recorded advice carries its own copy of the address list:\n%s", got.Advice)
	}
	// And no temporary file is left behind by the atomic publish.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), consoleStateFile+".") {
			t.Errorf("the atomic write left %s behind", e.Name())
		}
	}
}

// THE ENGINE ACTUALLY WRITES THE RECORD, and first-boot reads what that boot
// produced. The unit rows above drive writeConsoleState directly, which proves
// the file format and nothing about whether serve ever calls it — this row runs
// the real runEngine and then asks the real command.
func TestServeRecordsTheConsoleAddressForFirstBoot(t *testing.T) {
	dir := t.TempDir()
	opts := bindAnnounceInsecureOpts(dir, "127.0.0.1:0", "127.0.0.1:0")
	opts.listen, opts.grpcListen = freeLoopbackAddr(t), freeLoopbackAddr(t)
	var engineOut bytes.Buffer
	res := runBindAnnounce(t, opts, &engineOut, serveAnnounce(true), nil)
	if res.err != nil {
		t.Fatalf("runEngine: %v", res.err)
	}
	if res.announced != 1 {
		t.Fatalf("announcements = %d, want 1", res.announced)
	}
	state, err := readConsoleState(dir)
	if err != nil {
		t.Fatalf("the engine recorded no readable console state: %v", err)
	}
	if state.Listen != opts.listen || state.GRPCListen != opts.grpcListen {
		t.Errorf("recorded binds %q/%q, want %q/%q", state.Listen, state.GRPCListen, opts.listen, opts.grpcListen)
	}
	if !state.Insecure {
		t.Error("the record does not say the engine served plain HTTP")
	}
	if state.BootedAt.IsZero() {
		t.Error("the record carries no boot time")
	}
	var out bytes.Buffer
	if err := runFirstBoot(&out, dir, false); err != nil {
		t.Fatalf("runFirstBoot over a directory a real engine booted in: %v", err)
	}
	if !strings.Contains(out.String(), state.Browse) {
		t.Errorf("first-boot did not report the address the engine recorded (%q):\n%s", state.Browse, out.String())
	}
}

// freeLoopbackAddr reserves a loopback port and releases it. The fixture needs a
// concrete address to probe, and --insecure is refused off-host — which is the
// guard D18 deliberately left in place.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a loopback port: %v", err)
	}
	addr := lis.Addr().String()
	if err := lis.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}
	return addr
}

// A REJECTED WRITE IS A FAILED COMMAND, and the row exists for one line: the
// replacement token. --new-token retires the previous token before minting the
// new one, so a write that silently failed would leave the operator with a token
// they never saw and an exit code that said it worked.
func TestFirstBootReportsARejectedWrite(t *testing.T) {
	t.Parallel()
	dir := seedConsoleState(t, true)
	err := runFirstBoot(rejectingWriter{}, dir, true)
	if err == nil {
		t.Fatal("a rejected write reported success")
	}
	if !strings.Contains(err.Error(), "not written in full") {
		t.Errorf("the failure does not say the report is incomplete: %v", err)
	}
	if !strings.Contains(err.Error(), "already retired") {
		t.Errorf("the failure does not say what --new-token already did: %v", err)
	}
	// And the mint itself still happened, which is exactly why the operator must
	// be told: the previous token no longer works.
	if !secure.NewSetupToken(filepath.Join(dir, "setup.token")).Exists() {
		t.Error("no setup token survives the failed report; the operator would have no way back in")
	}
}

type rejectingWriter struct{}

func (rejectingWriter) Write([]byte) (int, error) { return 0, errors.New("stdout is closed") }
