// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/portal/localsession"
	"github.com/olivaresai/olivares/appliance/layer/services"
	"github.com/olivaresai/olivares/appliance/layer/storage"
)

// moduleRefresh is how often the console reads the module helpers again.
const moduleRefresh = 30 * time.Second

// moduleReadTimeout bounds one helper read of a round; the storage helper bounds its own read to
// 20 seconds.
const moduleReadTimeout = 25 * time.Second

// moduleReads is the console's read model of the module helpers, which the local socket's
// module.read answers from: the last completed answer of the units helper's list, of the storage
// helper's inventory and of the firewall helper's status, with the firewall owner's measurement read
// in the same round. The console reads them itself, one round at a time behind the shared lifecycle
// lock, and never for a request, so no local read waits on a helper or runs one. It asks those three
// reads alone: no act reaches a helper through it.
type moduleReads struct {
	now func() time.Time
	// measure reads the firewall owner's measurement of this boot, or why it is unmeasured.
	measure    func() (firewall.Measurement, string)
	mu         sync.Mutex
	units      services.Inventory
	unitsStamp localsession.Stamp
	disks      storage.Inventory
	disksStamp localsession.Stamp
	wall       firewallRead
	wallStamp  localsession.Stamp
}

// firewallRead is one completed read of the firewall: the owner's status answer and the
// measurement read with it, or why the firewall was unmeasured then.
type firewallRead struct {
	status      firewall.Status
	measurement firewall.Measurement
	unmeasured  string
}

// maxFirewallStatus bounds the firewall helper's status answer the console keeps: the owner's
// confirmed policy and its windows.
const maxFirewallStatus = 64 << 10

func newModuleReads(now func() time.Time) *moduleReads { return &moduleReads{now: now} }

// Units implements localsession.ModuleReader.
func (m *moduleReads) Units() (services.Inventory, localsession.Stamp, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.units, m.unitsStamp, m.unitsStamp.Generation > 0
}

// Storage implements localsession.ModuleReader.
func (m *moduleReads) Storage() (storage.Inventory, localsession.Stamp, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.disks, m.disksStamp, m.disksStamp.Generation > 0
}

// Firewall implements localsession.ModuleReader: the head and the flat rows of the last completed
// firewall read, each measured port and then each window.
func (m *moduleReads) Firewall() (localsession.FirewallRead, localsession.Stamp, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wallStamp.Generation == 0 {
		return localsession.FirewallRead{}, m.wallStamp, false
	}
	r := m.wall
	read := localsession.FirewallRead{Head: localsession.FirewallHead{Unmeasured: r.unmeasured, ConfirmedDigest: r.status.ConfirmedDigest}}
	if r.unmeasured == "" {
		read.Head.PolicyDigest, read.Head.BootID = r.measurement.PolicyDigest, r.measurement.BootID
		read.Head.MeasuredAt, read.Head.InputPolicy = r.measurement.MeasuredAt, r.measurement.InputPolicy
		for _, row := range r.measurement.Rows {
			read.Rows = append(read.Rows, localsession.FirewallRow{Kind: localsession.FirewallPort, Port: row.Port,
				Interfaces: slices.Clone(row.Interfaces)})
		}
	}
	for _, w := range r.status.Windows {
		read.Rows = append(read.Rows, localsession.FirewallRow{Kind: localsession.FirewallWindow, OperationID: w.OperationID,
			State: w.State, Reason: w.Reason, CandidateDigest: w.CandidateDigest})
	}
	return read, m.wallStamp, true
}

// refresh reads each module helper once through call. A completed answer, the helper's closed
// document, replaces the last one with the next generation; any other answer keeps it.
func (m *moduleReads) refresh(ctx context.Context, call storage.CallFunc) {
	readCtx, cancel := context.WithTimeout(ctx, moduleReadTimeout)
	response, err := call(readCtx, helperschema.HelperUnits, &services.Request{Op: services.OpList})
	cancel()
	if err == nil && response.Result == helperschema.ResultAnswered {
		if inventory, ok := decodeUnits(response.Bundle); ok {
			m.mu.Lock()
			m.units, m.unitsStamp = inventory, localsession.Stamp{Generation: m.unitsStamp.Generation + 1, ReadAt: m.now()}
			m.mu.Unlock()
		}
	}
	readCtx, cancel = context.WithTimeout(ctx, moduleReadTimeout)
	inventory, err := storage.HelperReader(call)(readCtx)
	cancel()
	if err == nil {
		m.mu.Lock()
		m.disks, m.disksStamp = inventory, localsession.Stamp{Generation: m.disksStamp.Generation + 1, ReadAt: m.now()}
		m.mu.Unlock()
	}
	readCtx, cancel = context.WithTimeout(ctx, moduleReadTimeout)
	response, err = call(readCtx, helperschema.HelperFirewall, &firewall.Request{Op: firewall.OpStatus})
	cancel()
	if err == nil && response.Result == helperschema.ResultAnswered {
		if status, ok := decodeFirewallStatus(response.Bundle); ok {
			read := firewallRead{status: status, unmeasured: "no measurement was read"}
			if m.measure != nil {
				read.measurement, read.unmeasured = m.measure()
			}
			m.mu.Lock()
			m.wall, m.wallStamp = read, localsession.Stamp{Generation: m.wallStamp.Generation + 1, ReadAt: m.now()}
			m.mu.Unlock()
		}
	}
}

// decodeFirewallStatus reads the firewall helper's status answer: one closed status document within
// maxFirewallStatus, whose digest is its confirmed policy's ("" with none confirmed) and each of
// whose windows is in one of the owner's states.
func decodeFirewallStatus(data []byte) (firewall.Status, bool) {
	if len(data) == 0 || len(data) > maxFirewallStatus {
		return firewall.Status{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var status firewall.Status
	if err := decoder.Decode(&status); err != nil || status.Windows == nil {
		return firewall.Status{}, false
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return firewall.Status{}, false
	}
	switch {
	case status.Confirmed == nil && status.ConfirmedDigest != "":
		return firewall.Status{}, false
	case status.Confirmed != nil && (status.Confirmed.Validate() != nil || policy.Digest(*status.Confirmed) != status.ConfirmedDigest):
		return firewall.Status{}, false
	}
	for _, w := range status.Windows {
		if !slices.Contains([]string{firewall.WindowPending, firewall.WindowConfirmed, firewall.WindowReverted, firewall.WindowFailed}, w.State) {
			return firewall.Status{}, false
		}
	}
	return status, true
}

// decodeUnits reads the units helper's list answer: one closed unit inventory within the helper's
// own bound.
func decodeUnits(data []byte) (services.Inventory, bool) {
	if len(data) == 0 || len(data) > services.MaxAnswerBytes {
		return services.Inventory{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var inventory services.Inventory
	if err := decoder.Decode(&inventory); err != nil || inventory.Units == nil {
		return services.Inventory{}, false
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return services.Inventory{}, false
	}
	return inventory, true
}

// round reads the module helpers once behind the shared lifecycle lock. lock returns the lock's
// release, or an error when it is not available now; then nothing is asked and round returns
// false.
func (m *moduleReads) round(ctx context.Context, call storage.CallFunc, lock func() (func(), error)) bool {
	release, err := lock()
	if err != nil {
		return false
	}
	defer release()
	m.refresh(ctx, call)
	return true
}

// start runs a round at once and then one per tick, until the returned stop is called; stop
// returns once the round in progress has ended.
func (m *moduleReads) start(call storage.CallFunc, ticks <-chan time.Time, lock func() (func(), error)) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.round(ctx, call, lock)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks:
				m.round(ctx, call, lock)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// sharedLifecycle takes, without waiting, the shared lifecycle lock that the local reads take.
func sharedLifecycle() (func(), error) {
	lock, err := hostops.OpenLifecycle(localsession.LifecycleDirectory, hostops.RolePortal)
	if err != nil {
		return nil, err
	}
	if err := lock.TryShared(); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return func() { _ = lock.Close() }, nil
}
