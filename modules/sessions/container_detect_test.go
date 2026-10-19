// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDetectContainerRuntime(t *testing.T) {
	installed := func(names ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, n := range names {
				if n == name {
					return "/usr/bin/" + name, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

	cases := []struct {
		name    string
		env     map[string]string
		have    []string
		path    string
		image   string
		wantErr error
		anyErr  bool
	}{
		{name: "podman is preferred when both are installed", have: []string{"docker", "podman"}, path: "/usr/bin/podman", image: DefaultContainerImage},
		{name: "docker when podman is absent", have: []string{"docker"}, path: "/usr/bin/docker", image: DefaultContainerImage},
		{name: "auto means the same as unset", env: map[string]string{ContainerRuntimeEnv: "auto"}, have: []string{"podman"}, path: "/usr/bin/podman", image: DefaultContainerImage},
		{name: "docker can be chosen over podman", env: map[string]string{ContainerRuntimeEnv: "docker"}, have: []string{"podman", "docker"}, path: "/usr/bin/docker", image: DefaultContainerImage},
		{name: "a chosen image", env: map[string]string{ContainerImageEnv: "registry.example/team/session:7"}, have: []string{"podman"}, path: "/usr/bin/podman", image: "registry.example/team/session:7"},
		{name: "none installed is the plain absence, not a failure", have: nil, wantErr: ErrNoContainerRuntime},
		{name: "off", env: map[string]string{ContainerRuntimeEnv: "off"}, have: []string{"podman"}, wantErr: ErrContainerRuntimeOff},
		{name: "a runtime named but not installed is a configuration error", env: map[string]string{ContainerRuntimeEnv: "docker"}, have: []string{"podman"}, anyErr: true},
		{name: "an unknown runtime", env: map[string]string{ContainerRuntimeEnv: "lxc"}, have: []string{"podman"}, anyErr: true},
		{name: "an image that would be read as an option", env: map[string]string{ContainerImageEnv: "--privileged"}, have: []string{"podman"}, anyErr: true},
		{name: "an image with a space", env: map[string]string{ContainerImageEnv: "a b"}, have: []string{"podman"}, anyErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt, err := DetectContainerRuntime(env(c.env), installed(c.have...))
			switch {
			case c.wantErr != nil:
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
			case c.anyErr:
				if err == nil || errors.Is(err, ErrNoContainerRuntime) || errors.Is(err, ErrContainerRuntimeOff) {
					t.Fatalf("err = %v, want a configuration error the operator must read", err)
				}
			default:
				if err != nil || rt.Path != c.path || rt.Image != c.image {
					t.Fatalf("got %+v, %v; want %s with image %s", rt, err, c.path, c.image)
				}
			}
		})
	}
}

func TestSELinuxEnforcingReadsTheKernelsAnswer(t *testing.T) {
	dir := t.TempDir()
	for content, want := range map[string]bool{"1": true, "0": false, "": false} {
		path := filepath.Join(dir, "enforce")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := selinuxEnforcingAt(path); got != want {
			t.Errorf("enforce=%q: got %v, want %v", content, got, want)
		}
	}
	if selinuxEnforcingAt(filepath.Join(dir, "missing")) {
		t.Error("a host without SELinux reported enforcing")
	}
}
