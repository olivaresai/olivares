// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package services

import (
	"slices"
	"strings"
)

// Class is a unit's row in the class table. The table, never the caller, decides which verbs a
// unit admits.
type Class string

// The rows of the class table.
const (
	// ClassProtected units admit no effect here: status and logs only.
	ClassProtected Class = "protected"
	// ClassStorageOwned units are the storage module's mount units; only it changes them.
	ClassStorageOwned Class = "storage-owned"
	// ClassLockoutRisk units admit every verb, and their plans state the lockout risk.
	ClassLockoutRisk Class = "lockout-risk"
	// ClassManaged units admit every verb.
	ClassManaged Class = "managed"
	// ClassOther units admit status and logs only.
	ClassOther Class = "other"
)

// Refusal is a closed refusal of a verb for a unit. It names the class and the owner that
// changes such a unit, and never repeats a value the caller sent.
type Refusal struct {
	Code   string `json:"code"`
	Class  Class  `json:"class,omitempty"`
	Owner  string `json:"owner,omitempty"`
	Detail string `json:"detail"`
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Detail }

// unitTypes are the unit type suffixes systemd names.
var unitTypes = []string{".service", ".socket", ".target", ".device", ".mount", ".automount", ".swap", ".timer", ".path", ".slice", ".scope"}

// protectedUnits and protectedPrefixes are the units the appliance depends on: the bus, polkit,
// the service manager's own units, NetworkManager, the Appliance Console's units, the tty1
// console, the helper sockets and their instances, the firewall and the network guard.
var protectedUnits = []string{
	"dbus.service", "dbus.socket", "dbus-broker.service", "polkit.service",
	"olivares-repair-console.service", "getty@tty1.service", "autovt@tty1.service",
	"olivares-firewall.service", "olivares-firewall-guard.service",
	"olivares-net-guard.service", "olivares-net-guard.socket", "olivares-network-runtime.service",
	"olivares-portal.service", "olivares-portal.socket",
}

var protectedPrefixes = []string{"systemd-", "NetworkManager", "olivares-portal-", "olivares-helper-"}

// lockoutRiskUnits are the SSH server's units, under Debian's and Fedora's names.
var lockoutRiskUnits = []string{"ssh.service", "sshd.service", "ssh.socket", "sshd.socket"}

// Classify returns unit's row in the class table, in order: storage-owned, protected,
// lockout-risk, managed, other. A name that is not a unit name is other.
func Classify(unit string) Class {
	switch {
	case !ValidUnitName(unit):
		return ClassOther
	case isStorageOwned(unit):
		return ClassStorageOwned
	case isProtected(unit):
		return ClassProtected
	case slices.Contains(lockoutRiskUnits, unit):
		return ClassLockoutRisk
	case isManaged(unit):
		return ClassManaged
	}
	return ClassOther
}

// isStorageOwned reports the Storage module's mount units: srv-olivares-mnt-<name>.mount with a
// non-empty name. Only olivares-portal-storage changes them.
func isStorageOwned(unit string) bool {
	name, ok := strings.CutPrefix(unit, "srv-olivares-mnt-")
	if !ok {
		return false
	}
	rest, isMount := strings.CutSuffix(name, ".mount")
	return isMount && rest != ""
}

func isProtected(unit string) bool {
	if slices.Contains(protectedUnits, unit) {
		return true
	}
	for _, prefix := range protectedPrefixes {
		if strings.HasPrefix(unit, prefix) {
			return true
		}
	}
	return false
}

// managedServices are the product unit and the unattended upgrades and cron services, under
// Debian's and Fedora's names.
var managedServices = []string{"olivares.service", "unattended-upgrades.service", "cron.service", "crond.service"}

// managedTimers are the layer's timers, each by name. The layer ships none yet: a timer joins
// this list with the change that ships it, never by the shape of its name.
var managedTimers = []string{}

// isManaged reports the named services and timers, and an optional app's unit,
// olivares-app-<slug>.service, whose slug is an app slug.
func isManaged(unit string) bool {
	if slices.Contains(managedServices, unit) || slices.Contains(managedTimers, unit) {
		return true
	}
	slug, ok := strings.CutPrefix(unit, "olivares-app-")
	if !ok {
		return false
	}
	name, isService := strings.CutSuffix(slug, ".service")
	return isService && appSlug(name)
}

// appSlug reports an app slug: at most 32 lowercase letters, digits and single hyphens,
// starting with a letter and not ending with a hyphen.
func appSlug(s string) bool {
	if s == "" || len(s) > 32 || s[0] < 'a' || s[0] > 'z' || s[len(s)-1] == '-' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && s[i-1] != '-':
		default:
			return false
		}
	}
	return true
}

// Allows reports whether the class admits verb. Every class admits status and logs; only
// lockout-risk and managed units admit effects. A protected unit admits none, start and enable
// included: starting a power target is a power act, and starting or enabling a network or
// update service would give that plane a second owner.
func (c Class) Allows(verb string) bool {
	if !slices.Contains([]Class{ClassProtected, ClassStorageOwned, ClassLockoutRisk, ClassManaged, ClassOther}, c) {
		return false
	}
	switch {
	case verb == OpStatus || verb == OpLogs:
		return true
	case !IsEffect(verb):
		return false
	case c == ClassLockoutRisk || c == ClassManaged:
		return true
	}
	return false
}

// Owner names who changes a unit of this class, for a refusal.
func (c Class) Owner() string {
	switch c {
	case ClassProtected:
		return "the appliance layer's packages and image"
	case ClassStorageOwned:
		return "olivares-portal-storage"
	case ClassLockoutRisk, ClassManaged:
		return "olivares-portal-units"
	}
	return "no one from this console"
}

// Consequence is the text a plan shows for a unit of this class.
func (c Class) Consequence() string {
	switch c {
	case ClassProtected:
		return "The appliance depends on this unit: no effect is performed on it here, which keeps power acts on the tty1 console and each plane with its one owner; status and logs remain."
	case ClassStorageOwned:
		return "A mount unit of the Storage module: only Storage changes it; status and logs remain."
	case ClassLockoutRisk:
		return "Stopping or disabling the SSH server ends remote shell access to this host; the web console and the tty1 console remain."
	case ClassManaged:
		return "An appliance-managed unit: each verb is one operation with its own act authorization."
	}
	return "Status and logs only: this console changes no unit outside the class table."
}

// ValidUnitName reports whether s is a unit name as this module admits it: at most 255 bytes;
// a prefix of letters, digits and ":", "_", ".", "-", with at most one "@" that separates a
// template from a non-empty instance; and one of systemd's unit type suffixes. There is no slash,
// no backslash, no glob character and no space, so a name is never a path or a pattern.
func ValidUnitName(s string) bool {
	if len(s) == 0 || len(s) > 255 {
		return false
	}
	dot := strings.LastIndexByte(s, '.')
	if dot <= 0 || !slices.Contains(unitTypes, s[dot:]) {
		return false
	}
	prefix := s[:dot]
	if prefix[0] == '-' || prefix[0] == '.' || prefix[0] == '@' {
		return false
	}
	if at := strings.IndexByte(prefix, '@'); at >= 0 && (at == len(prefix)-1 || strings.IndexByte(prefix[at+1:], '@') >= 0) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		c := prefix[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == ':' || c == '_' || c == '.' || c == '-' || c == '@':
		default:
			return false
		}
	}
	return true
}

// Check refuses verb for unit before any call reaches the service manager: a verb outside the
// closed set, a name that is not a unit name, and a verb the unit's class does not admit.
func Check(verb, unit string) *Refusal {
	if verb == OpList || !slices.Contains(Ops(), verb) {
		return &Refusal{Code: CodeInputRefused, Detail: "the verb is not one of this module's unit verbs"}
	}
	if !ValidUnitName(unit) {
		return &Refusal{Code: CodeInputRefused, Detail: "the unit is not a unit name; a path, a pattern or unit content is never accepted"}
	}
	class := Classify(unit)
	if !class.Allows(verb) {
		return &Refusal{Code: CodeUnitProtected, Class: class, Owner: class.Owner(), Detail: class.Consequence()}
	}
	return nil
}
