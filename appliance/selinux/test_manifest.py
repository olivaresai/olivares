# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""manifest.tsv lists the accesses the enforcing image must show. Each grant must be held by the rows of access.tsv it
names, on the label it expects; each deny must be a denied.tsv row that no row of access.tsv grants. So deleting a
row a grant needs, or granting what a deny forbids, fails here before any image is built. Every row and denial of
the local API is in the manifest.

Run: python3 -m unittest discover -s appliance/selinux -p 'test_*.py'
"""
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import compare_rules  # noqa: E402
import generate  # noqa: E402
import test_local_api  # noqa: E402
import test_network  # noqa: E402

MANIFEST_COLUMNS = ("id", "source", "domain", "path", "class", "perms", "expected_label", "label_basis", "rows",
                    "kind", "reason")


def entered(rows, login, entry_type):
    """The domains the rows make login enter by executing entry_type: interface calls and type_transition rows."""
    out = []
    for r in rows:
        if r["kind"] == "interface":
            _, transitions, _ = compare_rules.interface_rules(r)
            out += [new for s, t, c, new in transitions if (s, t, c) == (login, entry_type, "process")]
        elif r["kind"] == "type_transition" and (r["source"], r["target"], r["class"]) == (login, entry_type, "process"):
            out.append(r["perms"].split(" ", 1)[0])
    return out


def login_failures(m, rows):
    """entry: a login (domain) running the program (path) runs in the expected context, made by the interface row
    and admitted by the portal's rows after it; no-entry: a copy labeled bin_t keeps the login's context, and no row
    lets the portal read /proc of the login's domain; transition: sesearch shows exactly one transition to the type."""
    by_id = {r["id"]: r for r in rows}
    login, want = m["domain"], m["expected_label"].split(":")[2] if m["expected_label"].count(":") >= 3 else ""
    if m["kind"] == "transition":
        call = by_id.get(m["rows"])
        found = entered(rows, login, call["target"]) if call else []
        if found != [m["expected_label"]]:
            return [f"{m['id']}: {login} has {len(found)} transitions on {call['target'] if call else '?'}:process, "
                    f"not one to {m['expected_label']}"]
        return []
    if m["kind"] == "no-entry":
        out = []
        if want != login or entered(rows, login, "bin_t"):
            out.append(f"{m['id']}: a copy labeled bin_t does not stay in {login}")
        out += [f"{m['id']}: row {r['id']} lets the portal read /proc of {login}, so it would admit the copy"
                for r in rows if r["kind"] == "allow" and r["source"] == "olivares_portal_t" and r["target"] == login]
        return out
    ids = m["rows"].split(";")
    call = by_id.get(ids[0])
    if call is None or call["kind"] != "interface" or want not in entered([call], login, call["target"]):
        return [f"{m['id']}: row {ids[0]} does not enter {want} from {login} on {m['path']}"]
    out = []
    for rid in ids[1:]:
        r = by_id.get(rid)
        if r is None or (r["kind"], r["source"], r["target"], r["condition"]) != ("allow", "olivares_portal_t", want, ""):
            out.append(f"{m['id']}: row {rid} is not the portal's unconditional read of /proc of {want}")
    return out


def manifest_failures(manifest, rows, denied):
    failures, seen = [], set()
    by_id = {r["id"]: r for r in rows}
    denied_by_id = {d["id"]: d for d in denied}
    for m in manifest:
        if m["id"] in seen:
            failures.append(f"{m['id']}: repeated manifest id")
        seen.add(m["id"])
        wanted = set(m["perms"].split())
        if m["kind"] == "grant":
            held = set()
            for rid in m["rows"].split(";"):
                r = by_id.get(rid)
                if r is None:
                    failures.append(f"{m['id']}: row {rid} is not in access.tsv")
                    continue
                if (r["kind"], r["source"], r["target"], r["class"], r["condition"]) != (
                        "allow", m["domain"], m["expected_label"], m["class"], ""):
                    failures.append(f"{m['id']}: row {rid} is not an unconditional allow of {m['domain']} on "
                                    f"{m['expected_label']}:{m['class']}")
                    continue
                held |= set(r["perms"].split())
            if wanted - held:
                failures.append(f"{m['id']}: {m['path']} needs {' '.join(sorted(wanted - held))} on "
                                f"{m['expected_label']}:{m['class']}, which its rows do not hold")
        elif m["kind"] == "deny":
            d = denied_by_id.get(m["rows"])
            if d is None or (d["source"], d["target"], d["class"], d["perm"]) != (
                    m["domain"], m["expected_label"], m["class"], m["perms"]):
                failures.append(f"{m['id']}: denied.tsv has no row {m['rows']} for {m['domain']} "
                                f"{m['perms']} on {m['expected_label']}:{m['class']}")
            for r in rows:
                if (r["kind"] == "allow" and r["source"] == m["domain"] and r["target"] == m["expected_label"]
                        and r["class"] == m["class"] and wanted & set(r["perms"].split())):
                    failures.append(f"{m['id']}: row {r['id']} grants what {m['rows']} denies")
        elif m["kind"] in ("entry", "no-entry", "transition"):
            failures += login_failures(m, rows)
        else:
            failures.append(f"{m['id']}: kind {m['kind']!r} is not grant, deny, entry, no-entry or transition")
    return failures


def entry_failures(manifest, rows):
    """F6's entries for the operator's login: for each interface row, an entry for an SSH root login and one for an
    olivares-admins login, a no-entry for a copy of the program labeled bin_t, and the one transition sesearch shows."""
    failures = []
    for r in (r for r in rows if r["kind"] == "interface"):
        entries = [m for m in manifest if m["kind"] == "entry" and m["rows"].split(";")[0] == r["id"]]
        for who in ("root", "olivares-admins"):
            if not any(who in m["reason"] for m in entries):
                failures.append(f"{r['id']}: no entry entry for an SSH {who} login")
        if not any(m["kind"] == "no-entry" for m in manifest):
            failures.append(f"{r['id']}: no no-entry entry for a copy of the program labeled bin_t")
        if not any(m["kind"] == "transition" and m["rows"] == r["id"] for m in manifest):
            failures.append(f"{r['id']}: no transition entry for sesearch")
    return failures


def repository():
    manifest = generate.read_table(os.path.join(HERE, "manifest.tsv"), MANIFEST_COLUMNS)
    rows = generate.read_table(os.path.join(HERE, "access.tsv"), generate.ACCESS_COLUMNS)
    denied = compare_rules.read_denied(os.path.join(HERE, "denied.tsv"))
    return manifest, rows, denied


class Manifest(unittest.TestCase):
    def test_every_entry_is_held_or_refused_by_the_tables(self):
        self.assertEqual(manifest_failures(*repository()), [])

    def test_each_grant_fails_without_each_of_its_rows(self):
        manifest, rows, denied = repository()
        for m in (m for m in manifest if m["kind"] == "grant"):
            for rid in m["rows"].split(";"):
                with self.subTest(entry=m["id"], row=rid):
                    failures = manifest_failures([m], [r for r in rows if r["id"] != rid], denied)
                    self.assertTrue(failures)

    def test_a_row_granting_a_denied_access_fails(self):
        manifest, rows, denied = repository()
        deny = next(m for m in manifest if m["kind"] == "deny")
        extra = dict(rows[0], id="X-01", kind="allow", source=deny["domain"], target=deny["expected_label"],
                     condition="", via="")
        extra["class"], extra["perms"] = deny["class"], deny["perms"]
        self.assertIn(f"{deny['id']}: row X-01 grants what {deny['rows']} denies",
                      manifest_failures([deny], rows + [extra], denied))

    def test_first_boot_reads_cloud_init_and_userdb_state_and_executes_neither(self):
        manifest, _, _ = repository()
        paths = {(m["path"], m["kind"]) for m in manifest if m["domain"] == "olivares_firstboot_t"}
        for path in ("/usr/bin/cloud-init", "/var/lib/cloud/data/status.json", "/var/lib/cloud/data/result.json",
                     "/etc/cloud/cloud-init.disabled", "/run/cloud-init/disabled", "/proc/cmdline",
                     "/etc/userdb/olivares.user", "/run/userdb/olivares.user", "/run/host/userdb/olivares.user",
                     "/usr/lib/userdb/olivares.user"):
            with self.subTest(path=path):
                self.assertIn((path, "grant"), paths)
        self.assertIn(("/usr/bin/cloud-init", "deny"), paths)
        self.assertIn(("/usr/bin/id", "deny"), paths)

    def test_every_local_api_row_and_denial_is_in_the_manifest(self):
        manifest, rows, _ = repository()
        granted = {rid for m in manifest if m["kind"] == "grant" for rid in m["rows"].split(";")}
        self.assertEqual(sorted(set(test_local_api.local_api_rows(rows)) - granted), [])
        refused = {m["rows"] for m in manifest if m["kind"] == "deny"}
        self.assertEqual(sorted(set(test_local_api.DENIED) - refused), [])

    def test_without_the_denial_of_the_clis_key_open_its_entry_fails(self):
        manifest, rows, denied = repository()
        entry = next(m for m in manifest if m["rows"] == "N-CLI-KEY-OPEN")
        self.assertEqual(manifest_failures([entry], rows, [d for d in denied if d["id"] != "N-CLI-KEY-OPEN"]),
                         [f"{entry['id']}: denied.tsv has no row N-CLI-KEY-OPEN for olivares_cli_t open on "
                          "olivares_portal_key_t:file"])

    def test_the_operators_entry_is_in_the_manifest_for_f6(self):
        manifest, rows, _ = repository()
        self.assertEqual(entry_failures(manifest, rows), [])

    def test_the_manifest_without_an_entry_of_the_operator_fails(self):
        manifest, rows, _ = repository()
        self.assertEqual(entry_failures(manifest, rows), [])
        for kind in ("entry", "no-entry", "transition"):
            with self.subTest(kind=kind):
                self.assertTrue(entry_failures([m for m in manifest if m["kind"] != kind], rows))

    def test_an_entry_the_rows_do_not_make_fails(self):
        manifest, rows, denied = repository()
        entry = next(m for m in manifest if m["kind"] == "entry")
        other = dict(entry, expected_label="unconfined_u:unconfined_r:olivares_portal_t:s0-s0:c0.c1023")
        self.assertIn(f"{entry['id']}: row CLI-16 does not enter olivares_portal_t from {entry['domain']} on "
                      "/usr/bin/olivares-appliance", manifest_failures([other], rows, denied))

    def test_a_portal_read_of_the_login_domain_fails_the_refused_copy(self):
        manifest, rows, denied = repository()
        copy = next(m for m in manifest if m["kind"] == "no-entry")
        peer = dict(next(r for r in rows if r["id"] == "PORTAL-32"), id="X-01", target=copy["domain"])
        self.assertIn(f"{copy['id']}: row X-01 lets the portal read /proc of {copy['domain']}, so it would admit the copy",
                      manifest_failures([copy], rows + [peer], denied))

    def test_a_second_transition_to_the_cli_fails_the_sesearch_entry(self):
        manifest, rows, denied = repository()
        once = next(m for m in manifest if m["kind"] == "transition")
        extra = dict(next(r for r in rows if r["id"] == "CLI-16"), id="X-01")
        self.assertIn(f"{once['id']}: {once['domain']} has 2 transitions on olivares_cli_exec_t:process, not one to "
                      "olivares_cli_t", manifest_failures([once], rows + [extra], denied))


    def test_every_network_row_and_denial_is_in_the_manifest(self):
        manifest, rows, _ = repository()
        net = test_network.network_rows(rows)
        granted = {rid for m in manifest if m["kind"] == "grant" for rid in m["rows"].split(";")}
        self.assertEqual(sorted(r["id"] for r in net if r["kind"] == "allow" and r["id"] not in granted), [])
        labels = {m["expected_label"] for m in manifest if m["kind"] == "grant"}
        self.assertEqual(sorted(r["id"] for r in net if r["kind"] == "type_transition"
                                and r["perms"].split(" ", 1)[0] not in labels), [])
        refused = {m["rows"] for m in manifest if m["kind"] == "deny"}
        self.assertEqual(sorted(set(test_network.DENIED) - refused), [])


if __name__ == "__main__":
    unittest.main()
