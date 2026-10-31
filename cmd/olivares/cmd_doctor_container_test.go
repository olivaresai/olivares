// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions"
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
		image  sessions.RunnerImageState
		askErr error
		status string
		detail string
		fix    string // the remediation must contain it
		notFix string // and must not contain this
	}{
		{"podman found", nil, []string{"podman", "docker"}, sessions.RunnerImageUnknown, nil, "pass", "podman at /usr/bin/podman", "", ""},
		{"docker found", nil, []string{"docker"}, sessions.RunnerImageUnknown, nil, "pass", "docker at /usr/bin/docker", "", ""},
		{"none installed is optional, not a fault", nil, nil, sessions.RunnerImageUnknown, nil, "pass", "isolation=container is not available", "", ""},
		{"turned off", map[string]string{"OLIVARES_SESSION_CONTAINER_RUNTIME": "off"}, []string{"podman"}, sessions.RunnerImageUnknown, nil, "pass", "turned off", "", ""},
		{"a wrong setting is a warning that names it", map[string]string{"OLIVARES_SESSION_CONTAINER_RUNTIME": "lxc"}, []string{"podman"}, sessions.RunnerImageUnknown, nil, "warn", "OLIVARES_SESSION_CONTAINER_RUNTIME", "unset it", ""},
		{"the session image is on the host", nil, []string{"podman"}, sessions.RunnerImagePresent, nil, "pass", "localhost/olivares-session:latest is on this host", "", ""},
		{"a missing default image is a warning with its build command", nil, []string{"podman"}, sessions.RunnerImageMissing, nil, "warn",
			"localhost/olivares-session:latest is not on this host", "podman build -f /usr/share/olivares/session-image/Containerfile -t localhost/olivares-session:latest /usr/share/olivares/session-image", ""},
		{"a missing image of the operator's own is not given the default's recipe", map[string]string{"OLIVARES_SESSION_CONTAINER_IMAGE": "localhost/mine:1"}, []string{"docker"}, sessions.RunnerImageMissing, nil, "warn",
			"localhost/mine:1 is not on this host", "OLIVARES_SESSION_CONTAINER_IMAGE", "session-image"},
		{"docker names its own binary in the build command", nil, []string{"docker"}, sessions.RunnerImageMissing, nil, "warn",
			"localhost/olivares-session:latest is not on this host", "docker build -f /usr/share/olivares/session-image/Containerfile", ""},
		{"a runtime that was asked and failed is a warning with the reason, not a pass", nil, []string{"podman"}, sessions.RunnerImageUnknown, errors.New("podman did not answer within 5s"), "warn",
			"podman did not answer within 5s", "podman images", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deps := doctorDeps{getenv: env(c.env), lookPath: have(c.have...),
				// The package ships the recipe for every case in this table.
				stat: func(string) (os.FileInfo, error) { return nil, nil },
				containerImage: func(context.Context, sessions.ContainerRuntime) (sessions.RunnerImageState, error) {
					return c.image, c.askErr
				}}
			got := doctorContainerRuntime(context.Background(), deps)
			if got.Name != "container-runtime" || got.Required || got.Status != c.status || !strings.Contains(got.Detail, c.detail) {
				t.Fatalf("got %+v, want %s containing %q, optional", got, c.status, c.detail)
			}
			if (c.fix == "") != (got.Remediation == "") || !strings.Contains(got.Remediation, c.fix) || (c.notFix != "" && strings.Contains(got.Remediation, c.notFix)) {
				t.Fatalf("remediation %q: want it to contain %q and not %q", got.Remediation, c.fix, c.notFix)
			}
		})
	}
}

// A binary or source install has no packaged recipe: doctor names the source command
// instead of a path that does not exist on that host.
func TestDoctorContainerRuntimeNamesTheSourceRecipeWithoutThePackage(t *testing.T) {
	got := doctorContainerRuntime(context.Background(), doctorDeps{
		getenv:   func(string) string { return "" },
		lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		stat:     func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		containerImage: func(context.Context, sessions.ContainerRuntime) (sessions.RunnerImageState, error) {
			return sessions.RunnerImageMissing, nil
		},
	})
	if got.Status != "warn" || !strings.Contains(got.Remediation, "packaging/container/session/Containerfile") ||
		strings.Contains(got.Remediation, "/usr/share/olivares/session-image") {
		t.Fatalf("remediation = %q, want the source recipe and not the packaged path", got.Remediation)
	}
}

// With nothing wired to ask, doctor still names the runtime and does not claim to know the image.
func TestDoctorContainerRuntimeWithoutAnImageQuery(t *testing.T) {
	got := doctorContainerRuntime(context.Background(), doctorDeps{
		getenv:   func(string) string { return "" },
		lookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
	})
	if got.Status != "pass" || !strings.Contains(got.Detail, "looked up, not started") || got.Remediation != "" {
		t.Fatalf("got %+v", got)
	}
}

// The real wiring: doctor asks the runtime it found, through the same check the engine uses.
func TestDefaultDoctorDepsAskTheRuntimeAboutTheImage(t *testing.T) {
	dir := t.TempDir()
	// A podman that says "absent" the way podman does: exit 1, nothing printed.
	if err := os.WriteFile(filepath.Join(dir, "podman"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv(sessions.ContainerRuntimeEnv, "")
	t.Setenv(sessions.ContainerImageEnv, "localhost/olivares-session:doctor-wiring")
	got := doctorContainerRuntime(context.Background(), defaultDoctorDeps())
	if got.Status != "warn" || !strings.Contains(got.Detail, "is not on this host") {
		t.Fatalf("got %+v, want a warning that the image is not on this host", got)
	}
}
