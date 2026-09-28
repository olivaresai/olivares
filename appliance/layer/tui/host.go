// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"errors"
	"os"
	"os/user"
	"strconv"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
)

// SystemAccounts is the host's own account database, read through the system's user and group
// files each time it is asked.
func SystemAccounts() Accounts { return systemAccounts{} }

type systemAccounts struct{}

// Lookup implements Accounts.
func (systemAccounts) Lookup(login string) (uint32, []string, error) {
	account, err := user.Lookup(login)
	if err != nil {
		return 0, nil, err
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return 0, nil, err
	}
	ids, err := account.GroupIds()
	if err != nil {
		return 0, nil, err
	}
	groups := make([]string, 0, len(ids))
	for _, id := range ids {
		group, err := user.LookupGroupId(id)
		if err != nil {
			return 0, nil, err
		}
		groups = append(groups, group.Name)
	}
	return uint32(uid), groups, nil
}

// SelfTerminal attests this console's own process with the helper seam's own reading of the
// kernel's facts: its uid and account, the unit its cgroup names and its controlling terminal.
func SelfTerminal() Terminal { return selfTerminal{} }

type selfTerminal struct{}

// Attest implements Terminal.
func (selfTerminal) Attest() (helperschema.Peer, error) { return invocation.Attest(self{}, 0) }

// self is the kernel's view of this process in the shape the seam reads a connection's peer: the
// peer is this process itself, so its pid needs no pidfd to stay this process's.
type self struct{}

var errNotSelf = errors.New("not this process")

// selfPidfd stands for this process where the seam expects a pidfd.
const selfPidfd = 1

func (self) PeerCred(int) (int, uint32, error) { return os.Getpid(), uint32(os.Getuid()), nil }

func (self) PeerPidfd(int) (int, error) { return selfPidfd, nil }

func (self) PidOf(pidfd int) (int, error) {
	if pidfd != selfPidfd {
		return 0, errNotSelf
	}
	return os.Getpid(), nil
}

func (self) Cgroup(pid int) (string, error) {
	if pid != os.Getpid() {
		return "", errNotSelf
	}
	data, err := os.ReadFile("/proc/self/cgroup")
	return string(data), err
}

func (self) Stat(pid int) (string, error) {
	if pid != os.Getpid() {
		return "", errNotSelf
	}
	data, err := os.ReadFile("/proc/self/stat")
	return string(data), err
}

func (self) AccountOf(uid uint32) (string, error) {
	account, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return "", err
	}
	return account.Username, nil
}

func (self) Alive(int) error { return nil }

func (self) Close(int) {}
