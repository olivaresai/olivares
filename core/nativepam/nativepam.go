// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package nativepam checks one request against the host's native account policy.
// A fixed, socket-activated worker owns PAM and its blocking conversation. No
// credential, session or authorization is retained after the request.
package nativepam

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"
)

const (
	SocketPath          = "/run/olivares-helpers/pam.sock"
	WorkerPath          = "/usr/libexec/olivares/olivares-portal-pam"
	RepairService       = "olivares-portal"
	AccountService      = "sshd"
	AdministratorsGroup = "olivares-admins"
	MaxPassword         = 4096
	MaxLogin            = 256
	WorkerLifetime      = 30 * time.Second
)

var ErrRefused = errors.New("native account check refused")

// Result preserves authentication and account policy as separate facts.
// UID and Login are measured native account facts, never a product grant.
type Result struct {
	Authenticated  bool
	AccountAllowed bool
	Login          string
	UID            uint32
}

// ValidRequest bounds the native conversation before any host policy is used.
func ValidRequest(login string, password []byte) bool {
	return len(login) > 0 && len(login) <= MaxLogin && !strings.ContainsAny(login, "\x00\r\n") &&
		len(password) > 0 && len(password) <= MaxPassword && !bytes.Contains(password, []byte{0})
}

// CheckRepair verifies the repair service. The worker selects the service from
// the kernel-attested portal caller; the request carries no service choice.
// password remains request-owned and is cleared on every return.
func CheckRepair(ctx context.Context, login string, password []byte) (Result, error) {
	defer clear(password)
	return check(ctx, RepairService, login, password)
}

// CheckAccount verifies the existing sshd account-control policy for a native
// engine caller. It cannot produce a repair offer or authenticate a product
// principal. The core binding ceremony supplies its own exact native Ref.
func CheckAccount(ctx context.Context, login string, password []byte) (Result, error) {
	defer clear(password)
	return check(ctx, AccountService, login, password)
}

// LocalAccount resolves a canonical native login without deriving product
// identity from its name, email, GECOS or groups.
func LocalAccount(login string) (uint32, error) {
	account, err := user.Lookup(login)
	if err != nil || account.Username != login {
		return 0, ErrRefused
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return 0, ErrRefused
	}
	return uint32(uid), nil
}

// LocalGroups reads current OS group membership, which is only repair
// admission evidence and never a product permission.
func LocalGroups(login string) ([]string, error) {
	account, err := user.Lookup(login)
	if err != nil || account.Username != login {
		return nil, ErrRefused
	}
	ids, err := account.GroupIds()
	if err != nil {
		return nil, ErrRefused
	}
	groups := make([]string, 0, len(ids))
	for _, id := range ids {
		group, err := user.LookupGroupId(id)
		if err != nil {
			return nil, ErrRefused
		}
		groups = append(groups, group.Name)
	}
	return groups, nil
}

// MeasureRepair refuses absent policy rather than letting PAM use its fallback
// service. It presents no credential and starts no worker.
func MeasureRepair() bool {
	if !readablePolicy(RepairService) || !trustedFile(WorkerPath, true) || !trustedSocket(SocketPath) {
		return false
	}
	_, err := user.LookupGroup(AdministratorsGroup)
	return err == nil
}

func readablePolicy(service string) bool {
	if !trustedFile("/etc/pam.d/"+service, false) {
		return false
	}
	f, err := os.Open("/etc/pam.d/" + service)
	if err != nil {
		return false
	}
	return f.Close() == nil
}
