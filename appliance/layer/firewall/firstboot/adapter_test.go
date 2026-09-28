// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firstboot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/base"
	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
)

const testBoot = "8d8a1f0c-54c0-4b3e-9d6a-2a1f3b4c5d6e"

// kernel keeps the last ruleset loaded and reports it back as nft would.
type kernel struct{ ruleset []byte }

func (k *kernel) Load(_ context.Context, ruleset []byte) error {
	k.ruleset = slices.Clone(ruleset)
	return nil
}

func (k *kernel) Table(context.Context) (firewall.Table, error) {
	if k.ruleset == nil {
		return firewall.Table{}, errors.New("no such table")
	}
	var table firewall.Table
	chain := ""
	for _, line := range strings.Split(string(k.ruleset), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, `comment "`):
			table.Comment = strings.TrimSuffix(strings.TrimPrefix(line, `comment "`), `"`)
		case line == "chain input {", line == "chain forward {":
			chain = strings.Fields(line)[1]
		case line == "}":
			chain = ""
		case chain != "" && strings.HasSuffix(line, "policy drop;"):
			if chain == "input" {
				table.InputPolicy = "drop"
			} else {
				table.ForwardPolicy = "drop"
			}
		case chain == "input" && line != "":
			table.InputRules++
		case chain == "forward" && line != "":
			table.ForwardRules++
		}
	}
	return table, nil
}

type clock struct{ boot string }

func (c clock) Now() (string, time.Duration, error) { return c.boot, time.Minute, nil }

type lock struct{}

func (lock) Acquire() (func(), error) { return func() {}, nil }

// host is the first-boot unit's view of the host: systemctl, which it runs, and the firewall
// owner's boot unit, which systemctl starts. The adapter reaches the owner only through it.
type host struct {
	t            *testing.T
	calls        []string
	enabled      string
	active       string
	restartFails bool
	// selection is what the owner's boot unit reads from the published console selection.
	selection firewall.Selection
	kernel    *kernel
	owner     *firewall.Owner
	adapter   Adapter
}

func newHost(t *testing.T, selection firewall.Selection) *host {
	t.Helper()
	dir := t.TempDir()
	h := &host{t: t, enabled: "disabled", active: "inactive", selection: selection, kernel: &kernel{}}
	h.owner = &firewall.Owner{StateDir: filepath.Join(dir, "state"), RunDir: filepath.Join(dir, "run"), Kernel: h.kernel,
		Lock: lock{}, Clock: clock{boot: testBoot}, Wall: func() time.Time { return time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC) }}
	bootID := filepath.Join(dir, "boot_id")
	if err := os.WriteFile(bootID, []byte(testBoot+"\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	h.adapter = Adapter{Run: h.run, Measurement: filepath.Join(h.owner.RunDir, "measured.json"), BootID: bootID}
	production := fileOwner
	fileOwner = func(os.FileInfo) (uint32, bool) { return 0, true }
	t.Cleanup(func() { fileOwner = production })
	return h
}

func (h *host) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	h.calls = append(h.calls, call)
	switch call {
	case "systemctl is-enabled nftables.service":
		if h.enabled == "" {
			return nil, errors.New("exit status 1")
		}
		if h.enabled == "disabled" || h.enabled == "masked" || h.enabled == "not-found" {
			return []byte(h.enabled + "\n"), errors.New("exit status 1")
		}
		return []byte(h.enabled + "\n"), nil
	case "systemctl is-active nftables.service":
		return []byte(h.active + "\n"), nil
	case "systemctl restart olivares-firewall.service":
		if h.restartFails {
			return nil, errors.New("exit status 1")
		}
		// The owner's boot unit loads the confirmed policy, or confirms the first one from the
		// published selection, and publishes its measurement.
		_, err := h.owner.Install(ctx, firewall.Initial(h.selection, 22, []string{"eth0"}))
		return nil, err
	}
	h.t.Errorf("the adapter ran %q", call)
	return nil, errors.New("unexpected command")
}

// input is first boot's validated answers with the console's selection.
func input(enabled bool, listen string, interfaces ...string) base.Input {
	return base.Input{Source: "file", Digest: strings.Repeat("ab", 32),
		Answers: base.Answers{PortalEnabled: &enabled, PortalListen: &listen, ManagementInterfaces: interfaces}}
}

// refused reports whether err is first boot's refusal and its reason.
func refused(err error) (string, bool) {
	var outcome *base.Outcome
	if errors.As(err, &outcome) && outcome.State == base.Refused {
		return outcome.Reason, true
	}
	return "", false
}

var (
	isEnabled = "systemctl is-enabled nftables.service"
	isActive  = "systemctl is-active nftables.service"
	restart   = "systemctl restart olivares-firewall.service"
)

func TestFirstBoot_FirewallAdapterAppliesAndMeasuresThroughTheOwner(t *testing.T) {
	ctx := context.Background()
	selection := firewall.Selection{Enabled: true, Listen: policy.ListenManagement, ManagementInterfaces: []string{"eth0"}}
	h := newHost(t, selection)
	in := input(true, "management", "eth0")
	effect, err := h.adapter.Apply(ctx, in)
	if err != nil {
		t.Fatalf("the firewall stage did not complete: %v", err)
	}
	// Through the owner: the adapter only asks systemctl, in this order, and never runs nft.
	if want := []string{isEnabled, isActive, restart}; !slices.Equal(h.calls, want) {
		t.Fatalf("the adapter ran %q, want %q", h.calls, want)
	}
	installed := policy.Digest(firewall.Initial(selection, 22, []string{"eth0"}))
	table, err := h.kernel.Table(ctx)
	if err != nil || table.Comment != policy.TableComment+installed {
		t.Fatalf("the kernel holds %q (%v), want the owner's first confirmed policy", table.Comment, err)
	}
	if confirmed, ok, err := h.owner.Confirmed(); err != nil || !ok || policy.Digest(confirmed) != installed {
		t.Errorf("the owner did not confirm its first policy: %v %v", ok, err)
	}
	if want := base.Effect("measured firewall policy " + installed); effect != want {
		t.Errorf("the recorded effect is %q, want %q", effect, want)
	}
	if err := h.adapter.Verify(ctx, in, effect); err != nil {
		t.Errorf("a restarted first boot does not verify the same measurement: %v", err)
	}
	if err := h.adapter.Verify(ctx, in, base.Effect("measured firewall policy sha256:"+strings.Repeat("0", 64))); err == nil {
		t.Error("a first boot record of another policy was verified")
	}

	// The console off: the owner's policy has no 9443 row, and the stage completes.
	off := newHost(t, firewall.Selection{Enabled: false, Listen: policy.ListenManagement, ManagementInterfaces: []string{"eth0"}})
	if _, err := off.adapter.Apply(ctx, input(false, "management", "eth0")); err != nil {
		t.Errorf("a disabled console: %v", err)
	}

	// Refused, each with no effect recorded: the measurement admits 9443 elsewhere than the
	// answers select, the owner could not load, the measurement is absent or of another boot.
	for name, c := range map[string]struct {
		selection firewall.Selection
		in        base.Input
		prepare   func(*host)
	}{
		"9443 on another interface":     {selection, input(true, "management", "eth1"), nil},
		"9443 while the console is off": {selection, input(false, "management", "eth0"), nil},
		"9443 on fewer interfaces":      {selection, input(true, "management", "eth0", "eth1"), nil},
		"the owner could not load":      {selection, in, func(h *host) { h.restartFails = true }},
		"no published measurement": {selection, in, func(h *host) {
			h.adapter.Measurement = filepath.Join(t.TempDir(), "measured.json")
		}},
		"a measurement of another boot": {selection, in, func(h *host) {
			// Replace our read-only fixture through its owned parent; this also works as non-root.
			if err := os.Remove(h.adapter.BootID); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(h.adapter.BootID, []byte("1f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b\n"), 0o444); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		h := newHost(t, c.selection)
		if c.prepare != nil {
			c.prepare(h)
		}
		effect, err := h.adapter.Apply(ctx, c.in)
		if _, ok := refused(err); !ok || effect != "" {
			t.Errorf("%s: effect %q err %v, want a refusal", name, effect, err)
		}
	}
}

func TestFirstBoot_RefusesReadyWhenNftablesServiceIsEnabled(t *testing.T) {
	ctx := context.Background()
	selection := firewall.Selection{Enabled: true, Listen: policy.ListenManagement, ManagementInterfaces: []string{"eth0"}}
	in := input(true, "management", "eth0")

	// nftables.service would flush every table, the owner's included, when it starts: while it
	// is enabled, or its enablement cannot be read, first boot refuses before asking the owner.
	for _, state := range []string{"enabled", "enabled-runtime", "alias", "linked", "linked-runtime", "static", "indirect", "generated", "transient", ""} {
		h := newHost(t, selection)
		h.enabled = state
		effect, err := h.adapter.Apply(ctx, in)
		reason, ok := refused(err)
		if !ok || effect != "" || !strings.Contains(reason, "nftables.service") {
			t.Errorf("nftables.service %q: effect %q err %v, want a refusal naming nftables.service", state, effect, err)
		}
		if !slices.Equal(h.calls, []string{isEnabled}) {
			t.Errorf("nftables.service %q: the adapter went on to %q", state, h.calls)
		}
		if h.kernel.ruleset != nil {
			t.Errorf("nftables.service %q: a table was loaded", state)
		}
	}
	// Running while disabled is refused too.
	for _, state := range []string{"active", "activating", "reloading", "refreshing"} {
		h := newHost(t, selection)
		h.active = state
		if _, err := h.adapter.Apply(ctx, in); err == nil || !slices.Equal(h.calls, []string{isEnabled, isActive}) {
			t.Errorf("nftables.service %s: err %v calls %q, want a refusal before the owner is asked", state, err, h.calls)
		}
	}
	// Disabled, masked or absent, first boot goes on to the owner.
	for _, state := range []string{"disabled", "masked", "not-found"} {
		h := newHost(t, selection)
		h.enabled = state
		if _, err := h.adapter.Apply(ctx, in); err != nil || !slices.Contains(h.calls, restart) {
			t.Errorf("nftables.service %s: err %v calls %q", state, err, h.calls)
		}
	}
	// A first boot that restarts after nftables.service was enabled does not verify either.
	h := newHost(t, selection)
	effect, err := h.adapter.Apply(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	h.enabled = "enabled"
	if reason, ok := refused(h.adapter.Verify(ctx, in, effect)); !ok || !strings.Contains(reason, "nftables.service") {
		t.Errorf("verify with nftables.service enabled: %q %v", reason, ok)
	}
}
