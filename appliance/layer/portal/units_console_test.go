// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// pamDirective is one line of a PAM service file: the management group it belongs to, its
// control, the module it names and that module's arguments.
type pamDirective struct {
	group   string
	control string
	module  string
	args    []string
}

// pamStacks is the console's sign-in stack per package format: the source each format installs
// at /etc/pam.d/olivares-portal, and the host scheme that stack defers to for who the human is.
// Debian's scheme is its common-auth and common-account files. Fedora's is password-auth, the
// stack authselect gives services a human reaches over the network (Fedora's sshd includes it);
// an include of a file the host does not have fails the whole stack, so each format installs
// only its own.
var pamStacks = []struct {
	packager, path, auth, account string
}{
	{"deb", "units/pam.d/olivares-portal", "common-auth", "common-account"},
	{"rpm", "units/pam.d/rpm/olivares-portal", "password-auth", "password-auth"},
}

// stackProblems names each way directives fall short of the console's stack over a host scheme
// whose auth and account groups are the files auth and account; none means the stack holds.
func stackProblems(directives []pamDirective, auth, account string) []string {
	var problems []string
	groups := map[string]bool{"auth": true, "account": true, "password": true, "session": true}
	for _, d := range directives {
		if !groups[d.group] {
			problems = append(problems, fmt.Sprintf("%q is not one of the four management groups", d.group))
		}
		// No path reaches this stack: a module is named, never a file.
		for _, field := range append([]string{d.module}, d.args...) {
			if strings.Contains(field, "/") {
				problems = append(problems, fmt.Sprintf("the directive for %s carries a path: %q", d.group, field))
			}
		}
		// It includes only its own format's scheme.
		if (d.control == "include" || d.control == "substack") && d.module != auth && d.module != account {
			problems = append(problems, fmt.Sprintf("the %s group includes %s, which is not this format's scheme", d.group, d.module))
		}
	}

	// The group check comes first in the auth group, so a human the host does not place in
	// the named group never reaches the credential check at all.
	var firstAuth pamDirective
	for _, d := range directives {
		if d.group == "auth" {
			firstAuth = d
			break
		}
	}
	if firstAuth.module != "pam_succeed_if.so" || firstAuth.control != "requisite" ||
		len(firstAuth.args) < 3 || !slices.Equal(firstAuth.args[:3], []string{"user", "ingroup", "olivares-admins"}) {
		problems = append(problems, fmt.Sprintf("the first auth directive is %+v, want a requisite group check", firstAuth))
	}

	// The host's own scheme answers who the human is, and the account group refuses the same
	// non-member a second time.
	asked := map[string]bool{}
	for _, d := range directives {
		switch {
		case d.group == "auth" && d.control == "include" && d.module == auth:
			asked["the host's authentication scheme"] = true
		case d.group == "account" && d.control == "include" && d.module == account:
			asked["the host's account scheme"] = true
		case d.group == "account" && d.module == "pam_succeed_if.so" &&
			slices.Contains(d.args, "ingroup") && slices.Contains(d.args, "olivares-admins"):
			asked["the account group's own membership check"] = true
		case d.group == "session" && d.module == "pam_deny.so":
			asked["no session"] = true
		case d.group == "password" && d.module == "pam_deny.so":
			asked["no credential change"] = true
		}
	}
	for _, want := range []string{
		"the host's authentication scheme",
		"the host's account scheme",
		"the account group's own membership check",
		"no session",
		"no credential change",
	} {
		if !asked[want] {
			problems = append(problems, "the stack does not declare: "+want)
		}
	}
	return problems
}

// readPAMService reads the console's PAM service file and returns its directives in order.
// Comments and blank lines carry no directive and are dropped.
func readPAMService(t *testing.T, path string) []pamDirective {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var directives []pamDirective
	for i, line := range strings.Split(string(data), "\n") {
		if cut := strings.IndexByte(line, '#'); cut >= 0 {
			line = line[:cut]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 3 {
			t.Fatalf("%s:%d: not a directive: %q", path, i+1, line)
		}
		directives = append(directives, pamDirective{
			group: fields[0], control: fields[1], module: fields[2], args: fields[3:],
		})
	}
	if len(directives) == 0 {
		t.Fatalf("%s declares no directive", path)
	}
	return directives
}

func TestPortalUnits_TheLastDoorIsOnTty1AndTheStackAdmitsOneGroup(t *testing.T) {
	t.Run("the repair console is shipped disabled and is not a network service", func(t *testing.T) {
		console := readUnit(t, "units/olivares-repair-console.service")

		if _, ok := console["Install"]; ok {
			t.Error("the repair console has an [Install] section, so packaging would enable it at boot")
		}
		for section := range console {
			for _, key := range []string{"WantedBy", "RequiredBy", "UpheldBy", "Alias", "Also", "ListenStream"} {
				if len(console.values(section, key)) > 0 {
					t.Errorf("the repair console declares %s=", key)
				}
			}
		}

		// It is the last door, so it takes the console on tty1 and says so to the login
		// prompt that would otherwise own it.
		for key, want := range map[string]string{
			"TTYPath":        "/dev/tty1",
			"StandardInput":  "tty",
			"StandardOutput": "tty",
			"Type":           "idle",
		} {
			if got := console.values("Service", key); !slices.Equal(got, []string{want}) {
				t.Errorf("service %s=%v, want %s", key, got, want)
			}
		}
		if got := console.values("Unit", "Conflicts"); !slices.Equal(got, []string{"getty@tty1.service"}) {
			t.Errorf("the repair console does not take tty1 from the login prompt: Conflicts=%v", got)
		}

		// It cannot be locked out by the failure that locks out the console on 9443, so it
		// depends on neither, and it is not reachable over the network at all.
		for _, key := range []string{"Requires", "Requisite", "BindsTo", "After", "PartOf"} {
			for _, value := range console.values("Unit", key) {
				if strings.Contains(value, "olivares-portal") {
					t.Errorf("the last door depends on the console on 9443: %s=%s", key, value)
				}
			}
		}
		for key, want := range map[string]string{"PrivateNetwork": "yes", "IPAddressDeny": "any"} {
			if got := console.values("Service", key); !slices.Equal(got, []string{want}) {
				t.Errorf("service %s=%v, want %s", key, got, want)
			}
		}

		start := console.values("Service", "ExecStart")
		if len(start) != 1 || len(strings.Fields(start[0])) != 1 {
			t.Errorf("ExecStart=%v must be the binary alone, with no argument", start)
		}
	})

	t.Run("each format's sign-in stack admits one group and opens no session", func(t *testing.T) {
		for _, stack := range pamStacks {
			for _, problem := range stackProblems(readPAMService(t, stack.path), stack.auth, stack.account) {
				t.Errorf("%s (%s): %s", stack.path, stack.packager, problem)
			}
		}
	})

	t.Run("the formats' stacks differ only in the host scheme they include", func(t *testing.T) {
		shape := func(path string) []string {
			var lines []string
			for _, d := range readPAMService(t, path) {
				module := d.module
				if d.control == "include" {
					module = "(the host's scheme)"
				}
				lines = append(lines, strings.Join(append([]string{d.group, d.control, module}, d.args...), " "))
			}
			return lines
		}
		if deb, rpm := shape(pamStacks[0].path), shape(pamStacks[1].path); !slices.Equal(deb, rpm) {
			t.Errorf("the stacks differ beyond the scheme:\n%s\n%s", strings.Join(deb, "\n"), strings.Join(rpm, "\n"))
		}
	})

	// The negative control: a stack that includes only Debian's common files names a scheme
	// Fedora does not have, so the Fedora format's assertion must refuse it.
	t.Run("a stack of Debian's common files fails the Fedora format's assertion", func(t *testing.T) {
		debian, fedora := pamStacks[0], pamStacks[1]
		if len(stackProblems(readPAMService(t, debian.path), fedora.auth, fedora.account)) == 0 {
			t.Error("the Fedora assertion accepts a stack that includes only common-auth and common-account")
		}
	})
}
