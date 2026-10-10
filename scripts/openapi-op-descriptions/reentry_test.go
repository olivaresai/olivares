// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeclaredInterfaceGetterReentry(t *testing.T) {
	for _, variant := range []string{"callback-zero-declared-getter", "callback-zero-metadata-declared-getter"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			if err := writeFixture(root); err != nil {
				t.Fatal(err)
			}
			if err := writeCrossPackageFixture(root, variant); err != nil {
				t.Fatal(err)
			}
			// The actual fixture must compile and mount both sites before a
			// refusal counts as preserving duplicate detection.
			if err := os.Mkdir(filepath.Join(root, "witness"), 0o755); err != nil {
				t.Fatal(err)
			}
			const witness = `package main
import (
 "fmt"
 "fixture/helper"
 "fixture/modules/demo"
)
func main() {
 reg := &helper.Recorder{}
 m := &demo.Module{Enabled: true}
 m.Object = &helper.GetterCallback{Callback: func() { m.APIRoutes(reg) }}
 m.APIRoutes(reg)
 if reg.Count != 2 { panic(fmt.Sprintf("registered %d routes, want 2", reg.Count)) }
 fmt.Println("runtime registrations=2")
}
`
			if err := os.WriteFile(filepath.Join(root, "witness/main.go"), []byte(witness), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("go", "run", "-p", "2", "./witness")
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
