// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package carriers_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/answers/carriers"
)

// fakeTools puts one program per name in a directory of its own. Each prints its name and
// its arguments, one bracket per argument, and leaves NAME.ran beside itself, so a test can
// tell which programs ran.
func fakeTools(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n: > \"$0.ran\"\nprintf '%s' \"${0##*/}\"\nfor a in \"$@\"; do printf ' [%s]' \"$a\"; done\necho\n"
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func ran(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name+".ran"))
	return err == nil
}

// First boot runs these tools by name from PATH: the guestinfo carrier's vmware-rpctool and
// vmtoolsd (sources.go) and the base layer's cloud-init, systemctl and id
// (layer/base/adapters.go). The base layer also runs the product's configuration generator,
// by its absolute path.
var listedTools = []string{"vmware-rpctool", "vmtoolsd", "cloud-init", "systemctl", "id"}

const productBinary = "/usr/bin/olivares"

func TestExecRunner_RunsOnlyTheClosedListOfProgramsFirstBootUses(t *testing.T) {
	dir := fakeTools(t, append([]string{"sh", "useradd", "olivares"}, listedTools...)...)
	t.Setenv("PATH", dir)
	ctx := context.Background()

	// Each listed tool runs from PATH, with its arguments unchanged, one vector entry each.
	for _, name := range listedTools {
		out, err := carriers.ExecRunner(ctx, name, "info-get guestinfo.ovfEnv", "--format")
		if want := name + " [info-get guestinfo.ovfEnv] [--format]\n"; err != nil || string(out) != want {
			t.Fatalf("%s is on the closed list and must run: got %q, %v; want %q", name, out, err, want)
		}
	}

	// A context that is already done stops exec before it starts a process, so the runner's
	// answer tells an accepted program (the context's error) from a refused one, and nothing runs.
	done, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := carriers.ExecRunner(done, productBinary, "config", "generate"); !errors.Is(err, context.Canceled) {
		t.Fatalf("%s is on the closed list and must reach exec: %v", productBinary, err)
	}

	// Anything else is refused before exec, naming the program: another program on PATH, a
	// shell, a listed tool by a path, the product binary by its bare name or another path, and
	// no name. The refusal also names the whole closed list, so a program added to the list
	// fails here until this test names it too.
	refused := []string{"sh", "bash", "/bin/sh", "useradd", "olivares", filepath.Join(dir, "systemctl"), "./id",
		"/usr/local/bin/olivares", "/usr/bin/../bin/olivares", "systemctl ", ""}
	const closedList = "vmware-rpctool, vmtoolsd, cloud-init, systemctl, id, /usr/bin/olivares"
	for _, name := range refused {
		out, err := carriers.ExecRunner(done, name, "--help")
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("%q is not on the closed list and must be refused before exec: got %q, %v", name, out, err)
		}
		if want := fmt.Sprintf("first boot runs no such program: %q; it runs only %s", name, closedList); err.Error() != want {
			t.Fatalf("the refusal of %q must name it and the whole closed list:\n got %v\nwant %s", name, err, want)
		}
	}

	// With a live context too, and none of the refused programs ran.
	for _, name := range []string{"sh", "useradd", "olivares"} {
		if out, err := carriers.ExecRunner(ctx, name, "--help"); err == nil {
			t.Fatalf("%q is not on the closed list and ran: %q", name, out)
		}
		if ran(dir, name) {
			t.Fatalf("%s ran although it is not on the closed list", name)
		}
	}
}

// The guestinfo carrier reads any runner error as "no VMware tools here", so a tool missing from
// the closed list would make the carrier silently absent on VMware.
func TestExecRunner_RunsEveryProgramTheGuestInfoCarrierAsksFor(t *testing.T) {
	var names []string
	record := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		names = append(names, name)
		return nil, errors.New("no VMware tools")
	}
	if _, ok, err := (carriers.GuestInfoCarrier{Run: record}).Read(context.Background()); ok || err != nil {
		t.Fatalf("without VMware tools the carrier must be absent: %v, %v", ok, err)
	}
	if got := strings.Join(names, " "); got != "vmware-rpctool vmtoolsd" {
		t.Fatalf("the guestinfo carrier ran %q", got)
	}
	done, cancel := context.WithCancel(context.Background())
	cancel()
	for _, name := range names {
		if _, err := carriers.ExecRunner(done, name); errors.Is(err, carriers.ErrUnlistedProgram) {
			t.Fatalf("the guestinfo carrier runs %s, which ExecRunner refuses: %v", name, err)
		}
	}
	if _, err := carriers.ExecRunner(done, "sh"); !errors.Is(err, carriers.ErrUnlistedProgram) {
		t.Fatalf("a refusal must be ErrUnlistedProgram: %v", err)
	}
}
