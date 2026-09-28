#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Reject an incomplete core guest result; composed admission is always separate."""
import json
import pathlib
import sys

REQUIRED = ["G-N0", "G-N0-second-owner", "G-N1", "G-N2-core", "G-N3", "G-N4", "G-N5", "G-N6-core", "G-N7", "G-N8-core", "transient-driver-refused", "portal-has-no-writer-authority", "installed-interface-identity"]

def validate(value):
    if value.get("schema") != "network-guest-cases/v1" or len(value.get("head", "")) != 40:
        raise ValueError("guest identity missing")
    if value.get("selinux") != "Enforcing" or not isinstance(value.get("avcs"), list):
        raise ValueError("SELinux evidence missing")
    rows=value.get("cases", [])
    if sorted(row.get("id", "") for row in rows) != sorted(REQUIRED):
        raise ValueError("missing, duplicate or foreign case")
    for row in rows:
        if row.get("observed") is not True or not row.get("assertions") or any(v is not True for v in row["assertions"]):
            raise ValueError("unobserved, empty, skipped or failed case: "+row["id"])
        if not row.get("receipts") or "injected-probe" not in row.get("scope", "") or "root-library-fixture" not in row["scope"]:
            raise ValueError("case custody or fixture scope missing")

def reboot_rollback(status, boot_before, boot_after):
    """G-N4: the window opened before the reboot and was rolled back in the new boot, at its
    deadline or reboot, never by a requested revert, with no call pending."""
    return [status.get("state") == "rolled_back", status.get("reason") not in (None, "", "requested"),
            bool(boot_before) and bool(boot_after) and boot_before != boot_after,
            status.get("boot_id") == boot_before, status.get("final_boot_id") == boot_after, status.get("pending_calls") == 0]

def in_memory_only(observation, keyfiles_before, keyfiles_during):
    """Inside the window the candidate is applied but unsaved (UNSAVED flag), and no keyfile changed."""
    flags = observation.get("Profile", {}).get("flags", 0)
    return [isinstance(flags, int) and flags & 1 == 1, keyfiles_before == keyfiles_during]

def restored_through_netrestore(window, pid_before, pid_after, before, after):
    """G-N3: the original daemon was replaced; recovery ran as a closed netrestore attempt bound
    to the new daemon, with a correlated, successful Reload in that attempt; and the applied
    settings, not only the stored profile, returned to the baseline."""
    calls = window.get("calls", [])
    attempts = window.get("restore_attempts", [])
    original = [c for c in calls if c.get("method") == "checkpoint_create"]
    bound = {a.get("attempt") for a in attempts if a.get("closed") and a.get("target", {}).get("process", {}).get("pid") == pid_after}
    reloads = [c for c in calls if c.get("method") == "reload_connections" and c.get("attempt") in bound and c.get("settled") is True
               and c.get("success") is True and c.get("settlement") == "correlated_reply" and c.get("target", {}).get("pid") == pid_after]
    return [bool(pid_before) and bool(pid_after) and pid_before != pid_after,
            bool(original) and all(c.get("target", {}).get("pid") == pid_before for c in original),
            bool(attempts) and all(a.get("closed") is True for a in attempts), bool(bound), bool(reloads),
            after.get("Applied", {}).get("ipv4") == before.get("Applied", {}).get("ipv4")]

def slaac_addresses(ip_json):
    """Global router-advertised (dynamic) IPv6 addresses from `ip -j -6 addr show dev IFACE`."""
    return sorted(a.get("local") for d in json.loads(ip_json) for a in d.get("addr_info", [])
                  if a.get("family") == "inet6" and a.get("scope") == "global" and a.get("dynamic") is True)

def default_routes(ip_json):
    """Default routes as (gateway, protocol) from `ip -j -6 route show default`, without lifetimes."""
    return sorted((r.get("gateway", ""), r.get("protocol", "")) for r in json.loads(ip_json) if r.get("dst") == "default")

def slaac_kept(before, during, after):
    """G-N7: the SLAAC address exists and is the same before, during and after the change."""
    return [bool(before), before == during, before == after]

def dhcp6_renewed_across(measure, change_started, change_ended):
    """G-N7: a DHCPv6 lease acquired before the change was renewed after its revert."""
    first, renewed = measure.get("first_at"), measure.get("renewed_at")
    return [measure.get("acquired") is True, bool(measure.get("first_lease")), bool(measure.get("renewed_lease")),
            measure.get("renewed_lease") != measure.get("first_lease"), change_started < change_ended,
            isinstance(first, int) and first < change_started, isinstance(renewed, int) and renewed > change_ended]

def refused_then_expired(response, status):
    """G-N8: the confirm was refused and its window reverted at the deadline in the same boot,
    never by a requested revert, with no call pending."""
    return bool(response.get("error")) and status.get("state") == "rolled_back" and status.get("reason") not in (None, "", "requested") \
        and bool(status.get("boot_id")) and status.get("final_boot_id") == status.get("boot_id") and status.get("pending_calls") == 0

def main():
    try:
        name=pathlib.Path(sys.argv[1]);value=json.loads(name.read_text());validate(value)
        for row in value["cases"]:
            for receipt in row["receipts"]:
                child=pathlib.Path(receipt)
                if child.is_absolute() or ".." in child.parts or not (name.parent/child).is_file():
                    raise ValueError("missing case receipt")
        for row in value["cases"]: print("PASS "+row["id"]+" (core fixture scope)")
        return 0
    except (ValueError, OSError, IndexError, TypeError) as error:
        print("UNQUALIFIED: "+str(error),file=sys.stderr);return 2
if __name__ == "__main__": sys.exit(main())
