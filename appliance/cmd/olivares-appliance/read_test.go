// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/hostops/console"
	"github.com/olivaresai/olivares/appliance/layer/portal/localclient"
	"github.com/olivaresai/olivares/appliance/layer/portal/localsession"
)

// reads is the local API's module read as the CLI sees it: one answer or error per query, unit
// and offset. It records each read asked of it.
type reads struct {
	answers map[string]localsession.ModuleAnswer
	errs    map[string]error
	asked   []string
	closed  bool
}

func readKey(query, unit string, offset int) string {
	return query + " " + unit + " " + strconv.Itoa(offset)
}

func (r *reads) Read(query, unit string, offset int, surface string) (localsession.ModuleAnswer, error) {
	key := readKey(query, unit, offset)
	r.asked = append(r.asked, key+" "+surface)
	if err := r.errs[key]; err != nil {
		return localsession.ModuleAnswer{}, err
	}
	answer, ok := r.answers[key]
	if !ok {
		return localsession.ModuleAnswer{}, &localclient.Refused{Code: "input_refused"}
	}
	return answer, nil
}

func (r *reads) Close() error { r.closed = true; return nil }

// hostReads is a host of three units, read in two pages, and one disk of one partition.
func hostReads() *reads {
	stamp := func(a localsession.ModuleAnswer) localsession.ModuleAnswer {
		a.Generation, a.ReadAt = 4, "2026-09-27T19:00:00Z"
		return a
	}
	return &reads{answers: map[string]localsession.ModuleAnswer{
		readKey(localsession.QueryServicesList, "", 0): stamp(localsession.ModuleAnswer{Query: localsession.QueryServicesList, Total: 3, Next: 2, Units: []localsession.UnitRow{
			{Name: "chronyd.service", Class: "other", LoadState: "loaded", ActiveState: "inactive", SubState: "dead"},
			{Name: "olivares.service", Class: "managed", LoadState: "loaded", ActiveState: "active", SubState: "running"},
		}}),
		readKey(localsession.QueryServicesList, "", 2): stamp(localsession.ModuleAnswer{Query: localsession.QueryServicesList, Offset: 2, Total: 3, Units: []localsession.UnitRow{
			{Name: "sshd.service", Class: "lockout-risk", LoadState: "loaded", ActiveState: "active", SubState: "running"},
		}}),
		readKey(localsession.QueryServicesStatus, "sshd.service", 0): stamp(localsession.ModuleAnswer{Query: localsession.QueryServicesStatus, Total: 1,
			Unit: &localsession.UnitStatus{Unit: "sshd.service", Class: "lockout-risk", Consequence: "Stopping it can lock you out.", Allowed: []string{"status", "logs", "restart"},
				LoadState: "loaded", ActiveState: "active", SubState: "running"}}),
		readKey(localsession.QueryStorageInventory, "", 0): stamp(localsession.ModuleAnswer{Query: localsession.QueryStorageInventory, Total: 2, Devices: []localsession.DeviceRow{
			{Device: "/dev/vda", Kind: localsession.DeviceDisk, SizeBytes: 20 << 30, Model: "QEMU HARDDISK", SystemDisk: true},
			{Device: "/dev/vda1", Kind: localsession.DevicePartition, Disk: "/dev/vda", SizeBytes: 20 << 30, Filesystem: "xfs", MountPoints: []string{"/"}, SystemDisk: true, InUse: true},
		}}),
	}, errs: map[string]error{}}
}

// runRead runs one command line with r as the local API and returns the exit, whether a verb
// took it, stdout, stderr, the plan dials and the read dials.
func runRead(r *reads, args ...string) (int, bool, string, string, int, int) {
	plans, dials := 0, 0
	var out, diagnostic bytes.Buffer
	plan := func() (console.Session, error) { plans++; return &session{}, nil }
	read := func() (moduleSession, error) { dials++; return r, nil }
	code, handled := runService(args, &out, &diagnostic, plan, read)
	if !handled {
		code, handled = runStorage(args, &out, &diagnostic, read)
	}
	return code, handled, out.String(), diagnostic.String(), plans, dials
}

func firstWord(s string) string {
	if words := strings.Fields(s); len(words) > 0 {
		return words[0]
	}
	return ""
}

func TestServiceCLI_ListAndStatusReadThroughTheLocalAPI(t *testing.T) {
	r := hostReads()
	code, handled, out, diagnostic, plans, dials := runRead(r, "service", "list")
	if code != 0 || !handled || diagnostic != "" || plans != 0 || dials != 1 || !r.closed ||
		strings.Join(r.asked, ";") != "services.list  0 cli;services.list  2 cli" {
		t.Fatalf("service list: exit %d handled %v stderr %q plans %d dials %d asked %v", code, handled, diagnostic, plans, dials, r.asked)
	}
	for _, want := range []string{"chronyd.service", "olivares.service", "sshd.service", "lockout-risk", "active (running)", "inactive (dead)", "2026-09-27T19:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("service list does not show %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "sshd.service") != 1 {
		t.Errorf("a unit is shown more than once:\n%s", out)
	}

	r = hostReads()
	code, _, out, diagnostic, _, dials = runRead(r, "service", "status", "sshd.service")
	if code != 0 || diagnostic != "" || dials != 1 || !r.closed || strings.Join(r.asked, ";") != "services.status sshd.service 0 cli" {
		t.Fatalf("service status: exit %d stderr %q asked %v", code, diagnostic, r.asked)
	}
	for _, want := range []string{"sshd.service", "lockout-risk", "Stopping it can lock you out.", "active (running)", "restart", "2026-09-27T19:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("service status does not show %q:\n%s", want, out)
		}
	}

	// Exits per the console contract: 1 for a refusal with its closed code, 2 for a read that could
	// not run, with nothing on stdout.
	for _, c := range []struct {
		args []string
		err  error
		exit int
		code string
	}{
		{[]string{"service", "status", "absent.service"}, nil, 1, "input_refused"},
		{[]string{"service", "status", "sshd.service"}, &localclient.Refused{Code: "mode_refused"}, 1, "mode_refused"},
		{[]string{"service", "list"}, &localclient.Refused{Code: "consumer_unavailable"}, 2, "consumer_unavailable"},
		{[]string{"service", "status", "sshd.service"}, &localclient.Refused{Code: "consumer_unavailable"}, 2, "consumer_unavailable"},
		{[]string{"service", "list"}, &localclient.Failure{Code: "response_unverified", Sent: true}, 2, "response_unverified"},
	} {
		r := hostReads()
		if c.err != nil {
			unit := ""
			if len(c.args) > 2 {
				unit = c.args[2]
			}
			query := localsession.QueryServicesList
			if c.args[1] == "status" {
				query = localsession.QueryServicesStatus
			}
			r.errs[readKey(query, unit, 0)] = c.err
		}
		code, _, out, diagnostic, _, _ := runRead(r, c.args...)
		if code != c.exit || out != "" || firstWord(diagnostic) != c.code || !r.closed {
			t.Errorf("%v with %v: exit %d stdout %q stderr %q, want %d %s", c.args, c.err, code, out, diagnostic, c.exit, c.code)
		}
	}

	// A list whose read changed between pages is read again from its start, once.
	r = hostReads()
	moved := r.answers[readKey(localsession.QueryServicesList, "", 2)]
	moved.Generation = 5
	r.answers[readKey(localsession.QueryServicesList, "", 2)] = moved
	if code, _, out, diagnostic, _, _ := runRead(r, "service", "list"); code != 2 || out != "" || firstWord(diagnostic) != "consumer_unavailable" || len(r.asked) != 4 {
		t.Errorf("a list that kept changing: exit %d stdout %q stderr %q asked %v", code, out, diagnostic, r.asked)
	}

	// A session that cannot be opened could not run; a command line outside the grammar, or a
	// unit the class table refuses, is refused before dialing.
	var out2, diagnostic2 bytes.Buffer
	code, _ = runService([]string{"service", "list"}, &out2, &diagnostic2, nil, func() (moduleSession, error) {
		return nil, &localclient.Failure{Code: "local_unavailable", Reason: "socket_unavailable"}
	})
	if code != 2 || firstWord(diagnostic2.String()) != "local_unavailable" || out2.Len() != 0 {
		t.Errorf("no session: exit %d stderr %q", code, diagnostic2.String())
	}
	for _, args := range [][]string{{"service", "list", "sshd.service"}, {"service", "status"}, {"service", "status", "/etc/passwd"}, {"service", "status", "sshd.service", "--output", "json"}} {
		r := hostReads()
		if code, handled, out, diagnostic, plans, dials := runRead(r, args...); code != 1 || !handled || out != "" || plans != 0 || dials != 0 || firstWord(diagnostic) != "input_refused" {
			t.Errorf("%v: exit %d handled %v stdout %q stderr %q dials %d/%d", args, code, handled, out, diagnostic, plans, dials)
		}
	}
	// A plan is still the plan dial's, never a read.
	if code, _, _, _, plans, dials := runRead(hostReads(), "service", "status", "sshd.service", "--plan"); code != 0 || plans != 1 || dials != 0 {
		t.Errorf("a plan: exit %d plans %d reads %d", code, plans, dials)
	}
}

func TestStorageCLI_ListReadsTheInventoryThroughTheLocalAPI(t *testing.T) {
	r := hostReads()
	code, handled, out, diagnostic, plans, dials := runRead(r, "storage", "list")
	if code != 0 || !handled || diagnostic != "" || plans != 0 || dials != 1 || !r.closed || strings.Join(r.asked, ";") != "storage.inventory  0 cli" {
		t.Fatalf("storage list: exit %d handled %v stderr %q asked %v", code, handled, diagnostic, r.asked)
	}
	for _, want := range []string{"/dev/vda", "/dev/vda1", "xfs", "/", "QEMU HARDDISK", "system disk", "in use", "2026-09-27T19:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("storage list does not show %q:\n%s", want, out)
		}
	}

	r = hostReads()
	r.errs[readKey(localsession.QueryStorageInventory, "", 0)] = &localclient.Refused{Code: "consumer_unavailable"}
	if code, _, out, diagnostic, _, _ := runRead(r, "storage", "list"); code != 2 || out != "" || firstWord(diagnostic) != "consumer_unavailable" {
		t.Errorf("an unread inventory: exit %d stdout %q stderr %q", code, out, diagnostic)
	}

	// Every other storage command line is the common console's: the plan, and what it refuses.
	for _, args := range [][]string{{"storage", "list", "--plan"}, {"storage", "format", "/dev/vda"}, {"storage"}, {"storage", "list", "/dev/vda"}} {
		r := hostReads()
		if _, handled, _, _, _, dials := runRead(r, args...); handled || dials != 0 {
			t.Errorf("%v was taken by the storage read", args)
		}
	}
}
