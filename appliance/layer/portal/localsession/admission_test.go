// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localsession

import (
	"errors"
	"testing"
)

type facts struct {
	uid                    uint32
	admin, passcred, alive bool
	pidfdErr               bool
	closed                 int
	groupReads             int
	cgroup                 string
}

func (f *facts) PeerCred(int) (int, uint32, error) { return 42, f.uid, nil }
func (f *facts) PeerPidfd(int) (int, error) {
	if f.pidfdErr {
		return 0, errors.New("unavailable")
	}
	return 9, nil
}
func (f *facts) PidOf(int) (int, error)     { return 42, nil }
func (f *facts) Cgroup(int) (string, error) { return f.cgroup, nil }
func (f *facts) Alive(int) error {
	if f.alive {
		return nil
	}
	return errors.New("exited")
}
func (f *facts) Close(int)                  { f.closed++ }
func (f *facts) PassCred(int) (bool, error) { return f.passcred, nil }
func (f *facts) Admin(uint32) (bool, error) { f.groupReads++; return f.admin, nil }
func (f *facts) Unread(int) (int, error)    { return 0, nil }
func admittedFacts(uid uint32, admin bool) *facts {
	return &facts{uid: uid, admin: admin, passcred: true, alive: true, cgroup: "0::/user.slice/session-3.scope\n"}
}

func TestLocalAPI_AdmitsOnlyOlivaresAdminsAndRootWithoutASocketGroup(t *testing.T) {
	for _, tc := range []struct {
		name           string
		uid            uint32
		admin, allowed bool
	}{
		{"root", 0, false, true}, {"administrator", 1000, true, true}, {"ordinary", 1001, false, false}, {"portal", 900, false, false}, {"guard", 901, false, false}, {"support", 902, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := admittedFacts(tc.uid, tc.admin)
			owner, code := admit(4, f)
			if (code == "") != tc.allowed {
				t.Fatalf("code=%s allowed=%v", code, tc.allowed)
			}
			if tc.allowed {
				if owner.uid != tc.uid || f.closed != 0 {
					t.Fatal("peer is not held")
				}
				owner.Close()
				if f.closed != 1 {
					t.Fatal("pidfd leaked")
				}
			}
		})
	}
	f := admittedFacts(1000, true)
	owner, code := admit(4, f)
	if code != "" {
		t.Fatal(code)
	}
	defer owner.Close()
	f.admin = false
	if owner.Check() == nil || f.groupReads != 2 {
		t.Fatal("removed group membership retained")
	}
}
func TestLocalPeer_NoHeldPidfdGivesRefusedPidfdUnprovenAndNoSession(t *testing.T) {
	f := admittedFacts(0, false)
	f.pidfdErr = true
	owner, code := admit(4, f)
	if owner != nil || code != "pidfd_unproven" {
		t.Fatalf("owner=%v code=%s", owner, code)
	}
	f = admittedFacts(0, false)
	f.passcred = false
	if owner, code := admit(4, f); owner != nil || code != "passcred_unset" {
		t.Fatalf("owner=%v code=%s", owner, code)
	}
	f = admittedFacts(0, false)
	f.cgroup = "unverifiable"
	if owner, code := admit(4, f); owner != nil || code != "pidfd_unproven" || f.closed != 1 {
		t.Fatalf("owner=%v code=%s closed=%d", owner, code, f.closed)
	}
}
