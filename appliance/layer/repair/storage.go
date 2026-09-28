// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package repair

import (
	"context"
	"errors"
	"time"

	"github.com/godbus/dbus/v5"
)

// udisks is the storage daemon's name and its object manager on the system bus.
const (
	udisks        = "org.freedesktop.UDisks2"
	udisksManager = dbus.ObjectPath("/org/freedesktop/UDisks2")
	udisksJob     = udisks + ".Job"
)

// busWait bounds the system bus's authentication.
const busWait = 5 * time.Second

// UDisksJobs counts the jobs the storage daemon runs now, whoever asked for them. A daemon without
// a name on the bus runs none; the bus is asked, never activated, so reading does not start it.
func UDisksJobs(ctx context.Context) (int, error) {
	conn, err := dbus.SystemBusPrivate()
	if err != nil {
		return 0, err
	}
	defer conn.Close()
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
			return 0, err
		}
	case <-time.After(busWait):
		return 0, errors.New("the system bus did not answer")
	}
	var running bool
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, udisks).Store(&running); err != nil {
		return 0, err
	}
	if !running {
		return 0, nil
	}
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := conn.Object(udisks, udisksManager).CallWithContext(ctx, "org.freedesktop.DBus.ObjectManager.GetManagedObjects",
		dbus.FlagNoAutoStart).Store(&objects); err != nil {
		return 0, err
	}
	jobs := 0
	for _, interfaces := range objects {
		if _, ok := interfaces[udisksJob]; ok {
			jobs++
		}
	}
	return jobs, nil
}
