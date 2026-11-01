// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Run explicitly on a host with the named runtime and a built session image.
// The init frame must reach the capture reader while the child is idle: sending
// input first would hide a missing initial stdout attachment.
func TestContainerRunner_RealInitialFrameBeforeInput(t *testing.T) {
	runtime := os.Getenv("OLIVARES_TEST_CONTAINER_RUNTIME")
	image := os.Getenv("OLIVARES_TEST_CONTAINER_IMAGE")
	if runtime == "" && image == "" {
		t.Skip("set OLIVARES_TEST_CONTAINER_RUNTIME and OLIVARES_TEST_CONTAINER_IMAGE for the real-runtime capture probe")
	}
	if runtime == "" || image == "" {
		t.Fatal("both real-runtime probe variables are required")
	}
	path, err := exec.LookPath(runtime)
	if err != nil {
		t.Fatal(err)
	}
	spec := containerSpec(t)
	spec.Dir, spec.Args, spec.Env = "/tmp", nil, nil
	script := `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"capture-before-input"}'
while IFS= read -r line; do printf '%s\n' "$line"; done
`
	if err := os.WriteFile(spec.Program, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	runtimeConfig := ContainerRuntime{Path: path, Image: image}
	if strings.HasPrefix(image, localImagePrefix) {
		state, err := runtimeConfig.CheckImage(ctx)
		if err != nil || state != RunnerImagePresent {
			t.Fatalf("image readiness before launch: state=%q err=%v", state, err)
		}
	}
	process, err := NewContainerRunner(runtimeConfig).Launch(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, end := context.WithTimeout(context.Background(), 10*time.Second)
		defer end()
		if err := process.Stop(stop); err != nil {
			t.Errorf("stop: %v", err)
		}
	}()
	timer := time.NewTimer(120 * time.Second)
	defer timer.Stop()
	var stdout, stderr []string
	for {
		select {
		case frame, ok := <-process.Output():
			if !ok {
				t.Fatalf("output closed before the init frame: stdout=%q stderr=%q", stdout, stderr)
			}
			if frame.Stream == streamStdout {
				stdout = append(stdout, string(frame.Data))
				if init, ok := parseStreamJSON(frame.Data); ok && init.isInit() && init.SessionID == "capture-before-input" {
					return
				}
			} else {
				stderr = append(stderr, string(frame.Data))
			}
		case <-timer.C:

			t.Fatalf("initial provider frame did not reach the capture reader before any input: stdout=%q stderr=%q", stdout, stderr)
		}
	}
}
