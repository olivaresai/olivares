# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""The host's network plane under SELinux: the network guard (olivares_net_guard_t, also its root initializer),
netrestore (olivares_helper_netrestore_t) and netprobe (olivares_helper_netprobe_t), their sockets and state, and first
boot's host-owner drop-in.

Each path has its own type. The guard's domain holds no generic permission: no attribute as a target, no capability
but chown for its root initializer. Only the admitted peers connect to each socket: the portal and the repair console
to the guard and netprobe, the guard and the repair console to netrestore.

Run: python3 -m unittest discover -s appliance/selinux -p 'test_*.py'
"""
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import compare_rules  # noqa: E402
import generate  # noqa: E402

GUARD, RESTORE, PROBE = "olivares_net_guard_t", "olivares_helper_netrestore_t", "olivares_helper_netprobe_t"
# path regex -> (file type, type, declaration)
NETWORK_CONTEXTS = {
    "/usr/libexec/olivares/olivares-net-guard": ("--", "olivares_net_guard_exec_t", "init_daemon_domain"),
    "/usr/libexec/olivares/olivares-portal-netrestore": ("--", "olivares_helper_netrestore_exec_t", "init_daemon_domain"),
    "/usr/libexec/olivares/olivares-portal-netprobe": ("--", "olivares_helper_netprobe_exec_t", "init_daemon_domain"),
    "/run/olivares-net-guard-api": ("-d", "olivares_net_guard_api_run_t", "files_type"),
    "/run/olivares-net-guard-api/guard\\.sock": ("-s", "olivares_net_guard_sock_t", "files_type"),
    "/run/olivares-helpers/netprobe\\.sock": ("-s", "olivares_helper_netprobe_sock_t", "files_type"),
    "/run/olivares-helpers/netrestore\\.sock": ("-s", "olivares_helper_netrestore_sock_t", "files_type"),
    "/run/olivares-network(/.*)?": ("all", "olivares_network_run_t", "files_type"),
    "/run/olivares-netrestore(/.*)?": ("all", "olivares_netrestore_run_t", "files_type"),
    "/var/lib/olivares-net-guard(/.*)?": ("all", "olivares_net_guard_var_lib_t", "files_type"),
    "/etc/cloud/cloud\\.cfg\\.d/99-olivares-host-owner\\.cfg": ("--", "olivares_cloud_owner_t", "files_config_file"),
}
DOMAINS = {GUARD: "olivares_net_guard_exec_t", RESTORE: "olivares_helper_netrestore_exec_t", PROBE: "olivares_helper_netprobe_exec_t"}


def context_failures(contexts):
    failures = []
    module = {c["path"]: c for c in contexts if c["entry"] == "module"}
    for path, want in NETWORK_CONTEXTS.items():
        c = module.get(path)
        if c is None:
            failures.append(f"{path}: no module context")
        elif (c["file_type"], c["type"], c["declare"]) != want:
            failures.append(f"{path}: {c['file_type']} {c['type']} {c['declare']}, not {' '.join(want)}")
    return failures


def domain_failures(rows):
    """Each network domain is a service domain entered from its program with NoNewPrivileges, and nothing else."""
    failures = []
    for domain, entry in DOMAINS.items():
        entry_rows = [r for r in rows if r["kind"] == "allow" and r["source"] == domain and r["class"] == "file"
                      and "entrypoint" in r["perms"].split()]
        if [(r["target"], r["via"]) for r in entry_rows] != [(entry, "init_daemon_domain")]:
            failures.append(f"{domain}: its one entry point is not {entry} through init_daemon_domain")
        if not any(r["via"] == "init_nnp_daemon_domain" and r["target"] == domain for r in rows):
            failures.append(f"{domain}: no NoNewPrivileges transition")
    return failures

PORTAL, CONSOLE, FIRSTBOOT = "olivares_portal_t", "olivares_repair_console_t", "olivares_firstboot_t"
# (source, target, class, permissions, what needs it): the network plane's essential accesses
REQUIRED = [
    ("init_t", "olivares_net_guard_sock_t", "sock_file", "create setattr", "PID 1 binds guard.sock"),
    ("init_t", GUARD, "unix_stream_socket", "bind create listen", "the guard's listener"),
    (GUARD, GUARD, "unix_stream_socket", "accept getopt read write", "serves guard.sock and attests peers"),
    (GUARD, "olivares_net_guard_var_lib_t", "dir", "add_name remove_name write", "the journal directory"),
    (GUARD, "olivares_net_guard_var_lib_t", "file", "create link rename write", "the journal records"),
    (GUARD, "olivares_network_run_t", "file", "create lock open read", "network.lock and the namespace proof"),
    (GUARD, "olivares_netrestore_run_t", "file", "read", "root's ledger, read only"),
    (GUARD, "system_dbusd_t", "dbus", "send_msg", "the bus driver"),
    (GUARD, "system_dbusd_t", "fd", "use", "NetworkManager's pidfd from the bus"),
    (GUARD, "NetworkManager_t", "dbus", "send_msg", "NetworkManager's methods"),
    ("NetworkManager_t", GUARD, "dbus", "send_msg", "NetworkManager's replies"),
    (GUARD, "NetworkManager_t", "file", "read", "NetworkManager's /proc stat and status"),
    (GUARD, "init_t", "lnk_file", "read", "/proc/1/ns/pid for the initializer"),
    (GUARD, "olivares_portal_etc_t", "file", "read", "the management selection"),
    (GUARD, "olivares_helper_netrestore_sock_t", "sock_file", "getattr write", "dials netrestore"),
    (GUARD, RESTORE, "unix_stream_socket", "connectto", "dials netrestore"),
    (RESTORE, RESTORE, "unix_stream_socket", "getopt read write", "the accepted connection"),
    (RESTORE, "olivares_net_guard_var_lib_t", "file", "read", "the guard's window record"),
    (RESTORE, "olivares_netrestore_run_t", "file", "create write", "root's ledger"),
    (RESTORE, "NetworkManager_var_run_t", "dir", "remove_name", "removes the runtime shadow"),
    (RESTORE, "NetworkManager_var_run_t", "file", "read unlink", "validates and removes the runtime shadow"),
    (RESTORE, "NetworkManager_etc_rw_t", "file", "read", "the persistent baseline keyfile"),
    (RESTORE, "NetworkManager_t", "dbus", "send_msg", "Reload and LoadConnections"),
    (PROBE, PROBE, "unix_stream_socket", "getopt read write", "the accepted connection"),
    (PROBE, "http_port_t", "tcp_socket", "name_connect", "the gateway probe on 443"),
    (PORTAL, GUARD, "unix_stream_socket", "connectto", "the portal is an admitted guard client"),
    (CONSOLE, GUARD, "unix_stream_socket", "connectto", "the console is an admitted guard client"),
    (PORTAL, PROBE, "unix_stream_socket", "connectto", "the portal is an admitted netprobe invoker"),
    (CONSOLE, PROBE, "unix_stream_socket", "connectto", "the console is an admitted netprobe invoker"),
    (CONSOLE, RESTORE, "unix_stream_socket", "connectto", "the console is an admitted netrestore invoker"),
    (FIRSTBOOT, "olivares_cloud_owner_t", "file", "create rename write", "the host-owner drop-in"),
    ("cloud_init_t", "olivares_cloud_owner_t", "file", "read", "cloud-init reads the drop-in"),
    (FIRSTBOOT, "NetworkManager_t", "dbus", "send_msg", "first boot's read-only NetworkManager calls"),
    (FIRSTBOOT, "net_conf_t", "file", "read", "the second-owner scan"),
]
TRANSITIONS = [(GUARD, "var_run_t", "dir", 'olivares_network_run_t "olivares-network"'),
               (GUARD, "var_run_t", "dir", 'olivares_net_guard_api_run_t "olivares-net-guard-api"'),
               (GUARD, "var_run_t", "dir", 'olivares_netrestore_run_t "olivares-netrestore"'),
               (FIRSTBOOT, "etc_t", "file", "olivares_cloud_owner_t")]
# the domain whose listener it is -> the domains admitted to connect
ADMITTED = {GUARD: {PORTAL, CONSOLE}, RESTORE: {GUARD, CONSOLE}, PROBE: {PORTAL, CONSOLE}}
SOCKETS = {"olivares_net_guard_sock_t": GUARD, "olivares_helper_netrestore_sock_t": RESTORE,
           "olivares_helper_netprobe_sock_t": PROBE}
CAPABILITIES = {GUARD: {"chown"}, RESTORE: {"chown", "dac_read_search"}, PROBE: set()}
GENERIC = {"domain", "file_type", "non_security_file_type", "exec_type", "entry_type", "port_type", "node_type",
           "netif_type", "userdomain", "daemon", "application_domain_type", "security_file_type", "configfile"}
# (source, target, class) -> permissions no row may grant
REFUSED = {(FIRSTBOOT, "etc_t", "file"): {"write", "append", "unlink", "rename", "setattr", "create"},
           (GUARD, "etc_t", "file"): {"write", "append", "unlink", "rename", "setattr", "create"},
           (GUARD, "var_run_t", "dir"): {"create", "remove_name", "rmdir"},
           (GUARD, "olivares_netrestore_run_t", "dir"): {"add_name", "write", "remove_name"}}


def access_failures(rows):
    failures, held = [], {}
    allows = [r for r in rows if r["kind"] == "allow" and not r["condition"]]
    for r in allows:
        held.setdefault((r["source"], r["target"], r["class"]), set()).update(r["perms"].split())
    for source, target, cls, perms, why in REQUIRED:
        missing = set(perms.split()) - held.get((source, target, cls), set())
        if missing:
            failures.append(f"{source} lacks {' '.join(sorted(missing))} on {target}:{cls} ({why})")
    transitions = {(r["source"], r["target"], r["class"], r["perms"]) for r in rows if r["kind"] == "type_transition"}
    failures += [f"no type_transition {s} {t}:{c} {p}" for s, t, c, p in TRANSITIONS if (s, t, c, p) not in transitions]
    for r in allows:
        perms = set(r["perms"].split())
        listener = SOCKETS.get(r["target"]) if r["class"] == "sock_file" and "write" in perms else (
            r["target"] if r["class"] == "unix_stream_socket" and "connectto" in perms and r["target"] in ADMITTED else None)
        if listener and r["source"] not in ADMITTED[listener]:
            failures.append(f"{r['id']}: {r['source']} is not an admitted peer of {listener}")
        if r["source"] in CAPABILITIES:
            if r["target"] in GENERIC or "*" in perms:
                failures.append(f"{r['id']}: {r['source']} holds a generic permission on {r['target']}")
            if r["class"] in ("capability", "cap_userns") and perms - CAPABILITIES[r["source"]]:
                failures.append(f"{r['id']}: {r['source']} holds capability {' '.join(sorted(perms - CAPABILITIES[r['source']]))}")
        refused = REFUSED.get((r["source"], r["target"], r["class"]), set()) & perms
        if refused:
            failures.append(f"{r['id']}: {r['source']} may not {' '.join(sorted(refused))} {r['target']}:{r['class']}")
    return failures

# denied.tsv id -> (source, target, class, permission): what the installed policy must refuse
DENIED = {
    "N-NET-PROD-STATE": ("olivares_t", "olivares_net_guard_var_lib_t", "dir", "write"),
    "N-NET-PORTAL-STATE": (PORTAL, "olivares_net_guard_var_lib_t", "dir", "write"),
    "N-NET-CLI-STATE": ("olivares_cli_t", "olivares_net_guard_var_lib_t", "dir", "write"),
    "N-NET-PORTAL-LOCK": (PORTAL, "olivares_network_run_t", "file", "lock"),
    "N-NET-PROBE-NMETC": (PROBE, "NetworkManager_etc_rw_t", "file", "write"),
    "N-NET-PROBE-NMCONF": (PROBE, "NetworkManager_etc_t", "file", "write"),
    "N-NET-PROBE-NMRUN": (PROBE, "NetworkManager_var_run_t", "file", "write"),
    "N-NET-GUARD-SYSCONN": (GUARD, "NetworkManager_etc_rw_t", "file", "write"),
    "N-NET-GUARD-SYSCONN-ADD": (GUARD, "NetworkManager_etc_rw_t", "dir", "add_name"),
    "N-NET-GUARD-LEDGER": (GUARD, "olivares_netrestore_run_t", "dir", "add_name"),
    "N-NET-RESTORE-JOURNAL": (RESTORE, "olivares_net_guard_var_lib_t", "file", "write"),
    "N-FBOOT-ETC-WRITE": (FIRSTBOOT, "etc_t", "file", "write"),
    "N-FBOOT-ETC-UNLINK": (FIRSTBOOT, "etc_t", "file", "unlink"),
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


def network_rows(rows):
    """The network plane's rows: the three domains', and PORTAL-40..46, RCON-16..22 and FBOOT-74..88."""
    def number(rid):
        return int(rid.rsplit("-", 1)[1])
    return [r for r in rows if r["id"].startswith(("NGRD-", "NRST-", "NPRB-"))
            or (r["id"].startswith("PORTAL-") and 40 <= number(r["id"]) <= 46)
            or (r["id"].startswith("RCON-") and 16 <= number(r["id"]) <= 22)
            or (r["id"].startswith("FBOOT-") and 74 <= number(r["id"]) <= 88)]


def repository():
    rows = generate.read_table(os.path.join(HERE, "access.tsv"), generate.ACCESS_COLUMNS)
    contexts = generate.read_table(os.path.join(HERE, "contexts.tsv"), generate.CONTEXTS_COLUMNS)
    return rows, contexts


class NetworkContexts(unittest.TestCase):
    def test_each_network_path_has_its_own_context(self):
        self.assertEqual(context_failures(repository()[1]), [])

    def test_each_network_program_enters_its_own_service_domain(self):
        self.assertEqual(domain_failures(repository()[0]), [])

    def test_a_fedora_type_named_with_capitals_is_a_plain_name(self):
        row = {"id": "X-01", "kind": "allow", "source": "init_t", "target": "NetworkManager_t", "class": "dir",
               "perms": "search", "condition": "", "via": "", "rationale": "reaches NetworkManager's /proc"}
        self.assertIn("allow init_t NetworkManager_t:dir search;", generate.Module([row], []).te())

    def test_a_module_type_with_capitals_is_still_refused(self):
        row = {"id": "X-01", "kind": "allow", "source": "olivares_Portal_t", "target": "etc_t", "class": "dir",
               "perms": "search", "condition": "", "via": "", "rationale": "no"}
        with self.assertRaises(generate.Refused) as refused:
            generate.Module([row], [])
        self.assertIn("X-01: source 'olivares_Portal_t' is a module type that is not lowercase", refused.exception.reasons)


class NetworkAccess(unittest.TestCase):
    def test_the_rows_hold_the_network_planes_accesses(self):
        self.assertEqual(access_failures(repository()[0]), [])

    def test_a_generic_permission_on_the_guards_domain_fails(self):
        rows = repository()[0]
        self.assertEqual(access_failures(rows), [])
        generic = dict(rows[0], id="X-01", kind="allow", source=GUARD, target="file_type", condition="", via="",
                       perms="read", **{"class": "file"})
        capability = dict(generic, id="X-02", target=GUARD, perms="dac_override", **{"class": "capability"})
        failures = access_failures(rows + [generic, capability])
        self.assertIn(f"X-01: {GUARD} holds a generic permission on file_type", failures)
        self.assertIn(f"X-02: {GUARD} holds capability dac_override", failures)

    def test_a_peer_the_admission_tables_do_not_name_fails(self):
        rows = repository()[0]
        self.assertEqual(access_failures(rows), [])
        product = dict(rows[0], id="X-01", kind="allow", source="olivares_t", target=GUARD, condition="", via="",
                       perms="connectto", **{"class": "unix_stream_socket"})
        self.assertIn(f"X-01: olivares_t is not an admitted peer of {GUARD}", access_failures(rows + [product]))



class NetworkDenials(unittest.TestCase):
    def denied(self):
        return compare_rules.read_denied(os.path.join(HERE, "denied.tsv"))

    def test_denied_tsv_refuses_the_network_planes_forbidden_accesses(self):
        self.assertEqual(denied_failures(self.denied()), [])

    def test_without_the_denial_of_netprobe_writing_a_networkmanager_file_the_check_fails(self):
        self.assertEqual(denied_failures(self.denied()), [])
        denied = [d for d in self.denied() if d["id"] != "N-NET-PROBE-NMRUN"]
        self.assertEqual(denied_failures(denied), [f"denied.tsv has no N-NET-PROBE-NMRUN: {PROBE} write on "
                                                   "NetworkManager_var_run_t:file"])


if __name__ == "__main__":
    unittest.main()
