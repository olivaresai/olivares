// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"strings"
	"testing"
)

// The doctor line says which container runtime sessions would use, or why none is
// used. It never runs the runtime: finding it is a lookup.
func TestDoctorContainerRuntime(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	have := func(names ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, n := range names {
				if n == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", errors.New("absent")
		}
	}
	cases := []struct {
		name   string
		env    map[string]string
		have   []string
		status string
		detail string
	}{
		{"podman found", nil, []string{"podman", "docker"}, "pass", "podman at /usr/bin/podman"},
		{"docker found", nil, []string{"docker"}, "pass", "docker at /usr/bin/docker"},
		{"none installed is optional, not a fault", nil, nil, "pass", "isolation=container is not available"},
		{"turned off", map[string]string{"OLIVARES_SESSION_CONTAINER_RUNTIME": "off"}, []string{"podman"}, "pass", "turned off"},
		{"a wrong setting is a warning that names it", map[string]string{"OLIVARES_SESSION_CONTAINER_RUNTIME": "lxc"}, []string{"podman"}, "warn", "OLIVARES_SESSION_CONTAINER_RUNTIME"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := doctorContainerRuntime(doctorDeps{getenv: env(c.env), lookPath: have(c.have...)})
			if got.Name != "container-runtime" || got.Required || got.Status != c.status || !strings.Contains(got.Detail, c.detail) {
				t.Fatalf("got %+v, want %s containing %q, optional", got, c.status, c.detail)
			}
		})
	}
}
