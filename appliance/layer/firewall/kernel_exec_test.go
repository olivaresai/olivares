// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
)

// A canceled context also prevents a host process if a guard regresses. Requiring
// the specific refusal distinguishes the guard from the later command failure.
func TestExec_RefusesForeignCommandsBeforeSpawning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []struct {
		name string
		args []string
	}{
		{"/bin/sh", []string{"-c", "exit 0"}},
		{"sshd", []string{"-T"}},
		{"/other/sshd", []string{"-T"}},
		{"/usr/sbin/sshd", nil},
		{"/usr/sbin/sshd", []string{"-D"}},
		{"/usr/sbin/sshd", []string{"-T", "-f", "/tmp/sshd_config"}},
	} {
		if _, err := firewall.Exec(ctx, c.name, c.args...); err == nil || err.Error() != "not the SSH configuration probe's argument vector" {
			t.Errorf("%q %q: got %v, want refusal before spawning", c.name, c.args, err)
		}
	}
	if _, err := firewall.Exec(ctx, "/usr/sbin/sshd", "-T"); !errors.Is(err, context.Canceled) {
		t.Errorf("the fixed SSH probe must reach the canceled context: %v", err)
	}
}

func TestNFT_RefusesMutatedArgumentVectorsBeforeSpawning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	originalLoad, originalList := firewall.LoadArgs, firewall.ListArgs
	t.Cleanup(func() { firewall.LoadArgs, firewall.ListArgs = originalLoad, originalList })
	for _, c := range []struct {
		name string
		args []string
	}{
		{"empty", nil},
		{"file", []string{"-f", "/tmp/rules.nft"}},
		{"flush", []string{"flush", "ruleset"}},
		{"load", []string{"-f", "-"}},
		{"list", []string{"-j", "list", "table", "inet", "olivares"}},
		{"foreign table", []string{"-j", "list", "table", "inet", "filter"}},
	} {
		// Both exported globals change together: neither can serve as the other's allowlist.
		firewall.LoadArgs, firewall.ListArgs = slices.Clone(c.args), slices.Clone(c.args)
		err := (firewall.NFT{}).Load(ctx, nil)
		if c.name == "load" {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("valid load: %v", err)
			}
		} else if err == nil || err.Error() != "not the nft load argument vector" {
			t.Errorf("load %s: got %v, want refusal before spawning", c.name, err)
		}
		_, err = (firewall.NFT{}).Table(ctx)
		if c.name == "list" {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("valid list: %v", err)
			}
		} else if err == nil || err.Error() != "not the nft table argument vector" {
			t.Errorf("table %s: got %v, want refusal before spawning", c.name, err)
		}
	}
}

func TestNFT_RequiresAnAbsoluteConfiguredProgram(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, program := range []string{"nft", "./nft", "../sbin/nft"} {
		n := firewall.NFT{Program: program}
		if err := n.Load(ctx, nil); err == nil || err.Error() != "the program must be configured by absolute path" {
			t.Errorf("load %q: got %v, want path refusal", program, err)
		}
		if _, err := n.Table(ctx); err == nil || err.Error() != "the program must be configured by absolute path" {
			t.Errorf("table %q: got %v, want path refusal", program, err)
		}
	}
	// An alternate absolute path remains trusted caller configuration, not request input.
	for _, program := range []string{"", "/opt/nft/bin/nft"} {
		n := firewall.NFT{Program: program}
		if err := n.Load(ctx, nil); !errors.Is(err, context.Canceled) {
			t.Errorf("load %q: %v", program, err)
		}
		if _, err := n.Table(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("table %q: %v", program, err)
		}
	}
}
