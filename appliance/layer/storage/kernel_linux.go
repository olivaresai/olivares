// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// LinuxKernel is the kernel of an installed appliance. It opens a device node only read-only,
// without following a link, and only when the node is a block device of the number UDisks2
// reported; it reads at most the first MiB of a disk.
type LinuxKernel struct{}

// Kernel interfaces the adapter uses.
const (
	bootIDFile = "/proc/sys/kernel/random/boot_id"
	sysBlock   = "/sys/class/block/"
	// loopGetStatus64 is LOOP_GET_STATUS64 (linux/loop.h).
	loopGetStatus64 = 0x4C05
	// blkSSZGet is BLKSSZGET (linux/fs.h), the logical block size.
	blkSSZGet = 0x1268
	firstMiB  = 1 << 20
)

// loopInfo64 is struct loop_info64 (linux/loop.h), 232 bytes.
type loopInfo64 struct {
	Device         uint64
	Inode          uint64
	Rdevice        uint64
	Offset         uint64
	SizeLimit      uint64
	Number         uint32
	EncryptType    uint32
	EncryptKeySize uint32
	Flags          uint32
	FileName       [64]byte
	CryptName      [64]byte
	EncryptKey     [32]byte
	Init           [2]uint64
}

// The layout above is the kernel's: this fails to compile if it is not 232 bytes.
var _ [232]byte = [unsafe.Sizeof(loopInfo64{})]byte{}

var errNode = errors.New("not a block device node of the reported device number")

// BootID implements Kernel.
func (LinuxKernel) BootID() (string, error) {
	data, err := readBounded(bootIDFile, 64)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if !isUUID(id) {
		return "", errors.New("the kernel's boot id is not a UUID")
	}
	return id, nil
}

// LoopStatus implements Kernel with LOOP_GET_STATUS64 and BLKSSZGET on the loop device.
func (LinuxKernel) LoopStatus(device string, number uint64) (LoopStatus, error) {
	if !loopNode(device) {
		return LoopStatus{}, errNode
	}
	f, err := openNode(device, number)
	if err != nil {
		return LoopStatus{}, err
	}
	defer f.Close()
	var info loopInfo64
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), loopGetStatus64, uintptr(unsafe.Pointer(&info))); errno != 0 {
		return LoopStatus{}, errno
	}
	var blockSize int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), blkSSZGet, uintptr(unsafe.Pointer(&blockSize))); errno != 0 {
		return LoopStatus{}, errno
	}
	if blockSize <= 0 {
		return LoopStatus{}, errors.New("the loop device reports no logical block size")
	}
	return LoopStatus{BackingDevice: info.Device, BackingInode: info.Inode, Offset: info.Offset, SizeLimit: info.SizeLimit,
		BlockSize: uint32(blockSize)}, nil
}

// FirstMiB implements Kernel.
func (LinuxKernel) FirstMiB(device string, number uint64) (string, error) {
	f, err := openNode(device, number)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, firstMiB)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Holders implements Kernel from /sys/class/block/<name>/holders, after checking that the
// sysfs entry is the device of the reported number.
func (LinuxKernel) Holders(device string, number uint64) ([]string, error) {
	if !deviceNode(device) {
		return nil, errNode
	}
	dir := sysBlock + strings.TrimPrefix(device, "/dev/")
	dev, err := readBounded(dir+"/dev", 32)
	if err != nil || !sysfsDevIs(string(dev), number) {
		return nil, errNode
	}
	holders, err := os.Open(dir + "/holders")
	if err != nil {
		return nil, err
	}
	defer holders.Close()
	names, err := holders.Readdirnames(maxHolders + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(names) > maxHolders {
		return nil, errors.New("the device has more holders than one inventory reads")
	}
	for _, name := range names {
		if !deviceNode("/dev/" + name) {
			return nil, errors.New("a holder is not a kernel block device name")
		}
	}
	return names, nil
}

// openNode opens device read-only, not following a link, and returns it only when it is a
// block device of number.
func openNode(device string, number uint64) (*os.File, error) {
	if !deviceNode(device) {
		return nil, errNode
	}
	f, err := os.OpenFile(device, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFBLK || uint64(st.Rdev) != number {
		_ = f.Close()
		return nil, errNode
	}
	return f, nil
}

// readBounded reads a small kernel file of at most limit bytes, not following a final link.
func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("the kernel file exceeds its bound")
	}
	return data, nil
}
