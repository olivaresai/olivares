// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package services

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	systemdName       = "org.freedesktop.systemd1"
	systemdPath       = dbus.ObjectPath("/org/freedesktop/systemd1")
	managerInterface  = "org.freedesktop.systemd1.Manager"
	unitInterface     = "org.freedesktop.systemd1.Unit"
	unitPathPrefix    = "/org/freedesktop/systemd1/unit/"
	alreadySubscribed = "org.freedesktop.systemd1.AlreadySubscribed"
	dialTimeout       = 5 * time.Second
)

// SystemBus is the Bus of an installed appliance: a private connection to the system bus that
// talks to the service manager alone, whose unique bus name it resolves once and checks on
// every signal.
type SystemBus struct {
	conn       *dbus.Conn
	owner      string
	mu         sync.Mutex
	subscribed bool
}

// DialSystemBus opens a private, authenticated connection to the system bus and resolves the
// service manager's unique name.
func DialSystemBus() (*SystemBus, error) {
	conn, err := dbus.SystemBusPrivate()
	if err != nil {
		return nil, err
	}
	ready := make(chan error, 1)
	go func() {
		if err := conn.Auth(nil); err != nil {
			ready <- err
			return
		}
		ready <- conn.Hello()
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
	case <-time.After(dialTimeout):
		_ = conn.Close()
		return nil, errors.New("the system bus did not answer")
	}
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	var owner string
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, systemdName).Store(&owner); err != nil || !strings.HasPrefix(owner, ":") {
		_ = conn.Close()
		return nil, errors.New("the service manager has no name on the system bus")
	}
	return &SystemBus{conn: conn, owner: owner}, nil
}

// Close closes the connection.
func (b *SystemBus) Close() error { return b.conn.Close() }

func (b *SystemBus) manager() dbus.BusObject { return b.conn.Object(systemdName, systemdPath) }

// ListUnitsByPatterns implements Bus with the manager's ListUnitsByPatterns.
func (b *SystemBus) ListUnitsByPatterns(ctx context.Context, states, patterns []string) ([]Unit, error) {
	if states == nil {
		states = []string{}
	}
	if patterns == nil {
		patterns = []string{}
	}
	var rows []struct {
		Name, Description, LoadState, ActiveState, SubState, Following string
		Path                                                           dbus.ObjectPath
		JobID                                                          uint32
		JobType                                                        string
		JobPath                                                        dbus.ObjectPath
	}
	if err := b.manager().CallWithContext(ctx, managerInterface+".ListUnitsByPatterns", 0, states, patterns).Store(&rows); err != nil {
		return nil, err
	}
	units := make([]Unit, 0, len(rows))
	for _, r := range rows {
		units = append(units, Unit{Name: r.Name, LoadState: r.LoadState, ActiveState: r.ActiveState, SubState: r.SubState})
	}
	return units, nil
}

// GetUnitFileState implements Bus with the manager's GetUnitFileState.
func (b *SystemBus) GetUnitFileState(ctx context.Context, unit string) (string, error) {
	var state string
	if err := b.manager().CallWithContext(ctx, managerInterface+".GetUnitFileState", 0, unit).Store(&state); err != nil {
		return "", err
	}
	return state, nil
}

// Dependents implements Bus with the unit's RequiredBy and BoundBy properties. A unit that is
// not loaded has none.
func (b *SystemBus) Dependents(ctx context.Context, unit string) ([]string, error) {
	var path dbus.ObjectPath
	if err := b.manager().CallWithContext(ctx, managerInterface+".GetUnit", 0, unit).Store(&path); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(string(path), unitPathPrefix) {
		return nil, errors.New("the service manager named a unit object outside its unit tree")
	}
	var out []string
	for _, property := range []string{"RequiredBy", "BoundBy"} {
		var value dbus.Variant
		if err := b.conn.Object(systemdName, path).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, unitInterface, property).Store(&value); err != nil {
			return nil, err
		}
		names, ok := value.Value().([]string)
		if !ok {
			return nil, errors.New("a dependents property is not a list of names")
		}
		out = append(out, names...)
	}
	return out, nil
}

// Subscribe implements Bus. The first call adds the match for the manager's JobRemoved signal
// and calls the manager's Subscribe; every call then registers its own channel, which receives
// only JobRemoved signals sent by the manager's unique name from its object path.
func (b *SystemBus) Subscribe(ctx context.Context) (<-chan JobRemoved, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.subscribed {
		if err := b.conn.AddMatchSignalContext(ctx, dbus.WithMatchSender(systemdName), dbus.WithMatchObjectPath(systemdPath),
			dbus.WithMatchInterface(managerInterface), dbus.WithMatchMember("JobRemoved")); err != nil {
			return nil, nil, err
		}
		if err := b.manager().CallWithContext(ctx, managerInterface+".Subscribe", 0).Err; err != nil && errorName(err) != alreadySubscribed {
			return nil, nil, err
		}
		b.subscribed = true
	}
	raw := make(chan *dbus.Signal, 64)
	b.conn.Signal(raw)
	out := make(chan JobRemoved, 64)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case s, ok := <-raw:
				if !ok {
					return
				}
				removed, ok := b.jobRemoved(s)
				if !ok {
					continue
				}
				select {
				case out <- removed:
				case <-done:
					return
				}
			}
		}
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			b.conn.RemoveSignal(raw)
			close(done)
		})
	}
	return out, stop, nil
}

// jobRemoved reads s as the manager's JobRemoved(u id, o job, s unit, s result).
func (b *SystemBus) jobRemoved(s *dbus.Signal) (JobRemoved, bool) {
	if s == nil || s.Sender != b.owner || s.Path != systemdPath || s.Name != managerInterface+".JobRemoved" || len(s.Body) != 4 {
		return JobRemoved{}, false
	}
	id, ok1 := s.Body[0].(uint32)
	job, ok2 := s.Body[1].(dbus.ObjectPath)
	unit, ok3 := s.Body[2].(string)
	result, ok4 := s.Body[3].(string)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return JobRemoved{}, false
	}
	return JobRemoved{ID: id, Job: string(job), Unit: unit, Result: result}, true
}

// QueueJob implements Bus with StartUnit, StopUnit, RestartUnit or ReloadUnit, and no other
// method.
func (b *SystemBus) QueueJob(ctx context.Context, method, unit, mode string) (string, error) {
	switch method {
	case "StartUnit", "StopUnit", "RestartUnit", "ReloadUnit":
	default:
		return "", errors.New("not a job method of the units helper")
	}
	var job dbus.ObjectPath
	if err := b.manager().CallWithContext(ctx, managerInterface+"."+method, 0, unit, mode).Store(&job); err != nil {
		return "", err
	}
	return string(job), nil
}

// unitFileChange is one change EnableUnitFiles or DisableUnitFiles reports: type, file name and
// destination.
type unitFileChange struct{ Type, Filename, Destination string }

// EnableUnitFiles implements Bus with the manager's EnableUnitFiles.
func (b *SystemBus) EnableUnitFiles(ctx context.Context, units []string, runtime, force bool) (int, error) {
	var carriesInstallInfo bool
	var changes []unitFileChange
	if err := b.manager().CallWithContext(ctx, managerInterface+".EnableUnitFiles", 0, units, runtime, force).Store(&carriesInstallInfo, &changes); err != nil {
		return 0, err
	}
	return len(changes), nil
}

// DisableUnitFiles implements Bus with the manager's DisableUnitFiles.
func (b *SystemBus) DisableUnitFiles(ctx context.Context, units []string, runtime bool) (int, error) {
	var changes []unitFileChange
	if err := b.manager().CallWithContext(ctx, managerInterface+".DisableUnitFiles", 0, units, runtime).Store(&changes); err != nil {
		return 0, err
	}
	return len(changes), nil
}

// errorName is the D-Bus error name of err, or "".
func errorName(err error) string {
	var value dbus.Error
	if errors.As(err, &value) {
		return value.Name
	}
	var pointer *dbus.Error
	if errors.As(err, &pointer) && pointer != nil {
		return pointer.Name
	}
	return ""
}
