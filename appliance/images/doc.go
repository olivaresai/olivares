// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package images is the appliance image recipe: one KIWI NG description with a profile per
// edition and architecture (kiwi/), the assembly of the formats built from the disk it
// produces (formats/), and the tests that hold both to their shape.
//
// The package carries no runtime code on purpose. A recipe is data - a description, a package
// list, a pinned builder - and the programs that read it are KIWI NG, the assembly scripts and
// the Taskfile. What Go adds here is the gate: the tests read the same files the build reads
// and refuse a recipe that would produce the wrong image, which is the only part of an image
// build that can be checked without a container runtime, 20 GB of disk and /dev/kvm.
//
// The boot side of the same recipe is appliance/test (the QEMU boot battery and the unattended
// first-boot probe), and the hosted runner that builds and boots it is
// .github/workflows/appliance-image.yml.
package images
