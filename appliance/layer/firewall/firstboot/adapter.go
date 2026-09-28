// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package firstboot is first boot's firewall stage: an adapter of the firewall's one owner, not
// a second writer of the host firewall.
//
// The stage runs in the first-boot unit, which can neither load nftables nor write the owner's
// state. It refuses while nftables.service is enabled or running, because that service flushes
// every table when it starts; then it restarts the owner's boot unit, olivares-firewall.service,
// which loads the confirmed policy or confirms the first one from the console selection first
// boot published; and it completes only when the owner's measurement of this boot admits the
// product's ports and admits 9443 exactly on the management interfaces the answers select, and
// nowhere when they select none. It runs systemctl with fixed arguments and reads two files: the
// owner's measurement and the kernel's boot id.
package firstboot

import (
	"context"
	"os"
	"slices"
	"strings"
	"syscall"

	"github.com/olivaresai/olivares/appliance/answers/carriers"
	"github.com/olivaresai/olivares/appliance/layer/base"
	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
)

// OwnerUnit is the firewall owner's boot unit, which the stage restarts.
const OwnerUnit = "olivares-firewall.service"

// secondOwner is the distribution's firewall service, which must stay disabled and stopped.
const secondOwner = "nftables.service"

// Adapter is first boot's firewall stage.
type Adapter struct {
	// Run runs systemctl with fixed arguments.
	Run carriers.Runner
	// Measurement is the owner's measurement, firewall.MeasurementFile on an appliance.
	Measurement string
	// BootID is the kernel's report of this boot's identity.
	BootID string
}

// New returns the stage an installed appliance runs.
func New(run carriers.Runner) Adapter {
	return Adapter{Run: run, Measurement: firewall.MeasurementFile, BootID: firewall.BootIDFile}
}

// fileOwner returns the uid that owns info's file, for the measurement reader.
var fileOwner = func(info os.FileInfo) (uint32, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

// Apply refuses while a second firewall owner is enabled or running, asks the owner's boot unit
// to load, and records the measured policy's digest.
func (a Adapter) Apply(ctx context.Context, in base.Input) (base.Effect, error) {
	if err := a.noSecondOwner(ctx); err != nil {
		return "", err
	}
	if _, err := a.Run(ctx, "systemctl", "restart", OwnerUnit); err != nil {
		return "", base.Refuse("the firewall owner (olivares-firewall.service) did not load a confirmed policy; no service is exposed beyond loopback")
	}
	return a.measured(in)
}

// Verify compares a recorded stage with the host: still no second owner, and the owner's
// measurement of this boot is the recorded policy and admits what the answers select.
func (a Adapter) Verify(ctx context.Context, in base.Input, recorded base.Effect) error {
	if err := a.noSecondOwner(ctx); err != nil {
		return err
	}
	got, err := a.measured(in)
	if err != nil {
		return err
	}
	if got != recorded {
		return base.Refuse("the measured firewall policy is not the one first boot recorded")
	}
	return nil
}

// noSecondOwner refuses while nftables.service is anything but disabled, masked or absent, or
// is running, or either state cannot be read.
func (a Adapter) noSecondOwner(ctx context.Context) error {
	out, _ := a.Run(ctx, "systemctl", "is-enabled", secondOwner)
	switch firstLine(out) {
	case "disabled", "masked", "masked-runtime", "not-found":
	case "":
		return base.Refuse("the enablement of nftables.service could not be read; its start would flush the firewall owner's table")
	default:
		return base.Refuse("nftables.service is enabled: a second firewall owner flushes every table when it starts; disable or mask it")
	}
	out, _ = a.Run(ctx, "systemctl", "is-active", secondOwner)
	switch firstLine(out) {
	case "inactive", "failed":
		return nil
	case "":
		return base.Refuse("whether nftables.service is running could not be read; a second firewall owner flushes every table")
	}
	return base.Refuse("nftables.service is running: a second firewall owner flushes every table; stop and disable it")
}

// measured reads the owner's measurement and judges it against the answers.
func (a Adapter) measured(in base.Input) (base.Effect, error) {
	m, reason := firewall.MeasurementReader{Path: a.Measurement, BootID: a.BootID, Owner: fileOwner}.Read()
	if reason != "" {
		return "", base.Refuse("the firewall is unmeasured: " + reason + "; no service is exposed beyond loopback")
	}
	if m.InputPolicy != "drop" {
		return "", base.Refuse("the measured firewall policy does not drop what no row admits")
	}
	for _, port := range base.ProductPorts {
		if !slices.ContainsFunc(m.Rows, func(r policy.MeasuredRow) bool {
			return r.Port == port && slices.Equal(r.Interfaces, []string{policy.EveryInterface})
		}) {
			return "", base.Refuse("the measured firewall policy does not admit the product's ports")
		}
	}
	var console []string
	for _, r := range m.Rows {
		if r.Port == policy.ConsolePort {
			console = append(console, r.Interfaces...)
		}
	}
	slices.Sort(console)
	if !slices.Equal(slices.Compact(console), selected(in.Answers)) {
		return "", base.Refuse("the measured firewall policy does not admit 9443 exactly on the selected management interfaces")
	}
	return base.Effect("measured firewall policy " + m.PolicyDigest), nil
}

// selected returns the interfaces the answers expose the console on, sorted: the management
// interfaces when the console is enabled and listens on them, and none otherwise.
func selected(a base.Answers) []string {
	if a.PortalEnabled == nil || !*a.PortalEnabled || a.PortalListen == nil || *a.PortalListen != policy.ListenManagement {
		return nil
	}
	names := slices.Clone(a.ManagementInterfaces)
	slices.Sort(names)
	return names
}

// firstLine returns the first line of a command's output, trimmed.
func firstLine(out []byte) string {
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line)
}
