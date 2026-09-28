// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/portal/auth"
	"github.com/olivaresai/olivares/appliance/layer/portal/helperclient"
)

// powerHelper stands in for olivares-portal-power on its socket: it keeps each document it is
// sent and answers each with answer.
type powerHelper struct {
	mu       sync.Mutex
	received []map[string]any
}

// startPowerHelper listens on dir/power.sock until the test ends.
func startPowerHelper(t *testing.T, dir, answer string) *powerHelper {
	t.Helper()
	l, err := net.Listen("unix", filepath.Join(dir, "power.sock"))
	if err != nil {
		t.Fatal(err)
	}
	h := &powerHelper{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			var document map[string]any
			data, _ := io.ReadAll(conn)
			_ = json.Unmarshal(data, &document)
			h.mu.Lock()
			h.received = append(h.received, document)
			h.mu.Unlock()
			_, _ = io.WriteString(conn, answer)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		<-done
	})
	return h
}

func (h *powerHelper) verbs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var verbs []string
	for _, document := range h.received {
		verb, _ := document["verb"].(string)
		// The document is the verb and its operation id (TestConsole_PowerMintsAnOperationIDPerRequest).
		if _, ok := document["operation_id"].(string); len(document) != 2 || !ok {
			verb = "a document with other fields"
		}
		verbs = append(verbs, verb)
	}
	return verbs
}

// operations returns the operation id of each document received, in order.
func (h *powerHelper) operations() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var ids []string
	for _, document := range h.received {
		id, _ := document["operation_id"].(string)
		ids = append(ids, id)
	}
	return ids
}

// socketDir is a directory whose socket paths fit a sockaddr_un.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tty")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// powerPosition is the menu number of power.
func powerPosition(t *testing.T) string {
	t.Helper()
	i := slices.Index(Verbs(), auth.VerbPower)
	if i < 0 {
		t.Fatal("the console does not offer power")
	}
	return strconv.Itoa(i + 1)
}

// session runs the console with the lines and returns its exit code and output.
func session(t *testing.T, console Console, lines ...string) (int, string) {
	t.Helper()
	var out, diagnostics strings.Builder
	code := console.Run(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out, &diagnostics)
	if diagnostics.Len() != 0 {
		t.Fatalf("the console wrote a diagnostic: %q", diagnostics.String())
	}
	return code, out.String()
}

// signInState is an injected ordinary sign-in: qualified or not. It stands in for the qualified
// local-console sign-in Authority makes, for the cases that measure an act rather than the sign-in.
type signInState bool

func (s signInState) Qualified() bool { return bool(s) }

// signedIn is a console holding a qualified sign-in, the only state in which it asks the helper, and
// an admission no gate refuses, for the cases that measure the act rather than its gates.
func signedIn(c Console) Console {
	return c.withSignIn(signInState(true)).WithPowerAdmission(openAdmission())
}

func TestConsole_PowerAsksTheHelperOnlyWithAQualifiedSignIn(t *testing.T) {
	state := auth.State{Mode: auth.Unavailable}
	power := powerPosition(t)
	me := uint32(os.Getuid())
	performed := `{"result": "performed", "detail": "the service manager accepted the request"}` + "\n"

	for _, verb := range []string{"reboot", "shutdown"} {
		t.Run(verb+" is asked of the power helper, once, as a closed document", func(t *testing.T) {
			dir := socketDir(t)
			helper := startPowerHelper(t, dir, performed)
			console := signedIn(NewConsole(state).WithHelper(helperclient.Client{Dir: dir, Owner: me}))
			code, out := session(t, console, power, verb, "q")
			if code != 0 {
				t.Fatalf("the console exited %d:\n%s", code, out)
			}
			if got := helper.verbs(); !slices.Equal(got, []string{verb}) {
				t.Fatalf("the power helper was asked %q, want exactly %q", got, verb)
			}
			if !strings.Contains(out, "power helper performed "+verb) {
				t.Fatalf("the console does not state that the helper performed %s:\n%s", verb, out)
			}
			for _, never := range []string{"not implemented", "completed", "complete."} {
				if strings.Contains(strings.ToLower(out[strings.Index(out, "power helper performed"):]), never) {
					t.Fatalf("the power answer says %q:\n%s", never, out)
				}
			}
		})
	}

	t.Run("anything else asks for nothing", func(t *testing.T) {
		dir := socketDir(t)
		helper := startPowerHelper(t, dir, performed)
		console := signedIn(NewConsole(state).WithHelper(helperclient.Client{Dir: dir, Owner: me}))
		code, out := session(t, console, power, "yes", power, "REBOOT", power, "reboot now", "q")
		if code != 0 || len(helper.verbs()) != 0 || strings.Count(out, "Nothing was asked of the power helper.") != 3 {
			t.Fatalf("exit %d, helper asked %q:\n%s", code, helper.verbs(), out)
		}
	})

	t.Run("an absent helper performs nothing and the console stays", func(t *testing.T) {
		console := signedIn(NewConsole(state).WithHelper(helperclient.Client{Dir: socketDir(t), Owner: me}))
		code, out := session(t, console, power, "reboot", "q")
		if code != 0 || !strings.Contains(out, "power helper is unavailable") || !strings.Contains(out, "nothing was performed") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		if strings.Contains(out, "power helper performed") {
			t.Fatalf("an absent helper was reported as performing:\n%s", out)
		}
	})

	t.Run("a refusal is stated with its code", func(t *testing.T) {
		dir := socketDir(t)
		startPowerHelper(t, dir, `{"result": "refused", "code": "not_admitted"}`+"\n")
		console := signedIn(NewConsole(state).WithHelper(helperclient.Client{Dir: dir, Owner: me}))
		_, out := session(t, console, power, "shutdown", "q")
		if !strings.Contains(out, "refused shutdown (not_admitted)") || strings.Contains(out, "power helper performed") {
			t.Fatalf("a refusal read as:\n%s", out)
		}
	})

	t.Run("a console with no helper wired asks nothing", func(t *testing.T) {
		_, out := session(t, signedIn(NewConsole(state)), power, "reboot", "q")
		if !strings.Contains(out, "no helper is wired") {
			t.Fatalf("an unwired console read as:\n%s", out)
		}
	})

	t.Run("the verbs without an act in this version are still not performed", func(t *testing.T) {
		dir := socketDir(t)
		helper := startPowerHelper(t, dir, performed)
		console := signedIn(NewConsole(state).WithHelper(helperclient.Client{Dir: dir, Owner: me}))
		unbuilt := []auth.Verb{auth.VerbApplyUpdate, auth.VerbRollBack, auth.VerbSupportBundle}
		var lines []string
		for _, v := range unbuilt {
			lines = append(lines, verbPosition(t, v))
		}
		_, out := session(t, console, append(lines, "q")...)
		if strings.Count(strings.ToLower(out), "not implemented") != len(unbuilt) || len(helper.verbs()) != 0 {
			t.Fatalf("the verbs without an act asked the helper %q:\n%s", helper.verbs(), out)
		}
	})
}

func TestConsole_OrdinaryPowerRefusesWithoutQualifiedSignIn(t *testing.T) {
	state := auth.State{Mode: auth.Unavailable}
	power := powerPosition(t)
	me := uint32(os.Getuid())
	for name, answer := range map[string]string{
		"a helper that would perform, as once the guest proof is accepted": `{"result": "performed"}` + "\n",
		"a helper that refuses pidfd_unproven":                             `{"result": "refused", "code": "pidfd_unproven"}` + "\n",
	} {
		for signIn, console := range map[string]func(Console) Console{
			"no sign-in":              func(c Console) Console { return c },
			"a sign-in not qualified": func(c Console) Console { return c.withSignIn(signInState(false)) },
		} {
			t.Run(name+", "+signIn, func(t *testing.T) {
				dir := socketDir(t)
				helper := startPowerHelper(t, dir, answer)
				c := console(NewConsole(state).WithHelper(helperclient.Client{Dir: dir, Owner: me}))
				code, out := session(t, c, power, "reboot", power, "shutdown", "q")
				if code != 0 {
					t.Fatalf("the console exited %d:\n%s", code, out)
				}
				if got := helper.verbs(); len(got) != 0 {
					t.Fatalf("without a qualified sign-in the power helper was asked %q:\n%s", got, out)
				}
				if strings.Count(out, "needs its qualified sign-in") < 2 || strings.Contains(out, "power helper performed") ||
					strings.Contains(out, powerConfirm) {
					t.Fatalf("the refusal is not stated with its reason, or the console offered the act:\n%s", out)
				}
				if said := c.Power("reboot"); !strings.Contains(said, "needs its qualified sign-in") || len(helper.verbs()) != 0 {
					t.Fatalf("Power without a qualified sign-in said %q and asked %q", said, helper.verbs())
				}
			})
		}
	}

	t.Run("the header states that the console authenticates by its sign-in, never by access", func(t *testing.T) {
		header := strings.Join(NewConsole(state).Header(), "\n")
		for _, want := range []string{"its own sign-in", "never by access to this machine", "qualified"} {
			if !strings.Contains(header, want) {
				t.Errorf("the header does not state %q:\n%s", want, header)
			}
		}
		if strings.Contains(header, "authenticates by access") {
			t.Errorf("the header still says the console authenticates by access to the machine:\n%s", header)
		}
	})
}
