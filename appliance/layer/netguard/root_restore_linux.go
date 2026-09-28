// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && (amd64 || arm64)

package netguard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

const guardJournalDir = "/var/lib/olivares-net-guard/acts"
const restoreLedgerDir = "/run/olivares-netrestore"

type RestoreRequest struct {
	ConnectionUUID string `json:"connection_uuid"`
	OperationID    string `json:"operation_id"`
}

func (r RestoreRequest) Subcommand() string { return "restore" }
func (r RestoreRequest) Operation() string  { return r.OperationID }
func (r RestoreRequest) Validate() error {
	if !validID(r.OperationID) || !uuidShape(r.ConnectionUUID) {
		return errors.New("network_restore_input_refused")
	}
	return nil
}
func uuidShape(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
func guardAccount() (uint32, uint32, error) {
	u, err := user.Lookup("olivares-net-guard")
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, 0, err
	}
	gid, err := staticGroup("olivares-net-guard")
	return uint32(uid), gid, err
}
func readRestoreWindow(request RestoreRequest) (Window, uint32, error) {
	var w Window
	if err := request.Validate(); err != nil {
		return w, 0, err
	}
	uid, gid, err := guardAccount()
	if err != nil {
		return w, 0, err
	}
	if err := protectedJSON(filepath.Join(guardJournalDir, request.OperationID+".json"), uid, gid, 0600, MaxWindowBytes, &w); err != nil {
		return w, 0, err
	}
	if w.Schema != JournalSchema || w.OperationID != request.OperationID || w.Baseline.UUID != request.ConnectionUUID || w.State != StateRestoring || !w.HelperStarted || w.Attempt == 0 || w.Pending() || len(w.Calls) > MaxCallsPerWindow-2 {
		return w, 0, errors.New("network_restore_window_refused")
	}
	return w, gid, nil
}

type helperBeginning struct {
	OperationID, Generation string
	Attempt                 uint32
	Target                  TargetBinding
	Helper                  ProcessIdentity
}
type helperFinished struct{ Finished bool }

func ledgerDirectory(w Window) string {
	return filepath.Join(restoreLedgerDir, w.OperationID, fmt.Sprintf("%010d", w.Attempt))
}
func newRootLedger(w Window, gid uint32) (rootLedger, error) {
	l := rootLedger{dir: ledgerDirectory(w), uid: 0, gid: gid}
	for _, name := range []string{restoreLedgerDir, filepath.Join(restoreLedgerDir, w.OperationID), l.dir} {
		if err := ownedDirectory(name, gid, 0750); err != nil {
			return l, err
		}
	}
	start, err := procStart(os.Getpid())
	if err != nil {
		return l, err
	}
	clock, err := NewBootClock()
	if err != nil {
		return l, err
	}
	beginning := helperBeginning{OperationID: w.OperationID, Generation: w.Generation, Attempt: w.Attempt, Target: w.RecoveryTarget, Helper: ProcessIdentity{BootID: clock.boot, PID: os.Getpid(), StartTime: start}}
	if err := l.write("begin.json", beginning); err != nil {
		return l, err
	}
	return l, nil
}
func validateKeyfile(name, uuid string) error {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != 0 || st.Nlink != 1 || info.Size() > 65536 {
		return errors.New("network_keyfile_custody_refused")
	}
	scan := bufio.NewScanner(io.LimitReader(f, 65537))
	section := ""
	found := false
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line
			continue
		}
		if section != "[connection]" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == "uuid" {
			if found || strings.TrimSpace(value) != uuid {
				return errors.New("network_keyfile_identity_refused")
			}
			found = true
		}
	}
	if err := scan.Err(); err != nil {
		return errors.New("network_keyfile_unreadable")
	}
	if !found {
		return errors.New("network_keyfile_identity_refused")
	}
	return nil
}
func (t *NMTransport) findProfile(ctx context.Context, uuid string) (dbus.ObjectPath, string, bool, error) {
	body, err := t.read(ctx, t.process.identity.Unique, "/org/freedesktop/NetworkManager/Settings", nmName+".Settings", "ListConnections")
	if err != nil {
		return "", "", false, err
	}
	var paths []dbus.ObjectPath
	if err := storeReply(body, &paths); err != nil {
		return "", "", false, err
	}
	found := false
	var result dbus.ObjectPath
	filename := ""
	for _, path := range paths {
		body, err := t.read(ctx, t.process.identity.Unique, path, ProfileInterface, "GetSettings")
		if err != nil {
			return "", "", false, err
		}
		var values settingsMap
		if err := storeReply(body, &values); err != nil {
			return "", "", false, err
		}
		id, _ := values["connection"]["uuid"].Value().(string)
		if id != uuid {
			continue
		}
		if found {
			return "", "", false, errors.New("network_profile_identity_conflict")
		}
		v, err := t.get(ctx, path, ProfileInterface, "Filename")
		if err != nil {
			return "", "", false, err
		}
		var ok bool
		filename, ok = v.Value().(string)
		if !ok {
			return "", "", false, errors.New("network_profile_storage_unavailable")
		}
		result = path
		found = true
	}
	return result, filename, found, nil
}
func (t *NMTransport) verifyRootTarget(ctx context.Context, binding TargetBinding) error {
	if os.Geteuid() != 0 || t.busID != binding.BusID || t.process.identity != binding.Process {
		return errors.New("network_restore_target_changed")
	}
	if err := t.verifyTarget(ctx); err != nil {
		return err
	}
	body, err := t.read(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "GetConnectionCredentials", binding.Process.Unique)
	if err != nil || len(body) != 1 {
		return errors.New("network_process_identity_unavailable")
	}
	credentials, ok := body[0].(map[string]dbus.Variant)
	if !ok {
		return errors.New("network_process_identity_unavailable")
	}
	fd, ok := credentials["ProcessFD"].Value().(dbus.UnixFD)
	if !ok {
		return errors.New("network_process_identity_unavailable")
	}
	current, err := credentialProcess(int(fd), binding.Process.Unique, t.clock.boot)
	if err != nil {
		return err
	}
	defer closeProcess(current)
	if current.identity != binding.Process {
		return errors.New("network_restore_target_changed")
	}
	return nil
}
func (t *NMTransport) rootCall(ctx context.Context, w Window, l rootLedger, method Method, baseline string) error {
	if err := t.verifyRootTarget(ctx, w.RecoveryTarget); err != nil {
		return err
	}
	member := "ReloadConnections"
	var args []any
	if method == LoadConnections {
		if !persistentFilename(baseline) || validateKeyfile(baseline, w.Baseline.UUID) != nil {
			return errors.New("network_persistent_baseline_refused")
		}
		member = "LoadConnections"
		args = []any{[]string{baseline}}
	} else if method != ReloadConnections {
		return errors.New("network_restore_method_refused")
	}
	var intent CallRecord
	ch, err := t.dispatch(w.RecoveryTarget.Process.Unique, "/org/freedesktop/NetworkManager/Settings", nmName+".Settings", member, args, func(c CallRecord) error {
		_, now, err := t.clock.Now()
		if err != nil {
			return err
		}
		c.OperationID = w.OperationID
		c.WindowGeneration = w.Generation
		c.Attempt = w.Attempt
		c.SentAt = now
		intent = c
		return l.intent(c)
	}, method)
	if err != nil {
		return err
	}
	timer := time.NewTimer(CallNotificationAfter)
	defer timer.Stop()
	select {
	case reply := <-ch:
		if !intent.SettledBy(reply, DeathUnknown) {
			return errors.New("network_restore_call_pending")
		}
		intent.Settled = true
		intent.Success = reply.Success
		intent.Settlement = "correlated_reply"
		intent.EffectBootID, intent.EffectKnownAt, err = t.clock.Now()
		if err != nil {
			return err
		}
		if err := l.complete(intent); err != nil {
			return err
		}
		if !reply.Success {
			return errors.New("network_restore_call_failed")
		}
		var success bool
		if method == ReloadConnections {
			if err := storeReply(reply.Body, &success); err != nil || !success {
				return errors.New("network_reload_failed")
			}
		} else {
			var failed []string
			if err := storeReply(reply.Body, &success, &failed); err != nil || !success || len(failed) > 0 {
				return errors.New("network_load_failed")
			}
		}
		return nil
	case <-timer.C:
		return errors.New("network_restore_call_pending")
	}
}

// RunRootRestore is the narrow root library. The helper admits its kernel peer before
// calling it. Guest drivers may exercise this library only as an explicitly root fixture.
func RunRootRestore(ctx context.Context, request RestoreRequest) error {
	if os.Geteuid() != 0 {
		return errors.New("network_restore_requires_root")
	}
	w, gid, err := readRestoreWindow(request)
	if err != nil {
		return err
	}
	if !persistentFilename(w.Baseline.Filename) || validateKeyfile(w.Baseline.Filename, w.Baseline.UUID) != nil {
		return errors.New("network_persistent_baseline_refused")
	}
	bus, err := NewSystemTransport(nil)
	if err != nil {
		return err
	}
	defer bus.Close()
	if err := bus.verifyRootTarget(ctx, w.RecoveryTarget); err != nil {
		return err
	}
	ledger, err := newRootLedger(w, gid)
	if err != nil {
		return err
	}
	// The finish marker means this invocation will send no further calls. It does not
	// settle any intent; a missing completion remains pending even after this marker.
	defer ledger.write("finished.json", helperFinished{Finished: true})
	_, current, _, err := bus.findProfile(ctx, w.Baseline.UUID)
	if err != nil {
		return err
	}
	remove, err := RestoreFiles(w.Baseline.Filename, current)
	if err != nil {
		return err
	}
	if remove != "" {
		if err := validateKeyfile(remove, w.Baseline.UUID); err != nil {
			return err
		}
		if err := os.Remove(remove); err != nil {
			return err
		}
		dir, err := os.Open(filepath.Dir(remove))
		if err != nil {
			return err
		}
		err = dir.Sync()
		dir.Close()
		if err != nil {
			return err
		}
	}
	if err := bus.rootCall(ctx, w, ledger, ReloadConnections, ""); err != nil {
		return err
	}
	_, _, found, err := bus.findProfile(ctx, w.Baseline.UUID)
	if err != nil {
		return err
	}
	if !found {
		if err := bus.rootCall(ctx, w, ledger, LoadConnections, w.Baseline.Filename); err != nil {
			return err
		}
	}
	return nil
}

// ReadRestoreReport reads all root attempts. Missing intent is safe only after an
// authenticated finish marker or proof that the recorded helper process is gone.
func ReadRestoreReport(w Window) (RestoreReport, error) {
	_, gid, err := guardAccount()
	if err != nil {
		return RestoreReport{}, err
	}
	clock, err := NewBootClock()
	if err != nil {
		return RestoreReport{}, err
	}
	report := RestoreReport{Finished: true, MissingIntentSafe: true}
	for _, attemptRecord := range w.RestoreAttempts {
		attempt := attemptRecord.Attempt
		copy := w
		copy.Attempt = attempt
		l := rootLedger{dir: ledgerDirectory(copy), uid: 0, gid: gid}
		var beginning helperBeginning
		if err := l.read("begin.json", &beginning); err != nil {
			if errors.Is(err, os.ErrNotExist) && attemptRecord.Target.Process.BootID != "" && attemptRecord.Target.Process.BootID != clock.boot {
				continue
			}
			return RestoreReport{}, errors.New("network_restore_invocation_unknown")
		}
		if beginning.Target != attemptRecord.Target {
			return RestoreReport{}, errors.New("network_restore_target_changed")
		}

		if beginning.OperationID != w.OperationID || beginning.Generation != w.Generation || beginning.Attempt != attempt {
			return RestoreReport{}, errors.New("network_restore_ledger_conflict")
		}
		calls, err := l.calls()
		if err != nil {
			return RestoreReport{}, err
		}
		report.Calls = append(report.Calls, calls...)
		if len(report.Calls) > MaxCallsPerWindow {
			return RestoreReport{}, ErrJournalFull
		}
		var finished helperFinished
		ended := l.read("finished.json", &finished) == nil && finished.Finished
		if !ended && originalDeath(beginning.Helper, nil, clock.boot) != DeathProven {
			report.Finished = false
			report.MissingIntentSafe = false
		}
	}
	return report, nil
}
