// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// NetworkdUnit records separate loaded-unit and unit-file observations. A unit
// missing from systemd's loaded set does not prove that its file is absent.
type NetworkdUnit struct {
	NotLoaded, FileAbsent                 bool
	LoadState, ActiveState, UnitFileState string
	Job                                   uint32
}

// NetworkdState contains only the two fixed networkd activation units.
type NetworkdState struct{ Service, Socket NetworkdUnit }

// NetworkdReader observes existing systemd state without loading or starting units.
type NetworkdReader func(context.Context) (NetworkdState, error)

type networkdBus interface {
	Auth([]dbus.Auth) error
	Hello() error
	Close() error
	call(context.Context, string, dbus.ObjectPath, string, dbus.Flags, ...any) *dbus.Call
}

type networkdSystemBus struct{ *dbus.Conn }

func (b networkdSystemBus) call(ctx context.Context, dest string, path dbus.ObjectPath, method string, flags dbus.Flags, args ...any) *dbus.Call {
	return b.Object(dest, path).CallWithContext(ctx, method, flags, args...)
}

// ReadNetworkd reads two fixed units on a private system bus connection. It never
// uses LoadUnit, automatic bus activation, a command or a systemd mutation.
func ReadNetworkd(ctx context.Context) (NetworkdState, error) {
	return readNetworkd(ctx, func(ctx context.Context) (networkdBus, error) {
		// Bound the Unix dial too: SystemBusPrivate applies WithContext only
		// after dialing. This reader needs no descriptor-passing transport.
		socket, err := (&net.Dialer{}).DialContext(ctx, "unix", "/run/dbus/system_bus_socket")
		if err != nil {
			return nil, err
		}
		conn, err := dbus.NewConn(socket, dbus.WithContext(ctx))
		if err != nil {
			socket.Close()
			return nil, err
		}
		return networkdSystemBus{conn}, nil
	})
}

func readNetworkd(ctx context.Context, dial func(context.Context) (networkdBus, error)) (NetworkdState, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var result NetworkdState
	if err := ctx.Err(); err != nil {
		return result, err
	}
	conn, err := dial(ctx)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	if err := conn.Auth(nil); err != nil {
		return result, err
	}
	if err := conn.Hello(); err != nil {
		return result, err
	}
	owner := func() (string, error) {
		name, err := networkdValue[string](conn.call(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus.GetNameOwner", dbus.FlagNoAutoStart, "org.freedesktop.systemd1"))
		if err == nil && (!strings.HasPrefix(name, ":") || !strings.Contains(name, ".")) {
			err = errors.New("systemd unique owner unavailable")
		}
		return name, err
	}
	name, err := owner()
	if err != nil {
		return result, err
	}
	call := func(path dbus.ObjectPath, method string, args ...any) *dbus.Call {
		return conn.call(ctx, name, path, method, dbus.FlagNoAutoStart, args...)
	}
	for _, unit := range []struct {
		name  string
		state *NetworkdUnit
	}{
		{"systemd-networkd.service", &result.Service}, {"systemd-networkd.socket", &result.Socket},
	} {
		if err := readNetworkdUnit(call, unit.name, unit.state); err != nil {
			return NetworkdState{}, err
		}
	}
	after, err := owner()
	if err != nil {
		return NetworkdState{}, err
	}
	if after != name {
		return NetworkdState{}, errors.New("systemd owner changed during observation")
	}
	return result, nil
}

func readNetworkdUnit(call func(dbus.ObjectPath, string, ...any) *dbus.Call, name string, state *NetworkdUnit) error {
	const manager = "org.freedesktop.systemd1.Manager"
	const root dbus.ObjectPath = "/org/freedesktop/systemd1"
	path, err := networkdValue[dbus.ObjectPath](call(root, manager+".GetUnit", name))
	if exactNetworkdBusError(err, "org.freedesktop.systemd1.NoSuchUnit") {
		state.NotLoaded = true
	} else if err != nil {
		return err
	} else {
		if !path.IsValid() || !strings.HasPrefix(string(path), string(root)+"/unit/") {
			return errors.New("invalid loaded unit path")
		}
		props, err := networkdValue[map[string]dbus.Variant](call(path, "org.freedesktop.DBus.Properties.GetAll", "org.freedesktop.systemd1.Unit"))
		if err != nil {
			return err
		}
		id, ok := props["Id"].Value().(string)
		if !ok || id != name {
			return errors.New("loaded unit identity mismatch")
		}
		state.LoadState, ok = props["LoadState"].Value().(string)
		if !ok || state.LoadState == "" {
			return errors.New("invalid unit load state")
		}
		state.ActiveState, ok = props["ActiveState"].Value().(string)
		if !ok || state.ActiveState == "" {
			return errors.New("invalid unit active state")
		}
		job, ok := props["Job"].Value().([]any)
		if !ok || len(job) != 2 || props["Job"].Signature().String() != "(uo)" {
			return errors.New("invalid unit job")
		}
		state.Job, ok = job[0].(uint32)
		jobPath, pathOK := job[1].(dbus.ObjectPath)
		if !ok || !pathOK || !jobPath.IsValid() || (state.Job == 0 && jobPath != "/") || (state.Job != 0 && string(jobPath) != string(root)+"/job/"+strconv.FormatUint(uint64(state.Job), 10)) {
			return errors.New("invalid unit job identity")
		}
	}
	// This manager method queries the file independently; GetUnit's NoSuchUnit
	// means only not loaded. systemd maps this method's ENOENT to FileNotFound.
	state.UnitFileState, err = networkdValue[string](call(root, manager+".GetUnitFileState", name))
	if exactNetworkdBusError(err, "org.freedesktop.DBus.Error.FileNotFound") {
		state.FileAbsent = true
		return nil
	}
	return err
}

func exactNetworkdBusError(err error, name string) bool {
	var pointer *dbus.Error
	if errors.As(err, &pointer) {
		return pointer.Name == name
	}
	var value dbus.Error
	return errors.As(err, &value) && value.Name == name
}

// Do not let dbus.Store coerce a wrong wire type (for example string to object
// path). Each of these fixed methods has exactly one result of the stated type.
func networkdValue[T any](call *dbus.Call) (T, error) {
	var zero T
	if call.Err != nil {
		return zero, call.Err
	}
	if len(call.Body) != 1 {
		return zero, errors.New("invalid systemd reply count")
	}
	value, ok := call.Body[0].(T)
	if !ok {
		return zero, errors.New("invalid systemd reply type")
	}
	return value, nil
}
