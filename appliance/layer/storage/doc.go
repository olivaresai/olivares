// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package storage is the read-only storage inventory of an appliance host: its disks,
// partitions, filesystems, swap and LVM, as UDisks2 and the kernel report them.
//
// Read builds one Inventory from two sources, UDisks2 (Bus) and the kernel (Kernel). It enables
// UDisks2's LVM2 module before it reads anything, so that LVM facts are attached to the block
// objects it reads. It marks the system disk (the disk that holds /, /boot, /boot/efi, a
// /var/lib/olivares volume or active swap, through partitions, encryption and LVM) and every
// consumer of each disk and partition, and it lists the operations the host reports it can
// perform for each filesystem type and mode, and no others.
//
// Each disk has an Identity: the first non-blank of the drive's WWN, serial and Id and the block
// Id, with its size and partition UUIDs. A disk with all four blank is identified by its
// /dev/disk/by-path link, size, partition table type, partition UUIDs and the SHA-256 of its first
// MiB, bound to the boot id, so it is valid in that boot only. A loop device is identified by what
// the kernel reports for it (backing device and inode, offset, size limit, block size) and the
// boot id; the backing file name UDisks2 shows is display only. A disk whose identity is missing
// or shared by another disk has no target and is offered no operation. Reresolve re-resolves a
// captured identity against a new inventory and refuses any difference.
//
// Nothing in this package changes a disk: it has no path, device or command from a caller.
package storage
