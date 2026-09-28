// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build appliance_host_hosted && linux

// These tests run only in the privileged host container of the hosted appliance job, as root,
// with UDisks2 and its LVM2 module installed and udisksd on the system bus. They attach loop
// devices to files in the test's own temporary directory and change nothing else.

package storage_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/storage"
)

// attach binds a loop device to file at offset and returns its node; the device is detached
// when the test ends.
func attach(t *testing.T, file string, offset int) string {
	t.Helper()
	out, err := exec.Command("/usr/sbin/losetup", "--find", "--show", "--offset", strconv.Itoa(offset), file).Output()
	if err != nil {
		t.Fatalf("losetup: %v", err)
	}
	device := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("/usr/sbin/losetup", "--detach", device).Run() })
	return device
}

// hostInventory reads the host until UDisks2 reports device with a loop identity, for at most
// ten seconds.
func hostInventory(t *testing.T, device string) (storage.Inventory, storage.Disk) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		bus, err := storage.DialSystemBus(ctx)
		if err != nil {
			cancel()
			t.Fatalf("system bus: %v", err)
		}
		inv, err := storage.Read(ctx, bus, storage.LinuxKernel{})
		_ = bus.Close()
		cancel()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		for _, d := range inv.Disks {
			if d.Device == device && d.Identity.Kind == storage.IdentityLoop {
				return inv, d
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("UDisks2 did not report %s with a loop identity: %+v", device, inv.Disks)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func TestHosted_LoopDeviceIsNamedByItsKernelFileAndRefusedWhenReattached(t *testing.T) {
	file := t.TempDir() + "/scratch.img"
	f, err := os.OpenFile(file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	var st syscall.Stat_t
	if err := syscall.Stat(file, &st); err != nil {
		t.Fatal(err)
	}

	device := attach(t, file, 0)
	inv, d := hostInventory(t, device)
	loop := d.Identity.Loop
	if loop == nil || loop.BackingInode != st.Ino || loop.BackingDevice != uint64(st.Dev) || loop.Offset != 0 || loop.BlockSize == 0 {
		t.Fatalf("identity %+v, want the kernel's device %#x and inode %d at offset 0", d.Identity, st.Dev, st.Ino)
	}
	if d.Identity.BootID != inv.BootID || d.Target != "drive:"+d.Identity.Digest || d.BackingFile != file {
		t.Fatalf("disk %+v", d)
	}
	if inv.LVM.State != storage.LVMRead {
		t.Fatalf("the LVM2 module was not enabled: %+v", inv.LVM)
	}
	// The container has e2fsprogs, so the manager's CanFormat answer for ext4 reaches the
	// inventory through the D-Bus adapter.
	if !slices.ContainsFunc(d.Operations, func(o storage.Operation) bool { return o.Name == storage.OperationFormat && o.Type == "ext4" }) {
		t.Fatalf("the host's CanFormat for ext4 did not reach the inventory: %+v", d.Operations)
	}
	if again, err := storage.Reresolve(d.Identity, inv); err != nil || again.Device != device {
		t.Fatalf("the unchanged device did not resolve: %+v %v", again, err)
	}

	if err := exec.Command("/usr/sbin/losetup", "--detach", device).Run(); err != nil {
		t.Fatal(err)
	}
	moved := attach(t, file, 1<<20)
	after, _ := hostInventory(t, moved)
	_, err = storage.Reresolve(d.Identity, after)
	var refusal *storage.Refusal
	if !errors.As(err, &refusal) || refusal.Code != storage.CodeIdentityChanged {
		t.Fatalf("the file reattached at another offset resolved: %v", err)
	}
}
