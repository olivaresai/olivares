// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// fakeBus is a service manager on a fake systemd1 bus: an inventory of loaded units, their
// unit-file states, and a job queue that emits JobRemoved only to the callers subscribed when
// the job is queued, before the call's reply, as the real manager may.
type fakeBus struct {
	mu          sync.Mutex
	calls       []string
	modes       []string
	units       map[string]Unit
	files       map[string]string
	dependents  map[string][]string
	subscribers []chan JobRemoved
	jobs        int
	// finish decides, for a queued job, the JobRemoved signals emitted before the reply and
	// the unit's state afterwards.
	finish func(method, unit, job string) ([]JobRemoved, Unit)
	// fileAfter is a unit's file state after enable or disable; absent leaves it unchanged.
	fileAfter map[string]string
	queueErr  error
	subErr    error
}

func newFakeBus(units ...Unit) *fakeBus {
	b := &fakeBus{units: map[string]Unit{}, files: map[string]string{}, dependents: map[string][]string{}, fileAfter: map[string]string{}}
	for _, u := range units {
		b.units[u.Name] = u
		b.files[u.Name] = "enabled"
	}
	return b
}

func active(name string) Unit {
	return Unit{Name: name, LoadState: "loaded", ActiveState: "active", SubState: "running"}
}

func inactive(name string) Unit {
	return Unit{Name: name, LoadState: "loaded", ActiveState: "inactive", SubState: "dead"}
}

func failed(name string) Unit {
	return Unit{Name: name, LoadState: "loaded", ActiveState: "failed", SubState: "failed"}
}

// finishWith answers every job with one JobRemoved of result and leaves the unit in after.
func finishWith(result string, after func(string) Unit) func(method, unit, job string) ([]JobRemoved, Unit) {
	return func(_, unit, job string) ([]JobRemoved, Unit) {
		return []JobRemoved{{ID: 7, Job: job, Unit: unit, Result: result}}, after(unit)
	}
}

func (b *fakeBus) record(call string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, call)
}

// Calls returns the calls in the order the bus received them.
func (b *fakeBus) Calls() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.calls)
}

// effects returns the calls that ask the manager for an act or for its job signals.
func effects(calls []string) []string {
	var out []string
	for _, c := range calls {
		switch c {
		case "Subscribe", "StartUnit", "StopUnit", "RestartUnit", "ReloadUnit", "EnableUnitFiles", "DisableUnitFiles":
			out = append(out, c)
		}
	}
	return out
}

func (b *fakeBus) ListUnitsByPatterns(_ context.Context, _, patterns []string) ([]Unit, error) {
	b.record("ListUnitsByPatterns")
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Unit
	for _, u := range b.units {
		if len(patterns) == 0 || slices.Contains(patterns, u.Name) {
			out = append(out, u)
		}
	}
	slices.SortFunc(out, func(a, c Unit) int { return strings.Compare(a.Name, c.Name) })
	return out, nil
}

func (b *fakeBus) GetUnitFileState(_ context.Context, unit string) (string, error) {
	b.record("GetUnitFileState")
	b.mu.Lock()
	defer b.mu.Unlock()
	state, ok := b.files[unit]
	if !ok {
		return "", errors.New("org.freedesktop.DBus.Error.FileNotFound")
	}
	return state, nil
}

func (b *fakeBus) Dependents(_ context.Context, unit string) ([]string, error) {
	b.record("Dependents")
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.dependents[unit]), nil
}

func (b *fakeBus) Subscribe(context.Context) (<-chan JobRemoved, func(), error) {
	b.record("Subscribe")
	if b.subErr != nil {
		return nil, nil, b.subErr
	}
	ch := make(chan JobRemoved, 16)
	b.mu.Lock()
	b.subscribers = append(b.subscribers, ch)
	b.mu.Unlock()
	stop := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.subscribers = slices.DeleteFunc(b.subscribers, func(c chan JobRemoved) bool { return c == ch })
	}
	return ch, stop, nil
}

func (b *fakeBus) QueueJob(_ context.Context, method, unit, mode string) (string, error) {
	b.record(method)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.modes = append(b.modes, mode)
	if b.queueErr != nil {
		return "", b.queueErr
	}
	b.jobs++
	job := fmt.Sprintf("/org/freedesktop/systemd1/job/%d", 100+b.jobs)
	if b.finish != nil {
		signals, after := b.finish(method, unit, job)
		b.units[unit] = after
		for _, s := range signals {
			for _, sub := range b.subscribers {
				select {
				case sub <- s:
				default:
				}
			}
		}
	}
	return job, nil
}

func (b *fakeBus) EnableUnitFiles(_ context.Context, units []string, _, _ bool) (int, error) {
	b.record("EnableUnitFiles")
	return b.changeFiles(units)
}

func (b *fakeBus) DisableUnitFiles(_ context.Context, units []string, _ bool) (int, error) {
	b.record("DisableUnitFiles")
	return b.changeFiles(units)
}

func (b *fakeBus) changeFiles(units []string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.queueErr != nil {
		return 0, b.queueErr
	}
	changes := 0
	for _, u := range units {
		if after, ok := b.fileAfter[u]; ok && after != b.files[u] {
			b.files[u] = after
			changes++
		}
	}
	return changes, nil
}
