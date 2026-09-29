// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/hostops/console"
	"github.com/olivaresai/olivares/appliance/layer/portal/localclient"
	"github.com/olivaresai/olivares/appliance/layer/services"
)

// session is the local API as the CLI sees it; it records each plan asked of it.
type session struct {
	plans  []string
	err    error
	closed bool
}

func (s *session) Descriptors(string) ([]hostops.Descriptor, error) {
	return services.Descriptors(), nil
}
func (s *session) Operation(string, string) (hostops.View, int, error) {
	return hostops.View{}, 0, &localclient.Refused{Code: "input_refused"}
}
func (s *session) Description(module, verb, surface string) (hostops.Descriptor, error) {
	catalog, _ := hostops.NewCatalog(services.Descriptors())
	return catalog.Describe(module, verb, surface)
}
func (s *session) Plan(module, verb, surface string, inputs json.RawMessage) (hostops.Plan, error) {
	s.plans = append(s.plans, module+" "+verb+" "+surface+" "+string(inputs))
	if s.err != nil {
		return hostops.Plan{}, s.err
	}
	d, err := s.Description(module, verb, surface)
	if err != nil {
		return hostops.Plan{}, &localclient.Refused{Code: "input_refused"}
	}
	return hostops.Plan{Descriptor: d, Inputs: inputs, Permissions: hostops.Permissions{Read: true, Plan: true, Code: "act_not_adopted"}}, nil
}
func (s *session) Close() error { s.closed = true; return nil }

func TestServiceCLI_PlansThroughTheLocalAPIAndRefusesBeforeDialing(t *testing.T) {
	run := func(s *session, args ...string) (int, bool, string, string, int) {
		dials := 0
		var out, diagnostic bytes.Buffer
		code, handled := runService(args, &out, &diagnostic, func() (console.Session, error) { dials++; return s, nil },
			func() (moduleSession, error) { dials++; return hostReads(), nil })
		return code, handled, out.String(), diagnostic.String(), dials
	}
	for _, c := range []struct {
		args  []string
		plan  string
		shows []string
	}{
		{[]string{"service", "stop", "olivares.service", "--plan"}, `service stop cli {"unit":"olivares.service"}`,
			[]string{"Stop a unit", "class managed", "Command: olivares-appliance service stop olivares.service", "Changes: disabled (act_not_adopted)"}},
		{[]string{"service", "logs", "sshd.service", "--lines", "20", "--plan"}, `service logs cli {"lines":20,"unit":"sshd.service"}`,
			[]string{"Unit logs", "class lockout-risk", "Command: olivares-appliance service logs sshd.service --lines 20"}},
		{[]string{"service", "list", "--plan"}, `service list cli {}`, []string{"List units", "Command: olivares-appliance service list"}},
	} {
		s := &session{}
		code, handled, out, diagnostic, dials := run(s, c.args...)
		if code != 0 || !handled || dials != 1 || !s.closed || len(s.plans) != 1 || s.plans[0] != c.plan || diagnostic != "" {
			t.Errorf("%v: exit %d handled %v dials %d plans %v stderr %q", c.args, code, handled, dials, s.plans, diagnostic)
		}
		for _, want := range c.shows {
			if !strings.Contains(out, want) {
				t.Errorf("%v: the plan does not show %q:\n%s", c.args, want, out)
			}
		}
	}
	// Refused before dialing: the class table, acts and reads the local API does not carry,
	// and every command line outside the closed grammar.
	for _, c := range []struct {
		args []string
		code string
	}{
		{[]string{"service", "stop", "dbus.service", "--plan"}, "unit_protected"},
		{[]string{"service", "start", "NetworkManager.service", "--plan"}, "unit_protected"},
		{[]string{"service", "start", "systemd-reboot.service", "--plan"}, "unit_protected"},
		{[]string{"service", "disable", "srv-olivares-mnt-host-data.mount", "--plan"}, "unit_protected"},
		{[]string{"service", "stop", "olivares.service"}, "act_not_adopted"},
		{[]string{"service", "logs", "olivares.service"}, "act_not_adopted"},
		{[]string{"service", "stop", "/etc/passwd", "--plan"}, "input_refused"},
		{[]string{"service", "stop", "olivares.service", "--unit", "x.service", "--plan"}, "input_refused"},
		{[]string{"service", "mask", "olivares.service", "--plan"}, "input_refused"},
		{[]string{"service", "logs", "olivares.service", "--lines", "501", "--plan"}, "input_refused"},
		{[]string{"service", "logs", "olivares.service", "--lines", "--plan"}, "input_refused"},
		{[]string{"service", "stop", "olivares.service", "--lines", "5", "--plan"}, "input_refused"},
		{[]string{"service", "stop", "olivares.service", "--plan", "--output", "json"}, "input_refused"},
		{[]string{"service", "list", "olivares.service", "--plan"}, "input_refused"},
		{[]string{"service"}, "input_refused"},
	} {
		s := &session{}
		code, handled, out, diagnostic, dials := run(s, c.args...)
		first := ""
		if words := strings.Fields(diagnostic); len(words) > 0 {
			first = words[0]
		}
		if code != 1 || !handled || dials != 0 || out != "" || first != c.code {
			t.Errorf("%v: exit %d handled %v dials %d stdout %q stderr %q, want %s before dialing", c.args, code, handled, dials, out, diagnostic, c.code)
		}
	}
	// The reads without --plan are TestServiceCLI_ListAndStatusReadThroughTheLocalAPI's.
	// A plan the local API refuses is that refusal; a lost session is exit 2.
	s := &session{err: &localclient.Refused{Code: "mode_refused"}}
	if code, _, _, diagnostic, _ := run(s, "service", "restart", "olivares.service", "--plan"); code != 1 || strings.TrimSpace(diagnostic) != "mode_refused" {
		t.Errorf("a refused plan: exit %d %q", code, diagnostic)
	}
	s = &session{err: &localclient.Failure{Code: "response_unverified", Sent: true}}
	if code, _, _, diagnostic, _ := run(s, "service", "restart", "olivares.service", "--plan"); code != 2 || strings.TrimSpace(diagnostic) != "response_unverified" {
		t.Errorf("a lost plan: exit %d %q", code, diagnostic)
	}
	// Other command lines are the common console's.
	for _, args := range [][]string{{"op", "get", strings.Repeat("12", 16)}, {"host", "status", "--plan"}, {}, {"tui"}} {
		if _, handled, _, _, dials := run(&session{}, args...); handled || dials != 0 {
			t.Errorf("%v was taken by the service verbs", args)
		}
	}
}
