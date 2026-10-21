// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Which container runtime a server has, and which one Olivares uses.
//
// Nothing is configured by default: the engine looks for podman (the platform's own
// on AlmaLinux and RHEL, rootless and SELinux-aware) and then docker, and uses the first
// it finds. Docker CE is a choice, not a different code path: the same arguments run on
// both. Two settings exist for the cases a search cannot decide.
const (
	// ContainerRuntimeEnv selects the runtime: auto (the default), podman, docker or off.
	ContainerRuntimeEnv = "OLIVARES_SESSION_CONTAINER_RUNTIME"
	// ContainerImageEnv names the image container sessions run in.
	ContainerImageEnv = "OLIVARES_SESSION_CONTAINER_IMAGE"
	// DefaultContainerImage is the name an Olivares Server image is meant to carry its
	// session image under, so a container session needs nothing typed there. Nothing in
	// this repository builds it: on any other host, name an image of your own.
	DefaultContainerImage = "localhost/olivares-session:latest"
)

var (
	// ErrNoContainerRuntime means neither podman nor docker is installed: container
	// isolation is then simply unavailable, as it always was.
	ErrNoContainerRuntime = errors.New("no container runtime is installed")
	// ErrContainerRuntimeOff means the operator turned container isolation off.
	ErrContainerRuntimeOff = errors.New("container isolation is turned off")
)

// selinuxEnforcePath is where the kernel says whether SELinux enforces.
var selinuxEnforcePath = "/sys/fs/selinux/enforce"

// DetectContainerRuntime resolves the runtime and the image from the two settings.
// ErrNoContainerRuntime and ErrContainerRuntimeOff are expected answers; any other
// error is a setting the operator must fix and says which.
func DetectContainerRuntime(getenv func(string) string, lookPath func(string) (string, error)) (ContainerRuntime, error) {
	image := strings.TrimSpace(getenv(ContainerImageEnv))
	if image == "" {
		image = DefaultContainerImage
	}
	// An image that starts with '-' would be read by the CLI as an option.
	if strings.HasPrefix(image, "-") || strings.ContainsAny(image, " \t\r\n\x00") {
		return ContainerRuntime{}, fmt.Errorf("%s is %q, which is not an image name", ContainerImageEnv, image)
	}
	choice := strings.ToLower(strings.TrimSpace(getenv(ContainerRuntimeEnv)))
	var candidates []string
	switch choice {
	case "", "auto":
		candidates = []string{"podman", "docker"}
	case "podman", "docker":
		candidates = []string{choice}
	case "off":
		return ContainerRuntime{}, ErrContainerRuntimeOff
	default:
		return ContainerRuntime{}, fmt.Errorf("%s is %q; use auto, podman, docker or off", ContainerRuntimeEnv, choice)
	}
	for _, name := range candidates {
		if path, err := lookPath(name); err == nil {
			return ContainerRuntime{Path: path, Image: image, SELinux: selinuxEnforcingAt(selinuxEnforcePath)}, nil
		}
	}
	if choice == "podman" || choice == "docker" {
		return ContainerRuntime{}, fmt.Errorf("%s is %q but it is not installed", ContainerRuntimeEnv, choice)
	}
	return ContainerRuntime{}, ErrNoContainerRuntime
}

// selinuxEnforcingAt reads the kernel's own answer; a host without SELinux has no file.
func selinuxEnforcingAt(path string) bool {
	raw, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(raw)) == "1"
}
