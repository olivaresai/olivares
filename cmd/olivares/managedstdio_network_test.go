// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
	sessionegress "github.com/olivaresai/olivares/modules/sessions/egress"
)

// idleProcess is a started child that says nothing until it is stopped.
type idleProcess struct{ out chan sessions.OutputFrame }

func (p *idleProcess) Send(context.Context, []byte) error  { return nil }
func (p *idleProcess) Output() <-chan sessions.OutputFrame { return p.out }
func (p *idleProcess) Wait() (int, error)                  { return 0, nil }
func (p *idleProcess) Stop(context.Context) error          { close(p.out); return nil }
func (p *idleProcess) PID() int                            { return 1 }

type idleRunner struct{}

func (idleRunner) Launch(context.Context, sessions.LaunchSpec) (sessions.Process, error) {
	return &idleProcess{out: make(chan sessions.OutputFrame)}, nil
}

// The tools list reports the network the runner was given: a server confined
// to its egress hosts must not be shown to the agent as unrestricted.
func TestManagedStdioReportsItsLaunchNetwork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy *sessionegress.Policy
		egress string
		hosts  []string
	}{
		{"no policy", nil, "unrestricted", nil},
		{"egress hosts", &sessionegress.Policy{Providers: []string{"https://registry.npmjs.org", "https://mirror.test:8443"}, PublicOnly: true}, "confined", []string{"registry.npmjs.org", "mirror.test:8443"}},
		{"offline", &sessionegress.Policy{Offline: true}, "none", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := launchManagedStdio(t.Context(), idleRunner{}, sessions.LaunchSpec{NetworkPolicy: tc.policy}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			got := s.confinement()
			if got["network_egress"] != tc.egress {
				t.Fatalf("network_egress = %v, want %s", got["network_egress"], tc.egress)
			}
			if hosts, _ := got["egress_hosts"].([]string); !reflect.DeepEqual(hosts, tc.hosts) {
				t.Fatalf("egress_hosts = %v, want %v", got["egress_hosts"], tc.hosts)
			}
		})
	}
}
