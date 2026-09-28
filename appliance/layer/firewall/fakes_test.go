// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
)

const (
	bootA = "8d8a1f0c-54c0-4b3e-9d6a-2a1f3b4c5d6e"
	bootB = "1f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"
)

// fakeKernel is the kernel's nftables state as nft reports it: the last ruleset loaded, or none
// after a boot.
type fakeKernel struct {
	ruleset  []byte
	loads    int
	failLoad bool
	// comment, when set, is what the kernel reports instead of the loaded table's comment.
	comment string
}

func (k *fakeKernel) Load(_ context.Context, ruleset []byte) error {
	if k.failLoad {
		return errors.New("netlink: Error: Could not process rule: Operation not permitted")
	}
	k.ruleset = slices.Clone(ruleset)
	k.loads++
	return nil
}

// Table reads the loaded ruleset back as the kernel would report it: the table's comment, each
// base chain's default policy and its number of rules.
func (k *fakeKernel) Table(context.Context) (firewall.Table, error) {
	if k.ruleset == nil {
		return firewall.Table{}, errors.New("Error: No such file or directory; did you mean table 'olivares' in family inet?")
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
		case chain != "" && strings.HasPrefix(line, "type filter hook "):
			policyWord := strings.TrimSuffix(line[strings.LastIndex(line, "policy ")+len("policy "):], ";")
			if chain == "input" {
				table.InputPolicy = policyWord
			} else {
				table.ForwardPolicy = policyWord
			}
		case chain == "input" && line != "":
			table.InputRules++
		case chain == "forward" && line != "":
			table.ForwardRules++
		}
	}
	if k.comment != "" {
		table.Comment = k.comment
	}
	return table, nil
}

// digest is the policy digest the loaded table carries, or "".
func (k *fakeKernel) digest(t *testing.T) string {
	t.Helper()
	table, err := k.Table(context.Background())
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(table.Comment, policy.TableComment)
}

// fakeClock is CLOCK_BOOTTIME with this boot's id.
type fakeClock struct {
	boot string
	now  time.Duration
}

func (c *fakeClock) Now() (string, time.Duration, error) { return c.boot, c.now, nil }

// fakeLock is the network lock; busy is another owner holding it. taken counts every hold.
type fakeLock struct {
	busy  bool
	held  int
	taken int
}

func (l *fakeLock) Acquire() (func(), error) {
	if l.busy {
		return nil, errors.New("network_lock_busy")
	}
	l.held++
	l.taken++
	return func() { l.held-- }, nil
}

// owner is a firewall owner over fakes, with its state and runtime directories under the test's.
type owner struct {
	*firewall.Owner
	kernel *fakeKernel
	clock  *fakeClock
	lock   *fakeLock
}

func newOwner(t *testing.T) *owner {
	t.Helper()
	dir := t.TempDir()
	wall := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	o := &owner{kernel: &fakeKernel{}, clock: &fakeClock{boot: bootA, now: 10 * time.Second}, lock: &fakeLock{}}
	o.Owner = &firewall.Owner{
		StateDir: filepath.Join(dir, "var-lib-olivares-firewall"),
		RunDir:   filepath.Join(dir, "run-olivares-firewall"),
		Kernel:   o.kernel,
		Lock:     o.lock,
		Clock:    o.clock,
		Wall:     func() time.Time { wall = wall.Add(time.Second); return wall },
	}
	return o
}

// reboot is a new boot: a new boot id, CLOCK_BOOTTIME from zero and a kernel without the table.
func (o *owner) reboot() {
	o.clock.boot, o.clock.now = bootB, 5*time.Second
	o.kernel = &fakeKernel{}
	o.Owner.Kernel = o.kernel
}

// managed is a confirmed policy: SSH on 22, the console on eth0, DHCPv6 on eth0.
func managed() policy.Document {
	return policy.Document{
		SchemaVersion:          policy.SchemaVersion,
		SSHPort:                22,
		Portal:                 policy.Portal{Enabled: true, Listen: policy.ListenManagement, ManagementInterfaces: []string{"eth0"}},
		DHCPv6ClientInterfaces: []string{"eth0"},
		Apps:                   []policy.AppRow{},
	}
}

// measurement is the published document, decoded with the console probe's closed schema.
type measurement struct {
	SchemaVersion string               `json:"schema_version"`
	BootID        string               `json:"boot_id"`
	MeasuredAt    string               `json:"measured_at"`
	PolicyDigest  string               `json:"policy_digest"`
	InputPolicy   string               `json:"input_policy"`
	Rows          []policy.MeasuredRow `json:"rows"`
}

// published reads the measurement, and false when none is published.
func (o *owner) published(t *testing.T) (measurement, bool) {
	t.Helper()
	path := filepath.Join(o.RunDir, "measured.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return measurement{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
		t.Errorf("the measurement is %v, want a regular file with mode 0644", info.Mode())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m measurement
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		t.Fatalf("the measurement does not follow the probe's schema: %v\n%s", err, data)
	}
	if _, err := time.Parse(time.RFC3339, m.MeasuredAt); err != nil || m.SchemaVersion != "olivares-firewall-measurement/v1" {
		t.Errorf("the measurement's schema %q or time %q", m.SchemaVersion, m.MeasuredAt)
	}
	return m, true
}

// rowOf returns the measured row of port, or false.
func rowOf(rows []policy.MeasuredRow, port string) (policy.MeasuredRow, bool) {
	for _, r := range rows {
		if r.Port == port {
			return r, true
		}
	}
	return policy.MeasuredRow{}, false
}

// code is a refusal's closed code, or "".
func code(err error) string {
	var refusal *firewall.Refusal
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	return ""
}

func operationID(n byte) string { return strings.Repeat(string("0123456789abcdef"[n%16]), 32) }
