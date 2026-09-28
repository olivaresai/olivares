// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package invocation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"syscall"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// fakeKernel describes one connection's peer as the kernel would report it.
type fakeKernel struct {
	pid, pidfdPid int
	uid           uint32
	noPidfd       bool              // the kernel offers no SO_PEERPIDFD
	cgroup        string            // /proc/<pid>/cgroup of the pidfd's process
	stat          string            // /proc/<pid>/stat of the pidfd's process
	exited        bool              // the pidfd's process exited after its cgroup was read
	names         map[uint32]string // accounts the user database names, beside root and operator
	cgroupReads   int
	closed        int
}

const tty1Cgroup = "0::/system.slice/olivares-repair-console.service\n"

// statOnTerminal is a /proc/<pid>/stat line whose controlling terminal is tty_nr.
func statOnTerminal(ttyNr string) string {
	return "4242 (olivares-repair) S 1 4242 4242 " + ttyNr + " 4242 4194560 0 0 0 0 0 0 0 0 20 0 1 0 1 0 0\n"
}

// tty1Stat is the repair console's process on /dev/tty1: major 4, minor 1.
var tty1Stat = statOnTerminal("1025")

// console is the repair console's process on tty1, with a pidfd.
func console() *fakeKernel {
	return &fakeKernel{pid: 4242, pidfdPid: 4242, uid: 0, cgroup: tty1Cgroup, stat: tty1Stat}
}

func (k *fakeKernel) PeerCred(int) (int, uint32, error) { return k.pid, k.uid, nil }

func (k *fakeKernel) PeerPidfd(int) (int, error) {
	if k.noPidfd {
		return 0, syscall.ENOPROTOOPT
	}
	return 77, nil
}

func (k *fakeKernel) PidOf(int) (int, error) { return k.pidfdPid, nil }

func (k *fakeKernel) Cgroup(int) (string, error) {
	k.cgroupReads++
	return k.cgroup, nil
}

func (k *fakeKernel) Stat(int) (string, error) { return k.stat, nil }

// AccountOf names uid as a Debian user database does: root, and an operator's account, unless
// names says otherwise.
func (k *fakeKernel) AccountOf(uid uint32) (string, error) {
	if name, ok := k.names[uid]; ok {
		return name, nil
	}
	switch uid {
	case 0:
		return "root", nil
	case 1000:
		return "operator", nil
	}
	return "", errors.New("unknown uid")
}

func (k *fakeKernel) Alive(int) error {
	if k.exited {
		return syscall.ESRCH
	}
	return nil
}

func (k *fakeKernel) Close(int) { k.closed++ }

// helpers are the seam's helpers with their production admission tables and documents; each
// Perform counts its calls and performs nothing.
func helpers(performed map[string]int) []Helper {
	perform := func(name string) func(context.Context, helperschema.Peer, helperschema.Request) helperschema.Response {
		return func(_ context.Context, _ helperschema.Peer, r helperschema.Request) helperschema.Response {
			performed[name+" "+r.Subcommand()]++
			return helperschema.Response{Result: helperschema.ResultPerformed}
		}
	}
	return []Helper{
		{Name: helperschema.HelperPower, Rules: helperschema.PowerRules(),
			NewRequest: func() helperschema.Request { return &helperschema.PowerRequest{} },
			Perform:    perform(helperschema.HelperPower)},
		{Name: helperschema.HelperSupportBundle, Rules: helperschema.SupportBundleRules(),
			NewRequest: func() helperschema.Request { return &helperschema.SupportBundleRequest{} },
			Perform:    perform(helperschema.HelperSupportBundle)},
	}
}

// operationID is an operation id a client minted.
const operationID = "0123456789abcdef0123456789abcdef"

// documents are one valid document per subcommand.
var documents = map[string]string{
	helperschema.PowerReboot:          `{"verb": "reboot", "operation_id": "` + operationID + `"}`,
	helperschema.PowerShutdown:        `{"verb": "shutdown", "operation_id": "` + operationID + `"}`,
	helperschema.SupportBundleProduce: `{"op": "produce", "operation_id": "` + operationID + `"}`,
}

// serveOne runs one connection through h as it will be served once the appliance's kernel is
// proven to give per-connection instances the peer's pidfd, so these cases measure admission
// itself; TestHelperAdmission_MutationsWaitForTheGuestPidfdProof measures the build as it ships.
// It returns the exit code and the decoded answer.
func serveOne(t *testing.T, h Helper, k Kernel, document string) (int, helperschema.Response) {
	t.Helper()
	return serveProven(t, h, k, true, document)
}

// serveProven runs one connection through h on the given side of the guest proof.
func serveProven(t *testing.T, h Helper, k Kernel, proven bool, document string) (int, helperschema.Response) {
	t.Helper()
	var out, log bytes.Buffer
	code := serve(context.Background(), h, k, proven, nil, 3, strings.NewReader(document), &out, &log)
	var response helperschema.Response
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &response); err != nil {
			t.Fatalf("the answer is not one document: %q", out.String())
		}
	}
	return code, response
}

func TestHelperAdmission_RefusesEveryMutationWithoutPeerPidfd(t *testing.T) {
	performed := map[string]int{}
	mutations := 0
	for _, h := range helpers(performed) {
		for _, rule := range h.Rules {
			if !rule.Mutating {
				continue
			}
			mutations++
			t.Run(h.Name+" "+rule.Subcommand, func(t *testing.T) {
				// The repair console's own process, as far as uid and pid can tell: only the
				// pidfd is missing.
				k := console()
				k.noPidfd = true
				peer, err := Attest(k, 3)
				if err != nil {
					t.Fatal(err)
				}
				if peer.Attested || peer.Unit != "" {
					t.Fatalf("a connection without SO_PEERPIDFD was attested as %+v", peer)
				}
				if k.cgroupReads != 0 {
					t.Fatalf("without a pidfd the helper read a cgroup by pid %d times: a pid names no process", k.cgroupReads)
				}
				var refusal *helperschema.Refusal
				err = helperschema.Admit(peer, h.Rules, rule.Subcommand)
				if !errors.As(err, &refusal) || refusal.Code != helperschema.CodeNoConnectionIdentity {
					t.Fatalf("%s %s without SO_PEERPIDFD: %v, want %s", h.Name, rule.Subcommand, err, helperschema.CodeNoConnectionIdentity)
				}
				code, response := serveOne(t, h, k, documents[rule.Subcommand])
				if code != ExitRefused || response.Result != helperschema.ResultRefused || response.Code != helperschema.CodeNoConnectionIdentity {
					t.Fatalf("served: exit %d, %+v", code, response)
				}
			})
		}
	}
	if mutations < 3 {
		t.Fatalf("the census found %d mutating subcommands, not the seam's", mutations)
	}
	for act, n := range performed {
		t.Errorf("%s was performed %d times without a proven connection identity", act, n)
	}

	t.Run("control: the same process with a pidfd is admitted for power", func(t *testing.T) {
		counted := map[string]int{}
		power := helpers(counted)[0]
		code, response := serveOne(t, power, console(), documents[helperschema.PowerReboot])
		if code != ExitDone || response.Result != helperschema.ResultPerformed || counted["power reboot"] != 1 {
			t.Fatalf("the repair console with a pidfd: exit %d, %+v, performed %v", code, response, counted)
		}
	})
}

func TestHelperAdmission_AttestsTheUnitFromTheKernelNotFromTheCaller(t *testing.T) {
	performed := map[string]int{}
	power := helpers(performed)[0]
	reboot := documents[helperschema.PowerReboot]

	t.Run("the kernel's unit decides", func(t *testing.T) {
		k := console()
		k.cgroup = "0::/system.slice/olivares-other.service\n"
		code, response := serveOne(t, power, k, reboot)
		if code != ExitRefused || response.Code != helperschema.CodeNotAdmitted {
			t.Fatalf("a root process in another unit: exit %d, %+v", code, response)
		}
		if code, response := serveOne(t, power, console(), reboot); code != ExitDone || response.Result != helperschema.ResultPerformed {
			t.Fatalf("control: the repair console's unit: exit %d, %+v", code, response)
		}
	})

	t.Run("a document cannot name the unit, the mode or the invoker", func(t *testing.T) {
		k := console()
		k.cgroup = "0::/system.slice/olivares-other.service\n"
		for _, document := range []string{
			`{"verb": "reboot", "operation_id": "` + operationID + `", "unit": "olivares-repair-console.service"}`,
			`{"verb": "reboot", "operation_id": "` + operationID + `", "mode": "tty1"}`,
			`{"verb": "reboot", "operation_id": "` + operationID + `", "invoker": "the repair console on tty1"}`,
			`{"verb": "reboot", "operation_id": "` + operationID + `", "uid": "0"}`,
			`{"verb": "reboot", "operation_id": "` + operationID + `", "tty": "/dev/tty1"}`,
		} {
			code, response := serveOne(t, power, k, document)
			if code != ExitRefused || response.Code != helperschema.CodeInputRefused {
				t.Errorf("%s: exit %d, %+v", document, code, response)
			}
		}
	})

	t.Run("the environment and the command line are not the invoker", func(t *testing.T) {
		t.Setenv("INVOCATION_ID", "0123456789abcdef0123456789abcdef")
		t.Setenv("OLIVARES_INVOKER", "olivares-repair-console.service")
		t.Setenv("SYSTEMD_UNIT", "olivares-repair-console.service")
		k := console()
		k.cgroup = "0::/system.slice/olivares-other.service\n"
		if code, response := serveOne(t, power, k, reboot); code != ExitRefused || response.Code != helperschema.CodeNotAdmitted {
			t.Fatalf("an environment naming the console admitted another unit: exit %d, %+v", code, response)
		}
		var out, log bytes.Buffer
		if code := Serve(context.Background(), power, console(), []string{"--unit=olivares-repair-console.service"}, 3,
			strings.NewReader(reboot), &out, &log); code != ExitFailure || out.Len() != 0 {
			t.Fatalf("an argument was accepted: exit %d, %q", code, out.String())
		}
	})

	for _, tc := range []struct {
		name  string
		shape func(k *fakeKernel)
	}{
		{"a cgroup nested under the console's", func(k *fakeKernel) {
			k.cgroup = "0::/system.slice/olivares-repair-console.service/payload\n"
		}},
		{"a user slice process named like the console", func(k *fakeKernel) {
			k.cgroup = "0::/user.slice/user-0.slice/olivares-repair-console.service\n"
		}},
		{"a cgroup v1 hierarchy", func(k *fakeKernel) {
			k.cgroup = "12:pids:/system.slice/olivares-repair-console.service\n0::/system.slice/olivares-repair-console.service\n"
		}},
		{"another uid in the console's unit", func(k *fakeKernel) { k.uid = 1000 }},
		{"a pidfd naming another pid", func(k *fakeKernel) { k.pidfdPid = 4243 }},
		{"a process that exited before its cgroup was confirmed", func(k *fakeKernel) { k.exited = true }},
	} {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			k := console()
			tc.shape(k)
			if code, response := serveOne(t, power, k, reboot); code != ExitRefused || response.Result != helperschema.ResultRefused {
				t.Fatalf("exit %d, %+v", code, response)
			}
			if k.closed != 1 {
				t.Fatalf("the pidfd was closed %d times, want once", k.closed)
			}
		})
	}

	if performed["power reboot"] != 1 || len(performed) != 1 {
		t.Fatalf("performed %v, want power reboot exactly once: the control", performed)
	}
}
