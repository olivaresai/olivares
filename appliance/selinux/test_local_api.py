# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""The appliance's local host-operations API under SELinux: the command-line client olivares-appliance, the portal's
local socket, the lifecycle lock, the receipts directory and the operations leaf of the static portal account.

Each of these paths has its own type, so the rules can name it. The operations leaf is
/var/lib/olivares-portal/operations under a root-owned parent that is the directory alone; the static account
never uses /var/lib/private, so no context may name it.

access.tsv must hold every access the source needs (REQUIRED), and only for the one admitted caller domain,
olivares_cli_t: no other domain may connect to the portal's local listener or write local.sock, and the portal
reads /proc of no other domain. The CLI only stats tls.key; the portal never writes the leaf's parents, nor
writes, removes or replaces the lifecycle lock. denied.tsv carries those refusals (DENIED), so the installed
policy is checked for them too.

Run: python3 -m unittest discover -s appliance/selinux -p 'test_*.py'
"""
import os
import re
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import compare_rules  # noqa: E402
import generate  # noqa: E402

# path regex -> (file type, type, declaration)
LOCAL_CONTEXTS = {
    "/usr/bin/olivares-appliance": ("--", "olivares_cli_exec_t", "application_domain"),
    "/run/olivares-portal-api": ("-d", "olivares_portal_api_run_t", "files_type"),
    "/run/olivares-portal-api/local\\.sock": ("-s", "olivares_portal_api_sock_t", "files_type"),
    "/run/olivares-lifecycle": ("-d", "olivares_lifecycle_run_t", "files_type"),
    "/run/olivares-lifecycle/lifecycle\\.lock": ("--", "olivares_lifecycle_lock_t", "files_type"),
    "/run/olivares-portal-receipts(/.*)?": ("all", "olivares_portal_receipts_run_t", "files_type"),
    "/var/lib/olivares-portal": ("-d", "olivares_portal_var_lib_t", "files_type"),
    "/var/lib/olivares-portal/operations(/.*)?": ("all", "olivares_portal_ops_t", "files_type"),
}
LEAF = "/var/lib/olivares-portal/operations(/.*)?"
PRIVATE = re.compile(r"^/var/lib/private(/|\(|$)")


def context_failures(contexts):
    failures = []
    module = {c["path"]: c for c in contexts if c["entry"] == "module"}
    for path, want in LOCAL_CONTEXTS.items():
        c = module.get(path)
        if c is None:
            failures.append(f"{path}: no module context")
        elif (c["file_type"], c["type"], c["declare"]) != want:
            failures.append(f"{path}: {c['file_type']} {c['type']} {c['declare']}, not {' '.join(want)}")
    for c in contexts:
        if PRIVATE.match(c["path"]):
            failures.append(f"{c['id']}: {c['path']} is under /var/lib/private, which the static portal account never uses")
    leaf = sorted(c["path"] for c in contexts if c["type"] == "olivares_portal_ops_t")
    if leaf != [LEAF]:
        failures.append(f"olivares_portal_ops_t labels {leaf}, not only {LEAF}")
    return failures


# (source, target, class, permissions, what needs it)
CLI, PORTAL = "olivares_cli_t", "olivares_portal_t"
REQUIRED = [
    (CLI, "olivares_cli_exec_t", "file", "entrypoint execute map read", "olivares-appliance enters its domain"),
    (CLI, "user_devpts_t", "chr_file", "read write ioctl", "the operator's terminal"),
    (CLI, "etc_t", "dir", "search", "the TLS delivery check"),
    (CLI, "olivares_portal_etc_t", "dir", "search", "the TLS delivery check"),
    (CLI, "olivares_portal_etc_t", "file", "getattr", "lstat of tls.crt"),
    (CLI, "olivares_portal_key_t", "file", "getattr", "lstat of tls.key"),
    (CLI, "passwd_file_t", "file", "getattr open read", "the static portal account's uid"),
    (CLI, "var_run_t", "dir", "search", "reach the socket directory"),
    (CLI, "olivares_portal_api_run_t", "dir", "getattr search", "lstat of the socket directory"),
    (CLI, "olivares_portal_api_sock_t", "sock_file", "getattr write", "lstat of local.sock and connect"),
    (CLI, PORTAL, "unix_stream_socket", "connectto", "connect to the portal's listener"),
    (CLI, CLI, "unix_stream_socket", "create connect setopt read write", "its client socket with SO_PASSCRED"),
    (CLI, "proc_t", "dir", "search", "the credential sentinel"),
    (CLI, "sysctl_t", "dir", "search", "the credential sentinel"),
    (CLI, "sysctl_kernel_t", "file", "getattr open read", "overflowuid and overflowgid"),
    (PORTAL, PORTAL, "unix_stream_socket", "accept getattr getopt ioctl read write", "the inherited local listener"),
    (PORTAL, PORTAL, "dir", "search", "its own /proc/self/fdinfo"),
    (PORTAL, PORTAL, "file", "getattr open read", "the held pidfd's fdinfo"),
    (PORTAL, CLI, "dir", "search", "/proc/<peer> of the admitted caller"),
    (PORTAL, CLI, "file", "getattr open read", "/proc/<peer>/cgroup"),
    (PORTAL, "passwd_file_t", "file", "getattr open read", "olivares-admins membership"),
    (PORTAL, "olivares_lifecycle_run_t", "dir", "search", "reach lifecycle.lock"),
    (PORTAL, "olivares_lifecycle_lock_t", "file", "getattr open read lock", "the shared hold"),
    (PORTAL, "var_lib_t", "dir", "search", "reach the leaf's parent"),
    (PORTAL, "olivares_portal_var_lib_t", "dir", "search", "reach the leaf"),
    (PORTAL, "olivares_portal_ops_t", "dir", "add_name getattr open read remove_name search write", "the store"),
    (PORTAL, "olivares_portal_ops_t", "file", "create getattr lock open read rename unlink write",
     "store.lock and the records"),
    ("init_t", "olivares_portal_api_run_t", "dir", "add_name create search setattr write", "PID 1 binds local.sock"),
    ("init_t", "olivares_portal_api_sock_t", "sock_file", "create getattr setattr unlink", "PID 1 binds local.sock"),
    ("init_t", PORTAL, "unix_stream_socket", "bind create listen setopt", "PID 1 creates the listener"),
    ("init_t", "olivares_portal_receipts_run_t", "dir", "create mounton rmdir setattr", "the receipts RuntimeDirectory"),
    ("init_t", "olivares_portal_var_lib_t", "dir", "add_name search write", "StateDirectory adds the leaf"),
    ("init_t", "olivares_portal_ops_t", "dir", "create mounton setattr", "StateDirectory creates the leaf"),
    ("init_t", "olivares_portal_ops_t", "file", "getattr setattr", "StateDirectory's ownership fix"),
    ("systemd_tmpfiles_t", "olivares_lifecycle_run_t", "dir", "add_name create setattr write", "tmpfiles"),
    ("systemd_tmpfiles_t", "olivares_lifecycle_lock_t", "file", "create open setattr write", "tmpfiles"),
    ("systemd_tmpfiles_t", "olivares_portal_api_run_t", "dir", "create setattr", "tmpfiles"),
    ("systemd_tmpfiles_t", "olivares_portal_var_lib_t", "dir", "create setattr", "tmpfiles"),
]
ADMITTED = {CLI}
# (source, target, class) -> permissions no row may grant
REFUSED = {
    (CLI, "olivares_portal_key_t", "file"): {"open", "read", "map", "execute", "ioctl", "lock", "write", "append"},
    (PORTAL, "olivares_portal_var_lib_t", "dir"): {"write", "add_name", "remove_name", "create", "setattr", "rmdir"},
    (PORTAL, "var_lib_t", "dir"): {"write", "add_name", "remove_name", "create", "setattr", "rmdir"},
    (PORTAL, "olivares_lifecycle_run_t", "dir"): {"write", "add_name", "remove_name", "create", "setattr", "rmdir"},
    (PORTAL, "olivares_lifecycle_lock_t", "file"): {"write", "append", "unlink", "rename", "setattr", "create"},
}

# denied.tsv id -> (source, target, class, permission): the local API's accesses the installed policy must refuse
DENIED = {
    "N-API-PROD-CONNECT": ("olivares_t", PORTAL, "unix_stream_socket", "connectto"),
    "N-API-PROD-SOCK": ("olivares_t", "olivares_portal_api_sock_t", "sock_file", "write"),
    "N-API-RCON-CONNECT": ("olivares_repair_console_t", PORTAL, "unix_stream_socket", "connectto"),
    "N-PORTAL-OPS-PARENT": (PORTAL, "olivares_portal_var_lib_t", "dir", "write"),
    "N-PORTAL-VARLIB": (PORTAL, "var_lib_t", "dir", "write"),
    "N-PORTAL-LIFECYCLE-ADD": (PORTAL, "olivares_lifecycle_run_t", "dir", "add_name"),
    "N-PORTAL-LIFECYCLE-UNLINK": (PORTAL, "olivares_lifecycle_lock_t", "file", "unlink"),
    "N-PORTAL-LIFECYCLE-WRITE": (PORTAL, "olivares_lifecycle_lock_t", "file", "write"),
    "N-CLI-KEY-OPEN": (CLI, "olivares_portal_key_t", "file", "open"),
    "N-CLI-KEY-READ": (CLI, "olivares_portal_key_t", "file", "read"),
}


def denied_failures(denied):
    by_id = {d["id"]: d for d in denied}
    failures = []
    for rid, want in DENIED.items():
        d = by_id.get(rid)
        if d is None:
            failures.append(f"denied.tsv has no {rid}: {want[0]} {want[3]} on {want[1]}:{want[2]}")
        elif (d["source"], d["target"], d["class"], d["perm"], d["allowed_when"]) != want + ("",):
            failures.append(f"{rid} is not {' '.join(want)} in every state")
    return failures


def access_failures(rows):
    failures, held, domains = [], {}, set()
    allows = [r for r in rows if r["kind"] == "allow" and not r["condition"]]
    for r in allows:
        held.setdefault((r["source"], r["target"], r["class"]), set()).update(r["perms"].split())
        if r["class"] == "file" and "entrypoint" in r["perms"].split():
            domains.add(r["source"])
    for source, target, cls, perms, why in REQUIRED:
        missing = set(perms.split()) - held.get((source, target, cls), set())
        if missing:
            failures.append(f"{source} lacks {' '.join(sorted(missing))} on {target}:{cls} ({why})")
    for r in allows:
        perms = set(r["perms"].split())
        caller = ((r["target"], r["class"]) == (PORTAL, "unix_stream_socket") and "connectto" in perms) or (
            (r["target"], r["class"]) == ("olivares_portal_api_sock_t", "sock_file") and "write" in perms)
        if caller and r["source"] not in ADMITTED:
            failures.append(f"{r['id']}: {r['source']} is not an admitted caller of the local API")
        if (r["source"] == PORTAL and r["class"] in ("dir", "file") and r["target"] in domains - {PORTAL}
                and r["target"] not in ADMITTED):
            failures.append(f"{r['id']}: the portal reads /proc of {r['target']}, which is not an admitted caller")
        refused = REFUSED.get((r["source"], r["target"], r["class"]), set()) & perms
        if refused:
            failures.append(f"{r['id']}: {r['source']} may not {' '.join(sorted(refused))} {r['target']}:{r['class']}")
    return failures


def repository_rows():
    return generate.read_table(os.path.join(HERE, "access.tsv"), generate.ACCESS_COLUMNS)


def local_api_rows(rows):
    """The rule rows of the local API: the CLI's, PID 1's and tmpfiles', and the portal's PORTAL-28 to PORTAL-39. The
    interface row, the operator's entry, is held by LoginEntry and by the manifest's entry entries; the portal's later
    rows belong to the network plane (test_network.py)."""
    return [r["id"] for r in rows if r["kind"] != "interface" and (r["id"].startswith(("CLI-", "LOCAL-"))
            or (r["id"].startswith("PORTAL-") and 28 <= int(r["id"].split("-")[1]) <= 39))]

CALL = "unconfined_run_to(olivares_cli_t, olivares_cli_exec_t)"


def unconfined_failures(te):
    """The module names unconfined exactly once: the one interface call, alone inside an optional_policy block."""
    lines = te.split("\n")
    hits = [n for n, line in enumerate(lines) if "unconfined" in line]
    if len(hits) != 1:
        return [f"olivares.te names unconfined on {len(hits)} lines, not once"]
    n = hits[0]
    if lines[n] != "\t" + CALL or n == 0 or lines[n - 1] != "optional_policy(`" or lines[n + 1:n + 2] != ["')"]:
        return [f"olivares.te line {n + 1} is not the one call inside optional_policy: {lines[n]!r}"]
    return []


def repository_contexts():
    return generate.read_table(os.path.join(HERE, "contexts.tsv"), generate.CONTEXTS_COLUMNS)


class LocalContexts(unittest.TestCase):
    def test_each_local_api_path_has_its_own_context(self):
        self.assertEqual(context_failures(repository_contexts()), [])

    def test_a_leaf_context_under_var_lib_private_fails(self):
        contexts = [dict(c) for c in repository_contexts()]
        leaf = next(c for c in contexts if c["path"] == LEAF)
        leaf["path"] = "/var/lib/private/olivares-portal/operations(/.*)?"
        failures = context_failures(contexts)
        self.assertIn(f"{leaf['id']}: {leaf['path']} is under /var/lib/private, which the static portal account never "
                      "uses", failures)

    def test_the_cli_domain_is_declared_as_a_program_users_run(self):
        with open(os.path.join(HERE, "olivares.te"), encoding="utf-8") as f:
            te = f.read()
        self.assertIn("application_domain(olivares_cli_t, olivares_cli_exec_t)\n", te)


class LocalAccess(unittest.TestCase):
    def test_the_rows_hold_every_access_the_local_api_needs_and_no_more(self):
        self.assertEqual(access_failures(repository_rows()), [])

    def test_the_tables_without_any_one_local_api_row_fail(self):
        rows = repository_rows()
        ids = local_api_rows(rows)
        self.assertEqual(len(ids), 38)
        for rid in ids:
            with self.subTest(row=rid):
                self.assertTrue(access_failures([r for r in rows if r["id"] != rid]))

    def test_a_wildcard_caller_domain_fails(self):
        rows = [dict(r) for r in repository_rows()]
        connect = next(r for r in rows if r["id"] == "CLI-11")
        connect["source"] = "domain"
        self.assertIn("CLI-11: domain is not an admitted caller of the local API", access_failures(rows))

    def test_a_row_letting_the_cli_open_the_private_key_fails(self):
        rows = repository_rows()
        key = dict(next(r for r in rows if r["id"] == "CLI-06"), id="X-01", perms="getattr open read")
        self.assertIn("X-01: olivares_cli_t may not open read olivares_portal_key_t:file", access_failures(rows + [key]))

    def test_a_portal_read_of_another_domains_proc_fails(self):
        rows = repository_rows()
        peer = dict(next(r for r in rows if r["id"] == "PORTAL-32"), id="X-01", target="olivares_t")
        self.assertIn("X-01: the portal reads /proc of olivares_t, which is not an admitted caller",
                      access_failures(rows + [peer]))


class LoginEntry(unittest.TestCase):
    def te(self):
        with open(os.path.join(HERE, "olivares.te"), encoding="utf-8") as f:
            return f.read()

    def test_the_module_names_unconfined_once_in_the_one_call_inside_optional_policy(self):
        self.assertEqual(unconfined_failures(self.te()), [])

    def test_the_call_outside_optional_policy_fails(self):
        te = self.te().replace("optional_policy(`\n\t" + CALL + "\n')\n", CALL + "\n")
        self.assertEqual(unconfined_failures(te), [f"olivares.te line {te.split(chr(10)).index(CALL) + 1} is not the one "
                                                   f"call inside optional_policy: {CALL!r}"])

    def test_a_second_unconfined_term_fails(self):
        te = self.te() + "typeattribute olivares_cli_t unconfined_domain_type;\n"
        self.assertEqual(unconfined_failures(te), ["olivares.te names unconfined on 2 lines, not once"])

class LocalDenials(unittest.TestCase):
    def denied(self):
        return compare_rules.read_denied(os.path.join(HERE, "denied.tsv"))

    def test_denied_tsv_refuses_the_local_api_accesses(self):
        self.assertEqual(denied_failures(self.denied()), [])

    def test_without_the_denial_of_the_clis_key_open_the_check_fails(self):
        denied = [d for d in self.denied() if d["id"] != "N-CLI-KEY-OPEN"]
        self.assertEqual(denied_failures(denied),
                         ["denied.tsv has no N-CLI-KEY-OPEN: olivares_cli_t open on olivares_portal_key_t:file"])


if __name__ == "__main__":
    unittest.main()
