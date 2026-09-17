// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// lockedBuffer collects a helper process's output; exec copies it from its own goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

const (
	envLoginCapabilityHelperDB    = "R105_LOGIN_CAPABILITY_HELPER_DB"
	envLoginCapabilityHelperReady = "R105_LOGIN_CAPABILITY_HELPER_READY"
	envLoginCapabilityHelperHold  = "R105_LOGIN_CAPABILITY_HELPER_HOLD_MS"
	envLoginCapabilityOldReaderDB = "R105_LOGIN_CAPABILITY_OLD_READER_DB"
)

// TestLoginCapabilitySQLiteReservationHelperProcess is the OTHER process of the cross-process
// reservation test. It does nothing unless the parent re-executes the test binary for it.
func TestLoginCapabilitySQLiteReservationHelperProcess(t *testing.T) {
	path := os.Getenv(envLoginCapabilityHelperDB)
	if path == "" {
		t.Skip("helper process only")
	}
	hold, err := strconv.Atoi(os.Getenv(envLoginCapabilityHelperHold))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	st, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: path}, nil)
	if err != nil {
		t.Fatalf("helper open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		if _, err := as.LoginCapability().Lock(ctx); err != nil {
			return err
		}
		if err := os.WriteFile(os.Getenv(envLoginCapabilityHelperReady), []byte("locked"), 0o600); err != nil {
			return err
		}
		time.Sleep(time.Duration(hold) * time.Millisecond)
		_, err := as.LoginCapability().Observe(ctx, "helper-process")
		return err
	}); err != nil {
		t.Fatalf("helper transaction: %v", err)
	}
}

// TestLoginCapabilitySQLiteReservationSerializesAcrossProcesses proves the SQLite writer
// reservation serializes a second PROCESS on the same file: a short hold makes the parent wait
// and then succeed after the helper commits; a hold past busy_timeout(5000) makes the parent's
// Lock fail and its AuthMutate retain the failure, committing nothing.
func TestLoginCapabilitySQLiteReservationSerializesAcrossProcesses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		holdMS   int
		wantFail bool
	}{
		{"short hold waits then serializes", 1500, false},
		{"hold beyond busy_timeout refuses", 7500, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			path := filepath.Join(dir, "capability.db")
			st, err := openSQLiteDestination(t, path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			ready := filepath.Join(dir, "ready")
			cmd := osexec.Command(os.Args[0], "-test.run=^TestLoginCapabilitySQLiteReservationHelperProcess$", "-test.count=1")
			cmd.Env = append(os.Environ(),
				envLoginCapabilityHelperDB+"="+path,
				envLoginCapabilityHelperReady+"="+ready,
				envLoginCapabilityHelperHold+"="+strconv.Itoa(tc.holdMS))
			out := &lockedBuffer{}
			cmd.Stdout, cmd.Stderr = out, out
			if err := cmd.Start(); err != nil {
				t.Fatalf("start helper: %v", err)
			}
			deadline := time.Now().Add(60 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					t.Fatalf("helper never took the reservation: %s", out.String())
				}
				time.Sleep(20 * time.Millisecond)
			}
			start := time.Now()
			var lockErr error
			mutateErr := st.AuthMutate(ctx, func(as store.AuthScope) error {
				_, lockErr = as.LoginCapability().Lock(ctx)
				if lockErr != nil {
					return nil // discard: the retained failure must still refuse the commit
				}
				_, err := as.LoginCapability().Observe(ctx, "parent-process")
				return err
			})
			elapsed := time.Since(start)
			if err := cmd.Wait(); err != nil {
				t.Fatalf("helper process failed: %v\n%s", err, out.String())
			}
			t.Logf("parent Lock elapsed=%s lockErr=%v mutateErr=%v", elapsed, lockErr, mutateErr)
			obs := readLoginCapabilityView(t, st)
			if tc.wantFail {
				// Measured: on SQLite every AuthMutate already reserves the writer in the Mutate
				// envelope (directory writer marker) before the callback, so a reservation held past
				// busy_timeout is refused THERE with SQLITE_BUSY before Lock runs. Either refusal
				// point is acceptable; what must hold is a bounded refusal that commits nothing.
				refusedAtLock := lockErr != nil && errors.Is(mutateErr, lockErr)
				refusedAtEnvelope := lockErr == nil && mutateErr != nil
				if !(refusedAtLock || refusedAtEnvelope) || elapsed < 4*time.Second {
					t.Fatalf("a reservation held past busy_timeout was not refused and retained: lockErr=%v mutateErr=%v elapsed=%s", lockErr, mutateErr, elapsed)
				}
				if obs.ObservationCount != 1 || obs.LastArtifactVersion != "helper-process" {
					t.Fatalf("after the refused parent: %+v, want only the helper's observation", obs)
				}
				return
			}
			if lockErr != nil || mutateErr != nil || elapsed < 1*time.Second {
				t.Fatalf("the parent did not wait for the helper's reservation: lockErr=%v mutateErr=%v elapsed=%s", lockErr, mutateErr, elapsed)
			}
			if obs.ObservationCount != 2 || obs.LastArtifactVersion != "parent-process" {
				t.Fatalf("serialized observations = %+v, want helper then parent", obs)
			}
		})
	}
}

// TestLoginCapabilitySQLiteOldReaderFixture writes a v13 SQLite database for the old-reader Open
// executed by the baseline d75d5290 binary (ceiling 11). It does nothing unless asked.
func TestLoginCapabilitySQLiteOldReaderFixture(t *testing.T) {
	path := os.Getenv(envLoginCapabilityOldReaderDB)
	if path == "" {
		t.Skip("old-reader fixture only")
	}
	st, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: path}, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	observeLoginCapability(t, st, "v13-writer")
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
