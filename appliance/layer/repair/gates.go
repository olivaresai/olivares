// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package repair

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/netguard"
)

// The retained gates' names, as a refusal states them.
const (
	gateWindow     = "pending network window"
	gateOperations = "host operations"
	gatePackage    = "package backend"
	gateStorage    = "storage"
)

// statusFacts are the facts of a window's status that identify it and its pending effect. The
// deadline and the observation times move while the operator reads, so they are not digested.
type statusFacts struct {
	OperationID      string `json:"operation_id"`
	State            string `json:"state"`
	Reason           string `json:"reason"`
	BootID           string `json:"boot_id"`
	WindowGeneration string `json:"window_generation"`
	RecoveryAttempt  uint32 `json:"recovery_attempt"`
	PendingCalls     int    `json:"pending_calls"`
	FinalBootID      string `json:"final_boot_id"`
	FinalKnownAt     int64  `json:"final_known_boottime_ns"`
}

// StatusDigest is the SHA-256, in lowercase hexadecimal, of the canonical JSON of a window's
// identifying status facts.
func StatusDigest(s netguard.Status) string {
	data, _ := json.Marshal(statusFacts{OperationID: s.OperationID, State: string(s.State), Reason: s.Reason, BootID: s.BootID,
		WindowGeneration: s.WindowGeneration, RecoveryAttempt: s.RecoveryAttempt, PendingCalls: s.PendingCalls,
		FinalBootID: s.FinalBootID, FinalKnownAt: int64(s.FinalKnownAt)})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// effectPending reports whether a window is the one this recovery exists for: not terminal, in this
// boot, with a mutating call whose reply the guard has not settled.
func effectPending(s netguard.Status, boot string) bool {
	return s.State != netguard.StateConfirmed && s.State != netguard.StateRolledBack && s.BootID == boot && s.PendingCalls > 0
}

// PendingFromStatus identifies the one pending window a guard's status answer shows in boot: the
// answer must be ok and of this boot, and hold exactly one open window, whose effect is pending.
func PendingFromStatus(response netguard.EdgeResponse, boot string) (PendingNetwork, error) {
	switch {
	case response.Code != "ok":
		return PendingNetwork{}, refuse(gateWindow, "the network guard's status is not an admitted answer")
	case !bootIDShape(boot) || response.BootID != boot:
		return PendingNetwork{}, refuse(gateWindow, "the network guard's status is not of this boot")
	case len(response.OpenWindows) != 1:
		return PendingNetwork{}, refuse(gateWindow, "the network guard does not show exactly one open window")
	}
	window := response.OpenWindows[0]
	if !effectPending(window, boot) {
		return PendingNetwork{}, refuse(gateWindow, "the open window is not a pending network effect of this boot")
	}
	pending := PendingNetwork{OperationID: window.OperationID, BootID: window.BootID, WindowGeneration: window.WindowGeneration,
		StatusDigest: StatusDigest(window)}
	if pending.Validate() != nil {
		return PendingNetwork{}, refuse(gateWindow, "the open window is outside the guard's types")
	}
	return pending, nil
}

// WindowGate reads the guard's status inside the hold. With no exempt window it refuses while any
// network window is open, as a running operation. With the exempt window it admits only when the
// status, of this boot, shows exactly that window, still pending, with the same facts the operator
// confirmed; another window, a stale boot or generation, a terminal result or an unreadable status
// refuses.
type WindowGate struct {
	Status func(context.Context) (netguard.EdgeResponse, error)
	Boot   func() (string, error)
}

// Name implements Gate.
func (WindowGate) Name() string { return gateWindow }

// Check implements Gate.
func (g WindowGate) Check(ctx context.Context, exempt PendingNetwork) error {
	if g.Status == nil || g.Boot == nil {
		return refuse(gateWindow, "the network guard's status is not composed")
	}
	boot, err := g.Boot()
	if err != nil {
		return refuse(gateWindow, "this boot's identity cannot be read")
	}
	response, err := g.Status(ctx)
	if err != nil || response.Code != "ok" {
		return refuse(gateWindow, "the network guard's status is missing or unreadable")
	}
	if response.BootID != boot {
		return refuse(gateWindow, "the network guard's status is not of this boot")
	}
	if exempt == (PendingNetwork{}) {
		if len(response.OpenWindows) != 0 {
			return refuse(gateWindow, "a network change window is open, operation "+safeID(response.OpenWindows[0].OperationID)+
				"; if its reply was lost, recovery-reboot is the one route that names it")
		}
		return nil
	}
	pending, err := PendingFromStatus(response, boot)
	if err != nil {
		return err
	}
	if pending != exempt {
		return refuse(gateWindow, "the pending window is not the one the operator confirmed")
	}
	return nil
}

// safeID is a guard operation id as the guard states it, or a placeholder for anything else.
func safeID(id string) string {
	if lowerHex(id, 32) {
		return id
	}
	return "(unreadable)"
}

// storeLock is the operation engine's store lock in its records directory, the lock every
// admission of a host operation holds while it claims its record.
const storeLock = "store.lock"

// maxRecordBytes bounds an operation record read here.
const maxRecordBytes = 64 * 1024

// RecordsGate refuses while the host operation engine records any operation that is not finished,
// other than the one exempt network operation: a running operation's outcome is unknown, and a
// planned one may start. An unreadable record refuses: absence of a finished state is never read as
// idle. It is read inside HostHold, which holds the engine's own store lock.
type RecordsGate struct {
	Dir string
}

// Name implements Gate.
func (RecordsGate) Name() string { return gateOperations }

// Check implements Gate.
func (g RecordsGate) Check(_ context.Context, exempt PendingNetwork) error {
	entries, err := os.ReadDir(g.Dir)
	if err != nil {
		return refuse(gateOperations, "the host operation records cannot be read")
	}
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !lowerHex(id, 32) {
			continue
		}
		record, err := readRecord(filepath.Join(g.Dir, entry.Name()))
		if err != nil || record.OperationID != id {
			return refuse(gateOperations, "a host operation record is unreadable")
		}
		switch record.State {
		case hostops.StateSucceeded, hostops.StateFailed, hostops.StateRolledBack, hostops.StatePartial:
			continue
		case hostops.StatePlanned, hostops.StateRunning:
		default:
			return refuse(gateOperations, "a host operation record is unreadable")
		}
		if exempt != (PendingNetwork{}) && record.OperationID == exempt.OperationID && record.Target == "network" {
			continue
		}
		return refuse(gateOperations, "the host operation "+id+" on "+safeTarget(record.Target)+" is not finished")
	}
	return nil
}

// operationRecord is the part of an engine record the gate reads.
type operationRecord struct {
	OperationID string `json:"operation_id"`
	Target      string `json:"target"`
	State       string `json:"state"`
}

// readRecord reads one engine record: a regular file, never a link, holding one document.
func readRecord(path string) (operationRecord, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return operationRecord{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxRecordBytes {
		return operationRecord{}, errors.New("not a record")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	if err != nil {
		return operationRecord{}, err
	}
	var stored struct {
		Record *operationRecord `json:"record"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&stored); err != nil || stored.Record == nil {
		return operationRecord{}, errors.New("not a record")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return operationRecord{}, errors.New("not a record")
	}
	return *stored.Record, nil
}

// safeTarget is an engine target in its closed grammar's alphabet, or a placeholder.
func safeTarget(target string) string {
	for i := 0; i < len(target); i++ {
		c := target[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && !strings.ContainsRune(":.-_@", rune(c)) {
			return "(unreadable)"
		}
	}
	if target == "" || len(target) > 300 {
		return "(unreadable)"
	}
	return target
}

// PackageLocks are the package backends' transaction locks: rpm's on the Fedora image, dpkg's two
// on a Debian host. A backend holds its lock for the whole of a transaction.
var PackageLocks = []string{"/usr/lib/sysimage/rpm/.rpm.lock", "/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock"}

// PackagePhaseRecord is the package-phase owner's record of a transaction it has not resolved.
const PackagePhaseRecord = "/var/lib/olivares-exposure/package-transaction.json"

// PackageGate refuses while a package backend is in a transaction, whoever started it, managed or
// not, and while the package-phase owner holds a record of a transaction: its phase is one this
// gate cannot prove safe. A host where no backend lock can be read is not proven idle and refuses.
type PackageGate struct {
	Locks []string
	Phase string
}

// Name implements Gate.
func (PackageGate) Name() string { return gatePackage }

// Check implements Gate.
func (g PackageGate) Check(context.Context, PendingNetwork) error {
	readable := 0
	for _, path := range g.Locks {
		held, err := lockHeld(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return refuse(gatePackage, "a package backend's lock cannot be read")
		}
		readable++
		if held {
			return refuse(gatePackage, "a package backend is in a transaction")
		}
	}
	if readable == 0 {
		return refuse(gatePackage, "no package backend's lock can be read, so the backend is not proven idle")
	}
	if g.Phase == "" {
		return refuse(gatePackage, "the package-phase record is not composed")
	}
	if _, err := os.Lstat(g.Phase); err == nil {
		return refuse(gatePackage, "a package transaction's phase is recorded and not resolved")
	} else if !errors.Is(err, os.ErrNotExist) {
		return refuse(gatePackage, "the package-phase record cannot be read")
	}
	return nil
}

// StorageGate refuses while the storage daemon runs a job, managed or not, or cannot answer. A
// daemon that is not running runs no job: its absence is established on the bus, not read from a
// file.
type StorageGate struct {
	Jobs func(context.Context) (int, error)
}

// Name implements Gate.
func (StorageGate) Name() string { return gateStorage }

// Check implements Gate.
func (g StorageGate) Check(ctx context.Context, _ PendingNetwork) error {
	if g.Jobs == nil {
		return refuse(gateStorage, "the storage daemon's jobs are not composed")
	}
	jobs, err := g.Jobs(ctx)
	switch {
	case err != nil:
		return refuse(gateStorage, "the storage daemon's jobs cannot be read")
	case jobs > 0:
		return refuse(gateStorage, "the storage daemon runs a job")
	}
	return nil
}

// holdWait bounds how long HostHold waits for the operation store's lock: an admission holds it
// only while it claims a record.
const holdWait = 5 * time.Second

// HostHold is the serialization point: the lifecycle lock held shared, without waiting, so no
// lifecycle transition is running or can start, and then the operation engine's own store lock held
// exclusively, so no host operation is admitted, until release. Neither lock is created or replaced
// here; an absent or unreadable one refuses. It takes no network lock.
type HostHold struct {
	LifecycleDir string
	RecordsDir   string
}

// Hold implements Serializer.
func (h HostHold) Hold(ctx context.Context) (func(), error) {
	lifecycle, err := hostops.OpenLifecycle(h.LifecycleDir, hostops.RolePortal)
	if err != nil {
		return nil, errors.New("the lifecycle lock cannot be opened")
	}
	if err := lifecycle.TryShared(); err != nil {
		_ = lifecycle.Close()
		return nil, errors.New("a lifecycle transition holds the lifecycle lock")
	}
	store, err := os.OpenFile(filepath.Join(h.RecordsDir, storeLock), os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		_ = lifecycle.Close()
		return nil, errors.New("the operation store's lock cannot be opened")
	}
	if err := exclusiveWithin(ctx, store, holdWait); err != nil {
		_ = store.Close()
		_ = lifecycle.Close()
		return nil, errors.New("the operation store's lock could not be held")
	}
	return func() {
		_ = unlock(store)
		_ = store.Close()
		_ = lifecycle.Close()
	}, nil
}

// CurrentBoot reads this boot's identity as the kernel states it.
func CurrentBoot() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	boot := strings.TrimSpace(string(data))
	if !bootIDShape(boot) {
		return "", errors.New("the boot identity is not a boot id")
	}
	return boot, nil
}
