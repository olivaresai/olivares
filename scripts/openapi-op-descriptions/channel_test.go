// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChannelReentry(t *testing.T) {
	for _, variant := range channelVariants {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			if err := writeFixture(root); err != nil {
				t.Fatal(err)
			}
			if err := writeChannelFixture(root, variant); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(root, "witness"), 0o755); err != nil {
				t.Fatal(err)
			}
			const witness = `package main
import (
 "fmt"
 "time"
 "fixture/helper"
 "fixture/modules/demo"
)
func main() {
 reg := &helper.Recorder{}
 m := &demo.Module{Enabled: true}
 demo.Wake = make(chan struct{})
 demo.Done = make(chan struct{})
 demo.Enabled = true
 joined := make(chan struct{})
 go func() {
  helper.Coordinate(m, reg, demo.Wake, demo.Done)
  close(joined)
 }()
 m.APIRoutes(reg)
 select {
 case <-joined:
 case <-time.After(5 * time.Second): panic("channel witness did not finish")
 }
 if n := reg.Count.Load(); n != 2 { panic(fmt.Sprintf("registered %d routes, want 2", n)) }
 fmt.Println("runtime registrations=2")
}
`
			source := witness
			if strings.HasSuffix(variant, "panic-recovery") {
				source = strings.Replace(source, " m.APIRoutes(reg)", " helper.RecoverRoute(m, reg)", 1)
			}
			if err := os.WriteFile(filepath.Join(root, "witness/main.go"), []byte(source), 0o644); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "run", "-p", "2", "-race", "./witness")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GOWORK=off")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("runtime witness: %v\n%s", err, out)
			}
			t.Log(strings.TrimSpace(string(out)))
			for _, write := range []bool{false, true} {
				var out, errw bytes.Buffer
				rc := run(root, write, false, &out, &errw)
				if rc != exitCannotSee || !strings.Contains(errw.String(), "registered twice") {
					t.Errorf("write=%t: want duplicate refusal rc 2; got rc %d: %s%s", write, rc, &out, &errw)
				}
			}
		})
	}
}
