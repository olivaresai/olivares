// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package repair

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/netguard"
)

// ofdSetLock is F_OFD_SETLK: a lock owned by the open file description, so a lock this process
// takes on one descriptor conflicts with a query on another, as a package manager's would.
const ofdSetLock = 37

func TestRecoveryPower_PackageStorageAndLifecycleGatesRemain(t *testing.T) {
	g := &guard{boot: bootID, windows: []netguard.Status{pendingStatus()}}
	plan := planFor(t, g)

	type host struct {
		lock, phase, records, lifecycle string
		jobs                            int
		jobsErr                         error
	}
	newHost := func(t *testing.T) *host {
		t.Helper()
		dir := t.TempDir()
		h := &host{lock: filepath.Join(dir, ".rpm.lock"), phase: filepath.Join(dir, "package-transaction.json")}
		if err := os.WriteFile(h.lock, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		_, h.records, h.lifecycle = newRecords(t)
		return h
	}
	gated := func(h *host, s LinkageStore) Admission {
		a := admission(g, s,
			RecordsGate{Dir: h.records},
			PackageGate{Locks: []string{filepath.Join(filepath.Dir(h.lock), "absent.lock"), h.lock}, Phase: h.phase},
			StorageGate{Jobs: func(context.Context) (int, error) { return h.jobs, h.jobsErr }},
		)
		a.Serializer = HostHold{LifecycleDir: h.lifecycle, RecordsDir: h.records}
		return a
	}

	t.Run("control: an idle backend, no unresolved phase, no storage job and no transition admit it", func(t *testing.T) {
		h, s, e := newHost(t), store(t), &effect{}
		if err := gated(h, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run); err != nil || e.runs != 1 {
			t.Fatalf("an idle host refused the recovery reboot: %v", err)
		}
	})

	t.Run("refused: the package backend is in a transaction, which the network exemption does not cover", func(t *testing.T) {
		h, s, e := newHost(t), store(t), &effect{}
		holder, err := os.OpenFile(h.lock, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer holder.Close()
		lock := syscall.Flock_t{Type: syscall.F_WRLCK}
		if err := syscall.FcntlFlock(holder.Fd(), ofdSetLock, &lock); err != nil {
			t.Fatalf("control: the backend's lock could not be taken: %v", err)
		}
		refusedBy(t, gated(h, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run), "package backend", e, s)
		refusedBy(t, gated(h, s).Ordinary(context.Background(), signedIn, e.run), "", e, s)
	})

	t.Run("refused: a package phase its owner has not resolved", func(t *testing.T) {
		h, s, e := newHost(t), store(t), &effect{}
		if err := os.WriteFile(h.phase, []byte(`{"phase": "unpacked"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		refusedBy(t, gated(h, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run), "package backend", e, s)
	})

	t.Run("refused: no backend lock can be read, so the backend is not proven idle", func(t *testing.T) {
		h, s, e := newHost(t), store(t), &effect{}
		if err := os.Remove(h.lock); err != nil {
			t.Fatal(err)
		}
		refusedBy(t, gated(h, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run), "package backend", e, s)
	})

	t.Run("refused: a storage job runs, or storage cannot be read", func(t *testing.T) {
		for name, set := range map[string]func(*host){
			"a job":      func(h *host) { h.jobs = 1 },
			"unreadable": func(h *host) { h.jobsErr = errors.New("udisks does not answer") },
		} {
			h, s, e := newHost(t), store(t), &effect{}
			set(h)
			err := gated(h, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run)
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Gate != "storage" || e.runs != 0 {
				t.Errorf("%s: %v, ran %d times", name, err, e.runs)
			}
		}
	})

	t.Run("refused: a lifecycle transition", func(t *testing.T) {
		h, s, e := newHost(t), store(t), &effect{}
		transition, err := hostops.OpenLifecycle(h.lifecycle, hostops.RoleInitializer)
		if err != nil {
			t.Fatal(err)
		}
		defer transition.Close()
		if err := transition.Exclusive(); err != nil {
			t.Fatal(err)
		}
		refusedBy(t, gated(h, s).Recover(context.Background(), plan, plan.Digest(), signedIn, e.run), "serialization", e, s)
	})

	t.Run("refused: a gate the operator cannot see still refuses", func(t *testing.T) {
		h, s, e := newHost(t), store(t), &effect{}
		a := gated(h, s)
		a.Gates = append(a.Gates, failing{name: "another owner"})
		refusedBy(t, a.Recover(context.Background(), plan, plan.Digest(), signedIn, e.run), "another owner", e, s)
	})
}
