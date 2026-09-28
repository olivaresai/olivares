// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/portal/auth"
	"github.com/olivaresai/olivares/appliance/layer/portal/helperclient"
)

// localStack is the host's local-console sign-in service as a case describes it. It never sees a
// credential either: the real service carries the operator's through its own conversation on the
// terminal, so a fake that only answers is a complete double.
type localStack struct {
	accept                                  map[string]bool
	refuseAccount                           map[string]bool
	services, logins                        []string
	authenticated, accounted, closed, opens int
}

func (s *localStack) Start(service, login string) (auth.Transaction, error) {
	s.opens++
	s.services = append(s.services, service)
	s.logins = append(s.logins, login)
	return &transaction{stack: s, login: login}, nil
}

type transaction struct {
	stack *localStack
	login string
}

func (t *transaction) Authenticate() error {
	t.stack.authenticated++
	if !t.stack.accept[t.login] {
		return errors.New("the service refused the credential")
	}
	return nil
}

func (t *transaction) Account() error {
	t.stack.accounted++
	if t.stack.refuseAccount[t.login] {
		return errors.New("the service refused the account")
	}
	return nil
}

func (t *transaction) Close() error {
	t.stack.closed++
	return nil
}

// hostAccounts is the host's account database: each login's uid and groups.
type hostAccounts map[string]hostAccount

type hostAccount struct {
	uid    uint32
	groups []string
}

func (a hostAccounts) Lookup(login string) (uint32, []string, error) {
	account, ok := a[login]
	if !ok {
		return 0, nil, errors.New("no such login")
	}
	return account.uid, slices.Clone(account.groups), nil
}

// attested is the console's own process as the kernel describes it.
type attested helperschema.Peer

func (a attested) Attest() (helperschema.Peer, error) { return helperschema.Peer(a), nil }

// onTty1 is the console's process where it belongs: root, in its unit, on /dev/tty1.
var onTty1 = attested{UID: 0, Account: "root", Unit: helperschema.RepairConsole.Unit, TTY: "/dev/tty1", Attested: true}

// fakeClock is a clock the case moves.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// e12Record is E12's host record as a fixture: the human the host layer created and recorded.
type e12Record struct {
	human Human
	err   error
}

func (r e12Record) RecordedHuman() (Human, error) { return r.human, r.err }

// e12Entry is E12's account-recovery entry, set-admin-password, as a fixture that remembers whom it
// was asked for.
type e12Entry struct {
	asked []Human
	err   error
}

func (e *e12Entry) SetAdminCredential(_ context.Context, human Human) error {
	e.asked = append(e.asked, human)
	return e.err
}

// fixture is one console on tty1 with its local sign-in, E12's record and entry, and a power helper.
type fixture struct {
	stack     *localStack
	accounts  hostAccounts
	clock     *fakeClock
	entry     *e12Entry
	power     *powerHelper
	helperDir string
	console   Console
}

// recordedHuman is the human E12's host layer created and recorded, in the E12 record fixture.
var recordedHuman = Human{Login: "olivares", UID: 1000}

func newFixture(t *testing.T, terminal attested, record HostRecord) *fixture {
	t.Helper()
	f := &fixture{
		stack: &localStack{accept: map[string]bool{"olivares": true, "root": true, "mallory": true}, refuseAccount: map[string]bool{}},
		accounts: hostAccounts{
			"olivares": {uid: 1000, groups: []string{"olivares", auth.AdministratorsGroup}},
			"root":     {uid: 0, groups: []string{"root"}},
			"mallory":  {uid: 1001, groups: []string{"mallory", "wheel"}},
		},
		clock: &fakeClock{now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)},
		entry: &e12Entry{},
	}
	dir := socketDir(t)
	f.helperDir = dir
	f.power = startPowerHelper(t, dir, `{"result": "performed"}`+"\n")
	f.console = NewConsole(auth.State{Mode: auth.Unavailable}).
		WithHelper(helperclient.Client{Dir: dir, Owner: uint32(os.Getuid())}).
		WithAuthority(Authority{Stack: f.stack, Accounts: f.accounts, Terminal: terminal, Now: f.clock.Now, Random: rand.Reader}).
		WithHostRecord(record).
		WithAccountRecovery(f.entry)
	return f
}

// ordinaryActs are the menu numbers of the acts a qualified sign-in performs in this version.
func ordinaryActs(t *testing.T) []string {
	t.Helper()
	var positions []string
	for _, v := range []auth.Verb{auth.VerbPower, auth.VerbCertificate, auth.VerbNetwork, auth.VerbRepairPortalPAM} {
		positions = append(positions, verbPosition(t, v))
	}
	return positions
}

func TestConsole_OrdinaryPhysicalAccessWithoutAuthenticationRefuses(t *testing.T) {
	f := newFixture(t, onTty1, e12Record{human: recordedHuman})
	var lines []string
	for i := range Verbs() {
		lines = append(lines, verbPositionAt(i), "reboot")
	}
	code, out := session(t, f.console, append(lines, "q")...)
	if code != 0 {
		t.Fatalf("the console exited %d:\n%s", code, out)
	}
	if got := f.power.verbs(); len(got) != 0 {
		t.Fatalf("physical access alone asked the power helper %q:\n%s", got, out)
	}
	if strings.Count(out, "needs its qualified sign-in") < len(Verbs()) {
		t.Fatalf("a verb selected without a sign-in was not refused for it:\n%s", out)
	}
	if f.stack.opens != 0 || len(f.entry.asked) != 0 {
		t.Fatalf("selecting verbs opened the sign-in service %d times or E12's entry %d times", f.stack.opens, len(f.entry.asked))
	}
	for _, v := range Verbs() {
		if answer := f.console.Select(v); !strings.Contains(answer, string(v)) || !strings.Contains(answer, "needs its qualified sign-in") {
			t.Errorf("selecting %s without a sign-in answers %q", v, answer)
		}
	}
	header := strings.Join(f.console.Header(), "\n")
	if !strings.Contains(header, "never by access to this machine") {
		t.Errorf("the header does not say that access alone authorizes nothing:\n%s", header)
	}
}

func TestConsole_RecoveryUsesTheIndependentLocalPamServiceAndCurrentAdminMembership(t *testing.T) {
	t.Run("the sign-in opens the host's local-console service, runs auth and account, and closes once", func(t *testing.T) {
		f := newFixture(t, onTty1, e12Record{human: recordedHuman})
		code, out := session(t, f.console, "s", "olivares", powerPosition(t), "nothing", "q")
		if code != 0 || !strings.Contains(out, "Signed in as olivares") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if !slices.Equal(f.stack.services, []string{LocalService}) || LocalService != "login" || LocalService == auth.PAMService {
			t.Fatalf("the sign-in opened %q, want the distribution's local-console service %q and never %q",
				f.stack.services, "login", auth.PAMService)
		}
		if f.stack.authenticated != 1 || f.stack.accounted != 1 || f.stack.closed != 1 {
			t.Fatalf("auth %d, account %d, close %d: want each exactly once", f.stack.authenticated, f.stack.accounted, f.stack.closed)
		}
		if !strings.Contains(out, powerConfirm) {
			t.Fatalf("a qualified sign-in was not offered power:\n%s", out)
		}
	})

	t.Run("the portal's own stack being unusable does not touch this sign-in", func(t *testing.T) {
		f := newFixture(t, onTty1, e12Record{human: recordedHuman})
		broken := auth.NewSelector(downProduct{}, auth.NewPAM(brokenStack{t: t})).Select()
		console := f.console
		console.network = broken
		if _, out := session(t, console, "s", "olivares", "q"); !strings.Contains(out, "Signed in as olivares") {
			t.Fatalf("a broken portal stack refused the console's own sign-in:\n%s", out)
		}
	})

	t.Run("an account the service refuses is not signed in", func(t *testing.T) {
		f := newFixture(t, onTty1, e12Record{human: recordedHuman})
		f.stack.refuseAccount["olivares"] = true
		_, out := session(t, f.console, "s", "olivares", powerPosition(t), "reboot", "q")
		if strings.Contains(out, "Signed in") || len(f.power.verbs()) != 0 || f.stack.closed != 1 {
			t.Fatalf("an account the service refused was signed in:\n%s", out)
		}
	})

	t.Run("a credential the service accepts is not enough: root or current membership of olivares-admins", func(t *testing.T) {
		f := newFixture(t, onTty1, e12Record{human: recordedHuman})
		_, out := session(t, f.console, "s", "mallory", powerPosition(t), "reboot", "q")
		if strings.Contains(out, "Signed in") || len(f.power.verbs()) != 0 {
			t.Fatalf("a login outside olivares-admins was signed in:\n%s", out)
		}
		if _, out := session(t, f.console, "s", "root", "q"); !strings.Contains(out, "Signed in as root") {
			t.Fatalf("root was refused:\n%s", out)
		}
	})

	t.Run("membership is read again at every act", func(t *testing.T) {
		f := newFixture(t, onTty1, e12Record{human: recordedHuman})
		signIn, err := f.console.authority.SignIn("olivares")
		if err != nil {
			t.Fatalf("control: the sign-in was refused: %v", err)
		}
		if !signIn.Qualified() {
			t.Fatal("control: a fresh sign-in is not qualified")
		}
		f.accounts["olivares"] = hostAccount{uid: 1000, groups: []string{"olivares"}}
		if signIn.Qualified() {
			t.Fatal("a sign-in stayed qualified after its login left olivares-admins")
		}
		f.accounts["olivares"] = hostAccount{uid: 1000, groups: []string{"olivares", auth.AdministratorsGroup}}
		if signIn.Qualified() {
			t.Fatal("a sign-in that lost its authority was qualified again without a new sign-in")
		}
		replaced, err := f.console.authority.SignIn("olivares")
		if err != nil {
			t.Fatal(err)
		}
		f.accounts["olivares"] = hostAccount{uid: 4242, groups: []string{auth.AdministratorsGroup}}
		if replaced.Qualified() {
			t.Fatal("a sign-in stayed qualified after its login came to name another uid")
		}
	})
}

func TestConsole_IdleExitOrRestartDropsAuthority(t *testing.T) {
	t.Run("five minutes without input end the sign-in", func(t *testing.T) {
		f := newFixture(t, onTty1, e12Record{human: recordedHuman})
		start := f.clock.now
		power := powerPosition(t)
		input := &stepped{lines: []string{"s", "olivares", power, "nothing", power, "q"}, before: map[int]func(){
			2: func() { f.clock.now = start.Add(IdleLimit - time.Second) },
			4: func() { f.clock.now = start.Add(2*IdleLimit + time.Second) },
		}}
		var out, diagnostics strings.Builder
		if code := f.console.Run(input, &out, &diagnostics); code != 0 {
			t.Fatalf("the console exited %d:\n%s", code, out.String())
		}
		text := out.String()
		if strings.Count(text, powerConfirm) != 1 || strings.Count(text, "needs its qualified sign-in") != 1 {
			t.Fatalf("the act within five minutes was not offered once and the act after them refused once:\n%s", text)
		}
		if !strings.Contains(text, "five minutes") {
			t.Fatalf("the console does not say why the sign-in ended:\n%s", text)
		}
	})

	t.Run("leaving the console ends the sign-in it held", func(t *testing.T) {
		f := newFixture(t, onTty1, e12Record{human: recordedHuman})
		signIn, err := f.console.authority.SignIn("olivares")
		if err != nil || !signIn.Qualified() {
			t.Fatalf("control: %v", err)
		}
		if code, _ := session(t, f.console.withSignIn(signIn), "q"); code != 0 {
			t.Fatalf("the console exited %d", code)
		}
		if signIn.Qualified() {
			t.Fatal("the sign-in outlived the console session that held it")
		}
	})

	t.Run("a restarted console holds no sign-in", func(t *testing.T) {
		f := newFixture(t, onTty1, e12Record{human: recordedHuman})
		if _, out := session(t, f.console, "s", "olivares", "q"); !strings.Contains(out, "Signed in as olivares") {
			t.Fatalf("control: the sign-in was refused:\n%s", out)
		}
		_, out := session(t, f.console, powerPosition(t), "reboot", "q")
		if len(f.power.verbs()) != 0 || !strings.Contains(out, "needs its qualified sign-in") {
			t.Fatalf("a restarted console acted on an earlier sign-in:\n%s", out)
		}
	})
}

func TestConsole_FirstPasswordUsesOnlyTheRecordedE12Human(t *testing.T) {
	f := newFixture(t, onTty1, e12Record{human: recordedHuman})
	// The operator is asked for nothing to name: the entry takes the recorded human, whatever is typed.
	code, out := session(t, f.console, "r", "root", "q")
	if code != 0 {
		t.Fatalf("the console exited %d:\n%s", code, out)
	}
	if !slices.Equal(f.entry.asked, []Human{recordedHuman}) {
		t.Fatalf("E12's entry was asked for %+v, want exactly the recorded human %+v", f.entry.asked, recordedHuman)
	}
	if !strings.Contains(out, "olivares") || !strings.Contains(out, "sign in") {
		t.Fatalf("the console does not name the recorded human and the sign-in that follows:\n%s", out)
	}

	t.Run("a human E12 did not record, or one it no longer can, is never the target", func(t *testing.T) {
		for name, record := range map[string]e12Record{
			"a record naming root":                 {human: Human{Login: "root", UID: 0}},
			"a record whose login has another uid": {human: Human{Login: "olivares", UID: 1001}},
			"a record naming an absent login":      {human: Human{Login: "gone", UID: 1002}},
			"a login that is not a login":          {human: Human{Login: "../olivares", UID: 1000}},
		} {
			f := newFixture(t, onTty1, record)
			if _, out := session(t, f.console, "r", "q"); len(f.entry.asked) != 0 || !strings.Contains(out, "refused") {
				t.Errorf("%s: E12's entry was asked for %+v:\n%s", name, f.entry.asked, out)
			}
		}
	})

	t.Run("the console creates no human: restore is E12's own entry", func(t *testing.T) {
		f := newFixture(t, onTty1, e12Record{human: Human{Login: "gone", UID: 1002}})
		_, out := session(t, f.console, "r", "q")
		if len(f.entry.asked) != 0 || !strings.Contains(out, "restore-human") {
			t.Fatalf("an absent recorded human was not left to E12's restore-human:\n%s", out)
		}
	})
}

func TestConsole_RecoveryNeverGrantsOrdinaryAuthority(t *testing.T) {
	f := newFixture(t, onTty1, e12Record{human: recordedHuman})
	lines := []string{"s", "olivares", "r"}
	for _, act := range ordinaryActs(t) {
		lines = append(lines, act, "reboot")
	}
	_, out := session(t, f.console, append(lines, "q")...)
	if len(f.entry.asked) != 1 {
		t.Fatalf("control: E12's entry was asked %d times", len(f.entry.asked))
	}
	if got := f.power.verbs(); len(got) != 0 {
		t.Fatalf("after account recovery the power helper was asked %q:\n%s", got, out)
	}
	if strings.Count(out, "needs its qualified sign-in") < len(ordinaryActs(t)) {
		t.Fatalf("an ordinary act after account recovery was not refused for its sign-in:\n%s", out)
	}

	t.Run("a failed recovery grants nothing either", func(t *testing.T) {
		f := newFixture(t, onTty1, e12Record{human: recordedHuman})
		f.entry.err = errors.New("E12 refused")
		_, out := session(t, f.console, "r", powerPosition(t), "reboot", "q")
		if len(f.power.verbs()) != 0 || !strings.Contains(out, "needs its qualified sign-in") {
			t.Fatalf("a refused recovery acted:\n%s", out)
		}
	})

	t.Run("the next ordinary act needs a new sign-in with the new credential", func(t *testing.T) {
		f := newFixture(t, onTty1, e12Record{human: recordedHuman})
		_, out := session(t, f.console, "r", "s", "olivares", powerPosition(t), "nothing", "q")
		if f.stack.opens != 1 || !strings.Contains(out, "Signed in as olivares") || !strings.Contains(out, powerConfirm) {
			t.Fatalf("the new sign-in after recovery did not go through the local service:\n%s", out)
		}
	})
}

func TestConsole_EmptyPasswordInventoryDoesNotSelectBootstrap(t *testing.T) {
	f := newFixture(t, onTty1, e12Record{human: recordedHuman})
	// No administrator has a usable credential: the service accepts nobody.
	f.stack.accept = map[string]bool{}
	var lines []string
	for i := range Verbs() {
		lines = append(lines, verbPositionAt(i), "reboot")
	}
	lines = append(lines, "s", "olivares", "s", "root", powerPosition(t), "reboot")
	code, out := session(t, f.console, append(lines, "q")...)
	if code != 0 {
		t.Fatalf("the console exited %d:\n%s", code, out)
	}
	if len(f.entry.asked) != 0 {
		t.Fatalf("an empty inventory selected E12's entry by itself: %+v", f.entry.asked)
	}
	if len(f.power.verbs()) != 0 || strings.Contains(out, "Signed in") {
		t.Fatalf("an empty inventory granted an act:\n%s", out)
	}
	for _, never := range []string{"factory", "first boot", "setup"} {
		if strings.Contains(strings.ToLower(out), never) {
			t.Errorf("an empty inventory offered %s:\n%s", never, out)
		}
	}
	if strings.Count(out, "The sign-in is refused.") != 2 {
		t.Fatalf("each refused sign-in is not stated once, in one phrase:\n%s", out)
	}
}

func TestConsole_ForeignTerminalAccountOrCorruptRecordRefuses(t *testing.T) {
	for name, terminal := range map[string]attested{
		"the second virtual console": {UID: 0, Account: "root", Unit: helperschema.RepairConsole.Unit, TTY: "/dev/tty2", Attested: true},
		"a pseudo-terminal":          {UID: 0, Account: "root", Unit: helperschema.RepairConsole.Unit, TTY: "device 136:0", Attested: true},
		"another unit on tty1":       {UID: 0, Account: "root", Unit: "getty@tty1.service", TTY: "/dev/tty1", Attested: true},
		"a non-root process":         {UID: 1000, Account: "olivares", Unit: helperschema.RepairConsole.Unit, TTY: "/dev/tty1", Attested: true},
		"an unattested process":      {UID: 0, Account: "root", Unit: helperschema.RepairConsole.Unit, TTY: "/dev/tty1"},
	} {
		t.Run("a foreign terminal: "+name, func(t *testing.T) {
			f := newFixture(t, terminal, e12Record{human: recordedHuman})
			_, out := session(t, f.console, "s", "olivares", "r", powerPosition(t), "reboot", "q")
			if f.stack.opens != 0 || len(f.entry.asked) != 0 || len(f.power.verbs()) != 0 {
				t.Fatalf("a foreign terminal opened the sign-in service %d times, E12's entry %d times, power %q:\n%s",
					f.stack.opens, len(f.entry.asked), f.power.verbs(), out)
			}
			if !strings.Contains(out, "The sign-in is refused.") {
				t.Fatalf("the refusal is not stated:\n%s", out)
			}
		})
	}

	t.Run("a foreign account", func(t *testing.T) {
		f := newFixture(t, onTty1, e12Record{human: recordedHuman})
		for _, login := range []string{"mallory", "nobody-here", "../root", "Olivares", ""} {
			if _, err := f.console.authority.SignIn(login); err == nil {
				t.Errorf("%q was signed in", login)
			}
		}
		if slices.Contains(f.stack.logins, "../root") || slices.Contains(f.stack.logins, "") {
			t.Errorf("a text that is not a login reached the sign-in service: %q", f.stack.logins)
		}
	})

	t.Run("a corrupt or unreadable E12 record", func(t *testing.T) {
		for name, record := range map[string]HostRecord{
			"corrupt":   e12Record{err: errors.New("the host record does not parse")},
			"absent":    e12Record{err: os.ErrNotExist},
			"not wired": nil,
		} {
			f := newFixture(t, onTty1, record)
			_, out := session(t, f.console, "r", "q")
			if len(f.entry.asked) != 0 || !strings.Contains(out, "refused") {
				t.Errorf("%s: E12's entry was asked for %+v:\n%s", name, f.entry.asked, out)
			}
		}
	})
}

// verbPositionAt is the menu number of the verb at index i.
func verbPositionAt(i int) string { return string(rune('1' + i)) }

// stepped yields one line per read and runs the hook for a line before yielding it, so a case can
// move the clock between two inputs.
type stepped struct {
	lines  []string
	before map[int]func()
	next   int
}

func (s *stepped) Read(p []byte) (int, error) {
	if s.next >= len(s.lines) {
		return 0, io.EOF
	}
	if hook := s.before[s.next]; hook != nil {
		hook()
	}
	n := copy(p, s.lines[s.next]+"\n")
	s.next++
	return n, nil
}
