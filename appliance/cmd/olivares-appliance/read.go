// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/appliance/layer/portal/localclient"
	"github.com/olivaresai/olivares/appliance/layer/portal/localsession"
	"github.com/olivaresai/olivares/appliance/layer/services"
)

// moduleSession is the local API's module read as the CLI uses it.
type moduleSession interface {
	Read(query, unit string, offset int, surface string) (localsession.ModuleAnswer, error)
	Close() error
}

// readDial opens a local API session for a module read.
type readDial func() (moduleSession, error)

// maxPages bounds one paged read.
const maxPages = 256

// errReadChanged is a list whose read changed between its pages again after it was read anew,
// or that did not end within maxPages: the read could not run.
var errReadChanged = errors.New("consumer_unavailable")

// readPages reads every page of query, all from one read of the portal's model. When a page comes
// from another read than the first page, the list is read again from its start, once.
func readPages(session moduleSession, query string) (localsession.ModuleAnswer, error) {
	for attempt := 0; attempt < 2; attempt++ {
		all, err := session.Read(query, "", 0, "cli")
		if err != nil {
			return localsession.ModuleAnswer{}, err
		}
		changed := false
		for next, pages := all.Next, 1; next != 0; pages++ {
			if pages >= maxPages {
				return localsession.ModuleAnswer{}, errReadChanged
			}
			page, err := session.Read(query, "", next, "cli")
			if err != nil {
				return localsession.ModuleAnswer{}, err
			}
			if page.Generation != all.Generation || page.Total != all.Total || (page.Next != 0 && page.Next <= next) {
				changed = true
				break
			}
			all.Units = append(all.Units, page.Units...)
			all.Devices = append(all.Devices, page.Devices...)
			all.FirewallRows = append(all.FirewallRows, page.FirewallRows...)
			next = page.Next
		}
		if !changed {
			all.Next = 0
			return all, nil
		}
	}
	return localsession.ModuleAnswer{}, errReadChanged
}

// reportRead prints a module read's refusal or failure and returns its exit: 1 for a refusal with
// its closed code, 2 for a read that could not run (consumer_unavailable, no session, a lost or
// unverified answer).
func reportRead(w io.Writer, err error) int {
	var refused *localclient.Refused
	switch {
	case errors.As(err, &refused) && refused.Code != "consumer_unavailable":
		_, _ = fmt.Fprintln(w, printable(refused.Code))
		return 1
	case errors.As(err, &refused), errors.Is(err, errReadChanged):
		_, _ = fmt.Fprintln(w, "consumer_unavailable")
		_, _ = fmt.Fprintln(w, "The portal has no completed read of this module yet, or the read changed while it was listed. Nothing was changed.")
		return 2
	}
	return reportService(w, err)
}

// readService answers "service list" and "service status <unit>" from the local API's module
// read and returns the exit.
func readService(c serviceCommand, out, diagnostic io.Writer, read readDial) int {
	session, err := read()
	if err != nil {
		return reportRead(diagnostic, err)
	}
	defer session.Close()
	var lines []string
	if c.verb == services.OpList {
		answer, err := readPages(session, localsession.QueryServicesList)
		if err != nil {
			return reportRead(diagnostic, err)
		}
		lines = append(lines, "Units, as read at "+printable(answer.ReadAt)+":")
		for _, u := range answer.Units {
			lines = append(lines, fmt.Sprintf("%-48s %-14s %s (%s)", printable(u.Name), printable(u.Class), printable(u.ActiveState), printable(u.SubState)))
		}
		if answer.Truncated {
			lines = append(lines, "The list stops at the units helper's bound.")
		}
	} else {
		answer, err := session.Read(localsession.QueryServicesStatus, c.unit, 0, "cli")
		if err == nil && (answer.Unit == nil || answer.Unit.Unit != c.unit) {
			err = &localclient.Failure{Code: "response_unverified", Reason: "record_malformed", Sent: true}
		}
		if err != nil {
			return reportRead(diagnostic, err)
		}
		u := answer.Unit
		allowed := make([]string, 0, len(u.Allowed))
		for _, op := range u.Allowed {
			allowed = append(allowed, printable(op))
		}
		lines = append(lines,
			"Unit: "+printable(u.Unit),
			"Class: "+printable(u.Class)+". "+printable(u.Consequence),
			"State: "+printable(u.LoadState)+", "+printable(u.ActiveState)+" ("+printable(u.SubState)+")",
			"Admits: "+strings.Join(allowed, ", "),
			"As read at "+printable(answer.ReadAt)+"; the unit-file state is not part of this read.")
	}
	if _, err := fmt.Fprintln(out, strings.Join(lines, "\n")); err != nil {
		return 2
	}
	return 0
}

// runStorage handles "storage list", the storage inventory read, and reports whether args were
// it. Every other storage command line, the plan among them, is the common console's.
func runStorage(args []string, out, diagnostic io.Writer, read readDial) (int, bool) {
	if len(args) != 2 || args[0] != "storage" || args[1] != "list" {
		return 0, false
	}
	session, err := read()
	if err != nil {
		return reportRead(diagnostic, err), true
	}
	defer session.Close()
	answer, err := readPages(session, localsession.QueryStorageInventory)
	if err != nil {
		return reportRead(diagnostic, err), true
	}
	lines := []string{"Block devices, as read at " + printable(answer.ReadAt) + ":"}
	for _, d := range answer.Devices {
		kind := printable(d.Kind)
		if d.Kind == localsession.DevicePartition {
			kind += " of " + printable(d.Disk)
		}
		fields := []string{fmt.Sprintf("%-16s %-28s %10s", printable(d.Device), kind, gib(d.SizeBytes))}
		if d.Model != "" {
			fields = append(fields, printable(d.Model))
		}
		if d.Filesystem != "" {
			mounts := make([]string, 0, len(d.MountPoints))
			for _, m := range d.MountPoints {
				mounts = append(mounts, printable(m))
			}
			fs := printable(d.Filesystem)
			if len(mounts) > 0 {
				fs += " on " + strings.Join(mounts, ", ")
			}
			fields = append(fields, fs)
		}
		if d.SystemDisk {
			fields = append(fields, "system disk")
		}
		if d.InUse {
			fields = append(fields, "in use")
		}
		lines = append(lines, strings.Join(fields, "  "))
	}
	lines = append(lines, "Changes are planned with --plan; this read changed nothing.")
	if _, err := fmt.Fprintln(out, strings.Join(lines, "\n")); err != nil {
		return 2, true
	}
	return 0, true
}

// gib formats a size in GiB with one decimal.
func gib(bytes uint64) string {
	return strconv.FormatFloat(float64(bytes)/(1<<30), 'f', 1, 64) + " GiB"
}
