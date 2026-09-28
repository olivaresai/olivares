# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Isolated controls for the retained guest-case oracle; no guest is started."""
import copy
import json
import pathlib
import unittest
import oracle

CASES = (pathlib.Path(__file__).resolve().parent / "guest-cases.py").read_text()


def row_line(case):
    """The guest-cases.py line that records case, which must carry its oracle predicates."""
    lines = [line for line in CASES.splitlines() if "g.row('" + case + "'" in line]
    if len(lines) != 1:
        raise AssertionError("expected one row line for " + case)
    return lines[0]

class OracleControls(unittest.TestCase):
    def evidence(self):
        return {"schema": "network-guest-cases/v1", "head": "a"*40,
                "selinux": "Enforcing", "avcs": [], "cases": [
                    {"id": name, "observed": True, "assertions": [True],
                     "scope": "real-NM; injected-probe; root-library-fixture", "receipts": [name+".json"]}
                    for name in oracle.REQUIRED]}
    def test_positive_and_every_missing_case(self):
        evidence = self.evidence()
        oracle.validate(evidence)
        for index in range(len(evidence["cases"])):
            bad = copy.deepcopy(evidence)
            bad["cases"].pop(index)
            with self.assertRaises(ValueError): oracle.validate(bad)
    def test_skip_empty_duplicate_false_and_permissive(self):
        for mutate in [lambda x:x.update(selinux="Permissive"),
                       lambda x:x["cases"][0].update(observed=False),
                       lambda x:x["cases"][0].update(assertions=[]),
                       lambda x:x["cases"][0].update(assertions=[True,False]),
                       lambda x:x["cases"][0].update(receipts=[]),
                       lambda x:x["cases"].append(x["cases"][0]),
                       lambda x:x["cases"][0].update(scope="production-qualified")]:
            bad=self.evidence();mutate(bad)
            with self.assertRaises(ValueError): oracle.validate(bad)

class RebootRollback(unittest.TestCase):
    """G1: G-N4 proves the reboot caused the rollback and that the change was never saved."""
    def status(self, **changes):
        value = {"state": "rolled_back", "reason": "deadline_or_reboot", "boot_id": "a", "final_boot_id": "b", "pending_calls": 0}
        value.update(changes)
        return value
    def test_reboot_cause(self):
        self.assertTrue(all(oracle.reboot_rollback(self.status(), "a", "b")))
        for bad, before, after in [(self.status(final_boot_id="a"), "a", "b"), (self.status(), "a", "a"), (self.status(boot_id="c"), "a", "b"),
                                   (self.status(reason="requested"), "a", "b"), (self.status(state="restoring"), "a", "b"), (self.status(pending_calls=1), "a", "b")]:
            self.assertFalse(all(oracle.reboot_rollback(bad, before, after)))
    def test_in_memory_only_inside_the_window(self):
        self.assertTrue(all(oracle.in_memory_only({"Profile": {"flags": 1}}, "k1", "k1")))
        self.assertFalse(all(oracle.in_memory_only({"Profile": {"flags": 0}}, "k1", "k1")))
        self.assertFalse(all(oracle.in_memory_only({"Profile": {"flags": 1}}, "k1", "k2")))
    def test_guest_n4_row_uses_both(self):
        line = row_line("G-N4")
        self.assertIn("oracle.reboot_rollback(", line)
        self.assertIn("oracle.in_memory_only(", line)

class RestoredThroughNetrestore(unittest.TestCase):
    """G2: G-N3 proves the recovery went through netrestore, bound to the new daemon after the
    original one died, and that the applied settings, not only the stored ones, came back."""
    before = {"Applied": {"ipv4": {"method": "manual", "addresses": ["10.0.3.20/24"]}}}
    def window(self):
        return {"calls": [
            {"method": "checkpoint_create", "attempt": 0, "target": {"pid": 100}, "settled": True, "success": True, "settlement": "correlated_reply"},
            {"method": "reload_connections", "attempt": 1, "target": {"pid": 200}, "settled": True, "success": True, "settlement": "correlated_reply"}],
            "restore_attempts": [{"attempt": 1, "target": {"process": {"pid": 200}}, "closed": True}]}
    def check(self, window, old=100, new=200, after=None):
        return all(oracle.restored_through_netrestore(window, old, new, self.before, after or copy.deepcopy(self.before)))
    def test_positive(self):
        self.assertTrue(self.check(self.window()))
    def test_every_missing_proof_fails(self):
        self.assertFalse(self.check(self.window(), new=100))
        mutations = [lambda w: w.update(restore_attempts=[]),
                     lambda w: w["restore_attempts"][0].update(closed=False),
                     lambda w: w["restore_attempts"][0]["target"]["process"].update(pid=100),
                     lambda w: w["calls"].pop(1),
                     lambda w: w["calls"][1].update(settled=False),
                     lambda w: w["calls"][1].update(success=False),
                     lambda w: w["calls"][1].update(attempt=2),
                     lambda w: w["calls"][1]["target"].update(pid=100),
                     lambda w: w["calls"][0]["target"].update(pid=200)]
        for mutate in mutations:
            window = self.window()
            mutate(window)
            self.assertFalse(self.check(window))
    def test_applied_settings_must_return(self):
        after = {"Applied": {"ipv4": {"method": "manual", "addresses": ["10.0.3.30/24"]}}}
        self.assertFalse(self.check(self.window(), after=after))
    def test_guest_n3_row_uses_it(self):
        self.assertIn("oracle.restored_through_netrestore(", row_line("G-N3"))

class SlaacAndDhcp6(unittest.TestCase):
    """G3: G-N7 compares the SLAAC address before, during and after the change, and the DHCPv6
    lease is acquired before the change and renewed after its revert."""
    ADDR = json.dumps([{"ifname": "nic1", "addr_info": [
        {"family": "inet6", "local": "fd00:33::5054:ff:fe12:3402", "prefixlen": 64, "scope": "global", "dynamic": True, "valid_life_time": 86000},
        {"family": "inet6", "local": "fe80::5054:ff:fe12:3402", "prefixlen": 64, "scope": "link"}]}])
    def test_slaac_addresses_are_compared_at_three_points(self):
        slaac = oracle.slaac_addresses(self.ADDR)
        self.assertEqual(slaac, ["fd00:33::5054:ff:fe12:3402"])
        self.assertTrue(all(oracle.slaac_kept(slaac, slaac, slaac)))
        for before, during, after in [(slaac, [], slaac), (slaac, slaac, ["fd00:33::9"]), ([], [], [])]:
            self.assertFalse(all(oracle.slaac_kept(before, during, after)))
    def test_default_routes_ignore_their_lifetimes(self):
        first = json.dumps([{"dst": "default", "gateway": "fe80::2", "protocol": "ra", "metric": 1024, "expires": 1790}])
        later = json.dumps([{"dst": "default", "gateway": "fe80::2", "protocol": "ra", "metric": 1024, "expires": 1712}])
        self.assertEqual(oracle.default_routes(first), oracle.default_routes(later))
        self.assertNotEqual(oracle.default_routes(first), oracle.default_routes("[]"))
    def test_dhcp6_renewal_across_the_change(self):
        measure = {"acquired": True, "first_lease": "100 1 fd00:66::150", "first_at": 10, "renewed_lease": "220 1 fd00:66::150", "renewed_at": 90}
        self.assertTrue(all(oracle.dhcp6_renewed_across(measure, 20, 80)))
        for bad, started, ended in [(dict(measure, renewed_at=70), 20, 80), (dict(measure, first_at=30), 20, 80),
                                    (dict(measure, renewed_lease=measure["first_lease"]), 20, 80), (dict(measure, acquired=False), 20, 80), (measure, 80, 20)]:
            self.assertFalse(all(oracle.dhcp6_renewed_across(bad, started, ended)))
    def test_guest_n7_row_uses_them(self):
        line = row_line("G-N7")
        for name in ("oracle.slaac_kept(", "oracle.dhcp6_renewed_across(", "'during_ipv6'"):
            self.assertIn(name, line)
        self.assertIn("oracle.default_routes(", CASES)
        self.assertIn("setup-guest.sh ipv6-renewal", CASES)

class WindowExpires(unittest.TestCase):
    """G4: each refused G-N8 confirm leaves its window to expire at the deadline; no revert act."""
    def status(self, **changes):
        value = {"state": "rolled_back", "reason": "deadline_or_reboot", "boot_id": "a", "final_boot_id": "a", "pending_calls": 0}
        value.update(changes)
        return value
    def test_refused_then_expired(self):
        self.assertTrue(oracle.refused_then_expired({"error": "network_confirmation_refused"}, self.status()))
        for response, status in [({}, self.status()), ({"error": "x"}, self.status(reason="requested")), ({"error": "x"}, self.status(final_boot_id="b")),
                                 ({"error": "x"}, self.status(state="confirmed")), ({"error": "x"}, self.status(pending_calls=1))]:
            self.assertFalse(oracle.refused_then_expired(response, status))
    def test_guest_n8_sends_no_revert(self):
        self.assertNotIn("'Action':'revert'", CASES)
        self.assertIn("oracle.refused_then_expired(", CASES)

if __name__ == "__main__": unittest.main()
