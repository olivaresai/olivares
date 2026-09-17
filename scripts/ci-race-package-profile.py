#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Bounded race-package profiler: run, exec, summarize. Standard library only."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shlex
import shutil
import signal
import stat
import subprocess
import sys
import threading
import time
from datetime import datetime, timezone
from pathlib import Path

RC_PASS = 0
RC_TEST_FAIL = 1
RC_PREPARE = 2
RC_INCOMPLETE = 3
RC_PROBE_NOT_STARTED = 4
RC_CENSORED = 5
RC_UNKNOWN = 6

ALLOWED_PACKAGES = {
    "core/api": {
        "artifact": "core-api",
        "go_dir": "./api",
        "import_path": "github.com/olivaresai/olivares/core/api",
    },
    "core/auth": {
        "artifact": "core-auth",
        "go_dir": "./auth",
        "import_path": "github.com/olivaresai/olivares/core/auth",
    },
    "core/internal/store/sqlstore": {
        "artifact": "core-sqlstore",
        "go_dir": "./internal/store/sqlstore",
        "import_path": "github.com/olivaresai/olivares/core/internal/store/sqlstore",
    },
}

TEST_ACTIONS = frozenset(
    {"start", "run", "pause", "cont", "pass", "fail", "skip", "output", "bench"}
)
BUILD_ACTIONS = frozenset({"build-output", "build-fail"})
SCHED_RE = re.compile(
    r"^SCHED (\d+)ms: gomaxprocs=(\d+) idleprocs=(\d+) threads=(\d+) "
    r"spinningthreads=(\d+) needspinning=(\d+) idlethreads=(\d+) runqueue=(\d+)"
)
GOFLAGS_P = re.compile(r"^-p=[1-9][0-9]*$")
HEX40 = re.compile(r"^[0-9a-fA-F]{40}$")
DSN_KEYS = (
    "OLIVARES_TEST_POSTGRES_DSN",
    "OLIVARES_TEST_POSTGRES_ADMIN_DSN",
    "OLIVARES_TEST_POSTGRES_SUPERUSER_DSN",
    "OLIVARES_TEST_VECTOR_DSN",
)

# Metadata allowlist: numbers, identities, tool paths, validated flags. Never environ dumps.
META_ENV = (
    "GITHUB_RUN_ID",
    "GITHUB_RUN_ATTEMPT",
    "GITHUB_JOB",
    "GITHUB_SHA",
    "GITHUB_REPOSITORY",
    "GITHUB_REF",
    "RUNNER_NAME",
    "RUNNER_OS",
    "RUNNER_ARCH",
    "RUNNER_ENVIRONMENT",
    "RUNNER_TEMP",
)


def utc_now():
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def repo_root():
    return Path(__file__).resolve().parent.parent


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def write_json(path, obj):
    target = Path(path)
    temporary = target.with_name(target.name + ".tmp." + str(os.getpid()))
    temporary.write_text(json.dumps(obj, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.replace(temporary, target)


def append_jsonl(path, obj):
    with open(path, "a", encoding="utf-8") as f:
        f.write(json.dumps(obj, sort_keys=True) + "\n")


def read_json(path, default=None):
    p = Path(path)
    if not p.is_file():
        return default
    try:
        value = json.loads(p.read_text(encoding="utf-8"))
        return value if isinstance(value, dict) else {"_read_error": "non_object"}
    except (OSError, ValueError) as e:
        return {"_read_error": type(e).__name__}


def git_out(root, *args):
    try:
        p = subprocess.run(
            ["git", *args],
            cwd=str(root),
            capture_output=True,
            text=True,
            check=False,
        )
    except OSError as e:
        return {"ok": False, "error": str(e), "stdout": "", "rc": 127}
    return {
        "ok": p.returncode == 0,
        "rc": p.returncode,
        "stdout": p.stdout.strip(),
        "stderr_present": bool(p.stderr),
    }


def git_identity(root):
    commit = git_out(root, "rev-parse", "HEAD")
    tree = git_out(root, "rev-parse", "HEAD^{tree}")
    porcelain = git_out(root, "status", "--porcelain")
    dirty = bool(porcelain.get("stdout"))
    return {
        "commit": commit.get("stdout") if commit.get("ok") else None,
        "tree": tree.get("stdout") if tree.get("ok") else None,
        "dirty": dirty,
        "status_available": porcelain.get("ok"),
        "git_ok": bool(commit.get("ok") and tree.get("ok")),
    }


def dsn_presence():
    out = {}
    for k in DSN_KEYS:
        v = os.environ.get(k)
        out[k] = "present" if v else "absent"
    out["OLIVARES_TEST_POSTGRES_REQUIRED"] = (
        "present" if os.environ.get("OLIVARES_TEST_POSTGRES_REQUIRED") else "absent"
    )
    out["OLIVARES_GATE_STRICT_PG"] = (
        "present" if os.environ.get("OLIVARES_GATE_STRICT_PG") else "absent"
    )
    out["maintenance_target"] = "/postgres"
    out["app_target"] = "/olivares"
    return out


def gomaxprocs_inherited():
    if "GOMAXPROCS" not in os.environ:
        return {"state": "unset"}
    v = os.environ["GOMAXPROCS"]
    if re.fullmatch(r"[1-9][0-9]*", v):
        return {"state": "inherited_positive", "value": v}
    return {"state": "invalid", "reason": "not_a_positive_integer"}


def validate_goflags():
    if "GOFLAGS" not in os.environ:
        return {"ok": True, "state": "unset"}
    v = os.environ["GOFLAGS"]
    if v == "":
        return {"ok": True, "state": "empty"}
    if GOFLAGS_P.fullmatch(v):
        return {"ok": True, "state": "p_N", "value": v}
    tokens = [name for name in ("-run", "-skip", "-exec", "-short", "-tags")
              if any(tok.split("=", 1)[0] == name for tok in v.split())]
    return {
        "ok": False,
        "state": "incompatible",
        "flag_names": tokens,
        "reason": "GOFLAGS is not empty and is not a validated -p=N wrapper value",
    }


def validate_gorace():
    if "GORACE" not in os.environ:
        return {"ok": True, "state": "unset"}
    v = os.environ["GORACE"]
    if v == "":
        return {"ok": True, "state": "empty"}
    lower = v.lower()
    keys = []
    for key in ("halt_on_error", "halt", "exitcode", "log_path", "log_dir", "log"):
        if key in lower:
            keys.append(key)
    if keys:
        return {
            "ok": False,
            "state": "incompatible",
            "keys": keys,
            "reason": "GORACE changes halt/exit/log behaviour",
        }
    return {
        "ok": False,
        "state": "incompatible",
        "reason": "GORACE is set; this profile refuses an unverified race runtime override",
    }


def validate_pg_overrides():
    probe = os.environ.get("OLIVARES_PG_PROBE")
    if probe is not None:
        return {
            "ok": False,
            "state": "incompatible",
            "reason": "OLIVARES_PG_PROBE is rejected for this profile; the wrapper probe must open a connection",
            "value_true_false": probe in ("true", "false"),
        }
    if os.environ.get("OLIVARES_PG_LOCAL_DEFAULTS"):
        return {
            "ok": False,
            "state": "incompatible",
            "reason": "OLIVARES_PG_LOCAL_DEFAULTS is rejected for this profile",
        }
    return {"ok": True, "state": "unset"}


def prepare_overrides():
    goflags = validate_goflags()
    gorace = validate_gorace()
    pg = validate_pg_overrides()
    gmp = gomaxprocs_inherited()
    ok = goflags["ok"] and gorace["ok"] and pg["ok"] and gmp["state"] != "invalid"
    reasons = []
    if not goflags["ok"]:
        reasons.append(goflags.get("reason") or "GOFLAGS")
    if not gorace["ok"]:
        reasons.append(gorace.get("reason") or "GORACE")
    if not pg["ok"]:
        reasons.append(pg.get("reason") or "PG")
    if gmp["state"] == "invalid":
        reasons.append("GOMAXPROCS inherited value is not a positive integer")
    return {
        "ok": ok,
        "GOFLAGS": goflags,
        "GORACE": gorace,
        "pg_overrides": pg,
        "GOMAXPROCS": gmp,
        "reasons": reasons,
        "dsn_presence": dsn_presence(),
    }


def boot_id_hash(proc_root="/proc"):
    p = Path(proc_root) / "sys/kernel/random/boot_id"
    try:
        raw = p.read_text(encoding="utf-8").strip()
    except OSError:
        return {"state": "absent"}
    return {"state": "ok", "sha256": sha256_bytes(raw.encode("utf-8"))}


def hostname_fact():
    try:
        return os.uname().nodename
    except OSError:
        return None


def assignment_identity():
    meta = {k: os.environ[k] for k in META_ENV if k in os.environ}
    meta["hostname"] = hostname_fact()
    meta["boot_id"] = boot_id_hash()
    return meta


def go_tool_facts():
    path = shutil.which("go")
    if not path:
        return {"state": "absent"}
    try:
        ver = subprocess.run(
            [path, "version"], capture_output=True, text=True, check=False, timeout=30
        )
    except OSError as e:
        return {"state": "unreadable", "path": path, "error": str(e)}
    try:
        digest = sha256_file(path)
    except OSError:
        digest = None
    return {
        "state": "ok",
        "path": path,
        "version_stdout": ver.stdout.strip(),
        "version_rc": ver.returncode,
        "sha256": digest,
    }


def tmpdir_facts(path):
    if not path:
        return {"state": "unset"}
    facts = {"state": "ok", "path": path, "length": len(path)}
    try:
        usage = shutil.disk_usage(path)
        facts["avail_bytes"] = usage.free
        facts["total_bytes"] = usage.total
    except OSError as e:
        facts["disk_usage"] = {"state": "unreadable", "error": str(e)}
    try:
        p = subprocess.run(
            ["findmnt", "-n", "-o", "FSTYPE,OPTIONS,TARGET", "--target", path],
            capture_output=True,
            text=True,
            check=False,
            timeout=5,
        )
        if p.returncode == 0 and p.stdout.strip():
            parts = p.stdout.strip().split(None, 2)
            facts["fstype"] = parts[0] if parts else None
            facts["mount_flags"] = parts[1] if len(parts) > 1 else None
            facts["mount_target"] = parts[2] if len(parts) > 2 else None
        else:
            facts["mount"] = {"state": "unavailable", "rc": p.returncode}
    except OSError:
        facts["mount"] = {"state": "unavailable", "reason": "findmnt_absent"}
    return facts


def cache_volumes():
    out = {}
    for key in ("GOCACHE", "GOMODCACHE"):
        p = os.environ.get(key)
        if not p:
            out[key] = {"state": "unset"}
            continue
        if not os.path.exists(p):
            out[key] = {"state": "absent", "path_length": len(p)}
            continue
        try:
            proc = subprocess.run(
                ["du", "-sb", p],
                capture_output=True,
                text=True,
                check=False,
                timeout=30,
            )
        except OSError as e:
            out[key] = {"state": "unreadable", "error": type(e).__name__}
            continue
        if proc.returncode != 0 or not proc.stdout.strip():
            out[key] = {"state": "unreadable", "rc": proc.returncode, "path_length": len(p)}
            continue
        size = proc.stdout.split()[0]
        try:
            size_i = int(size)
        except ValueError:
            out[key] = {"state": "unreadable", "path_length": len(p)}
            continue
        out[key] = {"state": "ok", "bytes": size_i, "path_length": len(p)}
    return out


def read_text_state(path):
    try:
        data = Path(path).read_text(encoding="utf-8")
        return {"state": "ok", "value": data}
    except FileNotFoundError:
        return {"state": "absent"}
    except PermissionError:
        return {"state": "unreadable"}
    except OSError as e:
        return {"state": "unreadable", "error": type(e).__name__}


def parse_stat(text):
    lpar = text.find("(")
    rpar = text.rfind(")")
    if lpar < 0 or rpar < 0:
        return None
    pid = int(text[:lpar].strip())
    comm = text[lpar + 1 : rpar]
    rest = text[rpar + 2 :].split()
    if len(rest) < 22:
        return None
    return {
        "pid": pid,
        "comm": comm,
        "state": rest[0],
        "ppid": rest[1],
        "utime": rest[11],
        "stime": rest[12],
        "starttime": rest[19],
        "rss_pages": rest[21],
    }


def proc_identity(pid, proc_root="/proc"):
    base = Path(proc_root) / str(pid)
    stat_s = read_text_state(base / "stat")
    if stat_s["state"] != "ok":
        return {"state": "gone" if stat_s["state"] == "absent" else "unreadable", "pid": pid, "stat": stat_s}
    parsed = parse_stat(stat_s["value"])
    if not parsed:
        return {"state": "unreadable", "pid": pid, "reason": "stat_parse"}
    ident = {
        "state": "ok",
        "pid": parsed["pid"],
        "starttime": parsed["starttime"],
        "comm": parsed["comm"],
        "ppid": parsed["ppid"],
        "stat": parsed,
    }
    try:
        ident["exe"] = os.readlink(str(base / "exe"))
    except OSError:
        ident["exe"] = {"state": "unreadable"}
    try:
        ident["cwd"] = os.readlink(str(base / "cwd"))
    except OSError:
        ident["cwd"] = {"state": "unreadable"}
    status = read_text_state(base / "status")
    if status["state"] == "ok":
        for line in status["value"].splitlines():
            if line.startswith("Cpus_allowed_list:"):
                ident["Cpus_allowed_list"] = line.split(":", 1)[1].strip()
            elif line.startswith("voluntary_ctxt_switches:"):
                ident["voluntary_ctxt_switches"] = line.split(":", 1)[1].strip()
            elif line.startswith("nonvoluntary_ctxt_switches:"):
                ident["nonvoluntary_ctxt_switches"] = line.split(":", 1)[1].strip()
            elif line.startswith("VmRSS:"):
                ident["VmRSS"] = line.split(":", 1)[1].strip()
            elif line.startswith("VmHWM:"):
                ident["VmHWM"] = line.split(":", 1)[1].strip()
    io_s = read_text_state(base / "io")
    ident["io"] = io_s
    ident["schedstat"] = read_text_state(base / "schedstat")
    try:
        ident["affinity"] = (sorted(os.sched_getaffinity(pid)) if str(proc_root) == "/proc"
                             else sorted(cpu_set(ident.get("Cpus_allowed_list")) or []))
    except (OSError, AttributeError):
        ident["affinity"] = {"state": "unavailable"}
    ident["cgroup"] = read_text_state(base / "cgroup")
    return ident


def parse_cpu_max(text):
    parts = text.strip().split()
    if not parts:
        return {"state": "absent"}
    quota, period = parts[0], parts[1] if len(parts) > 1 else None
    if quota == "max" and period is not None and period.isdigit() and int(period) > 0:
        return {"state": "unlimited", "quota": "max", "period": period, "raw": text.strip()}
    if quota == "-1":
        return {"state": "unlimited", "quota": "-1", "period": period, "raw": text.strip()}
    try:
        q = int(quota)
        p = int(period) if period is not None else 0
    except ValueError:
        return {"state": "unreadable", "raw": text.strip()}
    if p <= 0 or q <= 0:
        return {"state": "unreadable", "raw": text.strip(), "reason": "quota_or_period"}
    return {
        "state": "finite",
        "quota": q,
        "period": p,
        "cpus": q / p,
        "raw": text.strip(),
    }


def parse_cfs_pair(quota_text, period_text):
    try:
        q = int(quota_text.strip())
        p = int(period_text.strip())
    except (ValueError, AttributeError):
        return {"state": "unreadable"}
    if q == -1 and p > 0:
        return {"state": "unlimited", "quota": -1, "period": p, "raw_quota": q}
    if p <= 0 or q <= 0:
        return {"state": "unreadable", "quota": q, "period": p}
    return {"state": "finite", "quota": q, "period": p, "cpus": q / p}


def cpu_set(value):
    try:
        cpus = set()
        for part in value.strip().split(","):
            if not part:
                continue
            ends = [int(x) for x in part.split("-")]
            lo, hi = ends[0], ends[-1]
            if len(ends) > 2 or lo < 0 or hi < lo or hi > 1048576:
                return None
            cpus.update(range(lo, hi + 1))
        return cpus
    except (ValueError, AttributeError):
        return None


def mount_unescape(value):
    return re.sub(r"\\([0-7]{3})", lambda m: chr(int(m[1], 8)), value)


def cgroup_mounts(text):
    mounts = []
    for line in text.splitlines():
        left, sep, right = line.partition(" - ")
        fields, tail = left.split(), right.split()
        if not sep or len(fields) < 6 or len(tail) < 3 or tail[0] not in ("cgroup", "cgroup2"):
            continue
        mounts.append({"mount_id": fields[0], "device": fields[2],
                       "root": mount_unescape(fields[3]),
                       "mountpoint": mount_unescape(fields[4]),
                       "version": "v2" if tail[0] == "cgroup2" else "v1",
                       "controllers": sorted(set(tail[2].split(",")))})
    return mounts


def walk_cgroup(pid, proc_root="/proc", sysfs_root=None):
    """Resolve membership against target mountinfo; never certify hidden ancestors.

    sysfs_root is a fixture filesystem root, with the mountpoint below it.
    Production reads through /proc/PID/root to respect that PID's mount namespace.
    An incompatible membership/mount root stays unresolved; no root-path fallback.
    """
    ident = proc_identity(pid, proc_root)
    result = {"pid": pid, "identity_state": ident.get("state"), "version": "unknown",
              "ancestors": [], "hierarchies": [], "host_limit_complete": False,
              "scope": "visible_target_mounts_only; external_ancestors_unknown",
              "visible_limit_complete": False, "effective": {"state": "unknown"}}
    base = Path(proc_root) / str(pid)
    membership = read_text_state(base / "cgroup")
    mountinfo = read_text_state(base / "mountinfo")
    result.update(cgroup_file=membership, mountinfo_state=mountinfo["state"])
    if ident.get("state") != "ok" or membership["state"] != "ok" or mountinfo["state"] != "ok":
        return result
    mounts = cgroup_mounts(mountinfo["value"])
    quotas, sets, identities, problems = [], [], [], []
    affinity = cpu_set(ident.get("Cpus_allowed_list"))
    if affinity:
        sets.append(affinity)
        result["affinity_cpus"] = sorted(affinity)
    else:
        problems.append("affinity_unavailable")
    seen_cpu = False
    for line in membership["value"].splitlines():
        fields = line.split(":", 2)
        if len(fields) != 3:
            problems.append("membership_invalid")
            continue
        hid, controller_text, rel = fields
        version = "v2" if hid == "0" and not controller_text else "v1"
        controllers = set(controller_text.split(",")) if controller_text else set()
        if version == "v1" and not controllers.intersection({"cpu", "cpuacct", "cpuset", "memory"}):
            continue
        candidates = []
        for mount in mounts:
            if mount["version"] != version:
                continue
            if version == "v1" and not controllers.issubset(set(mount["controllers"])):
                continue
            root = mount["root"].rstrip("/")
            if rel == root or rel.startswith(root + "/"):
                suffix = rel[len(root):].lstrip("/")
                if ".." not in Path(suffix).parts:
                    candidates.append((mount, suffix))
        hierarchy = {"hierarchy_id": hid, "controllers": sorted(controllers),
                     "membership": rel, "version": version, "state": "unresolved"}
        result["hierarchies"].append(hierarchy)
        if not candidates:
            problems.append("mount_unresolved")
            continue
        # The widest visible mount exposes the most ancestors. Never sum sibling quotas.
        mount, suffix = min(candidates, key=lambda x: len(Path(x[0]["root"]).parts))
        fsroot = Path(sysfs_root) if sysfs_root is not None else base / "root"
        mounted = fsroot / mount["mountpoint"].lstrip("/")
        leaf = mounted / suffix
        hierarchy.update(state="resolved", mount=mount, leaf=str(leaf))
        result["version"] = version if result["version"] in ("unknown", version) else "hybrid"
        chain = []
        nodepath = leaf
        while True:
            chain.append(nodepath)
            if nodepath == mounted:
                break
            nodepath = nodepath.parent
        for index, nodepath in enumerate(chain):
            node = {"path": str(nodepath), "controllers": sorted(controllers),
                    "version": version, "leaf": index == 0,
                    "mount_root": nodepath == mounted}
            try:
                st = nodepath.stat()
                node["identity"] = [mount["device"], st.st_dev, st.st_ino]
            except OSError:
                node["identity"] = None
                problems.append("node_unreadable")
            identities.append([hid, rel, mount["mount_id"], mount["root"], node["identity"]])
            names = []
            if version == "v2":
                names = ["cpu.max", "cpu.stat", "cpu.pressure", "io.pressure", "memory.pressure",
                         "memory.max", "memory.current", "memory.events", "cpuset.cpus",
                         "cpuset.cpus.effective", "cgroup.controllers"]
            else:
                if "cpu" in controllers:
                    names += ["cpu.cfs_quota_us", "cpu.cfs_period_us", "cpu.stat"]
                if "cpuacct" in controllers:
                    names += ["cpuacct.usage", "cpuacct.stat"]
                if "cpuset" in controllers:
                    names += ["cpuset.cpus", "cpuset.effective_cpus"]
                if "memory" in controllers:
                    names += ["memory.limit_in_bytes", "memory.usage_in_bytes", "memory.failcnt",
                              "memory.stat", "memory.oom_control"]
            for name in names:
                node[name] = read_text_state(nodepath / name)
                if node[name]["state"] == "unreadable":
                    problems.append(name + "_unreadable")
            if version == "v2" or "cpu" in controllers:
                seen_cpu = True
                if version == "v2":
                    raw = node["cpu.max"]
                    quota = parse_cpu_max(raw["value"]) if raw["state"] == "ok" else raw
                else:
                    q, period = node["cpu.cfs_quota_us"], node["cpu.cfs_period_us"]
                    quota = parse_cfs_pair(q["value"], period["value"]) if q["state"] == period["state"] == "ok" else {
                        "state": "unreadable" if "unreadable" in (q["state"], period["state"]) else "absent"}
                node["quota"] = quota
                if quota["state"] == "finite":
                    quotas.append(quota["cpus"])
                elif quota["state"] != "unlimited":
                    # v2's true root has no cpu.max. A namespace root may hide a limit;
                    # keep 'absent' and host_limit_complete=false even there.
                    if not (version == "v2" and nodepath == mounted and quota["state"] == "absent"):
                        problems.append("quota_" + quota["state"])
            for key in ("cpuset.cpus.effective", "cpuset.effective_cpus", "cpuset.cpus"):
                raw = node.get(key, {})
                if raw.get("state") == "ok":
                    cs = cpu_set(raw["value"])
                    if cs:
                        sets.append(cs)
                        break
                    if cs is None:
                        problems.append("cpuset_invalid")
            result["ancestors"].append(node)
    if not seen_cpu:
        problems.append("cpu_controller_unresolved")
    final_ident = proc_identity(pid, proc_root)
    if (final_ident.get("starttime") != ident.get("starttime") or
            read_text_state(base / "cgroup") != membership or
            read_text_state(base / "mountinfo") != mountinfo):
        problems.append("identity_or_membership_changed_during_sample")
        identities = None
    memory_limits = []
    for node in result["ancestors"]:
        raw = node.get("memory.max", node.get("memory.limit_in_bytes", {"state": "absent"}))
        limit = dict(raw)
        if raw.get("state") == "ok":
            value = raw["value"].strip()
            limit = {"state": "unlimited", "raw": value} if value == "max" else (
                {"state": "finite", "bytes": int(value), "raw": value} if value.isdigit()
                else {"state": "unreadable"})
        node["memory_limit"] = limit
        if limit["state"] == "finite":
            memory_limits.append(limit["bytes"])
    result["visible_memory_limit_bytes"] = min(memory_limits) if memory_limits else None
    allowed = set.intersection(*sets) if sets else None
    ceilings = list(quotas)
    if allowed is not None:
        ceilings.append(len(allowed))
    result.update(identity=identities, problems=sorted(set(problems)),
                  visible_limit_complete=not problems,
                  quota_cpus=min(quotas) if quotas else None,
                  cpuset_affinity_cpus=sorted(allowed) if allowed is not None else None)
    result["effective"] = {"state": "finite" if ceilings else "unknown",
                           "cpus": min(ceilings) if ceilings else None,
                           "scope": "visible_upper_bound", "complete": not problems}
    return result


def monotonic_delta(before, after, identity_before, identity_after):
    if identity_before is None or identity_after is None:
        return {"state": "unavailable", "reason": "identity_unknown"}
    if identity_before != identity_after:
        return {"state": "discontinuity", "reason": "identity_changed_or_migrated"}
    if not before or before.keys() != after.keys():
        return {"state": "unavailable", "reason": "counter_set_incompatible"}
    if any(after[k] < v for k, v in before.items()):
        return {"state": "discontinuity", "reason": "counter_reset"}
    return {"state": "ok", "values": {k: after[k] - v for k, v in before.items()}}


def counter_values(raw, pressure=False, scalar=False):
    if raw.get("state") != "ok":
        return {}
    try:
        if scalar:
            return {"value": int(raw["value"].strip())}
        result = {}
        for line in raw["value"].splitlines():
            fields = line.split()
            if pressure:
                for field in fields[1:]:
                    if field.startswith("total="):
                        result[fields[0] + ".total"] = int(field.split("=", 1)[1])
            elif len(fields) == 2:
                result[fields[0]] = int(fields[1])
        return result
    except (ValueError, IndexError):
        return {}


def resource_delta(before, after):
    """CPU/process and aggregate cgroup counters remain separate columns."""
    if not before or before.get("state") != "ok" or after.get("state") != "ok":
        return {"state": "unavailable", "reason": "process_not_observed"}
    ident0 = [before.get("pid"), before.get("starttime")] if before.get("starttime") else None
    ident1 = [after.get("pid"), after.get("starttime")] if after.get("starttime") else None
    def cpu(rec):
        return {k: int(rec["stat"][k]) for k in ("utime", "stime")}
    result = {"process_cpu_ticks": monotonic_delta(cpu(before), cpu(after), ident0, ident1)}
    cg0, cg1 = before.get("cgroup_tree", {}), after.get("cgroup_tree", {})
    id0, id1 = cg0.get("identity"), cg1.get("identity")
    if ident0 != ident1 or not id0 or id0 != id1:
        result["cgroup"] = {"state": "discontinuity" if id0 and id1 else "unavailable",
                            "reason": "identity_changed_or_migrated"}
        return result
    rows = []
    for old, new in zip(cg0["ancestors"], cg1["ancestors"]):
        counters = {}
        for key in ("cpu.stat", "cpuacct.usage", "cpuacct.stat", "cpu.pressure", "io.pressure",
                    "memory.pressure", "memory.events", "memory.failcnt"):
            kw = {"pressure": key.endswith(".pressure"), "scalar": key in ("cpuacct.usage", "memory.failcnt")}
            counters[key] = monotonic_delta(counter_values(old.get(key, {}), **kw),
                                           counter_values(new.get(key, {}), **kw),
                                           old.get("identity"), new.get("identity"))
        rows.append({"path": new["path"], "counters": counters})
    result["cgroup"] = {"state": "compatible", "nodes": rows,
                        "scope": "aggregate_includes_other_processes"}
    return result


def host_sample(proc_root="/proc"):
    return {
        "identity": boot_id_hash(proc_root),
        "scope": "observer_procfs; aggregate CPU and PSI can include other workloads",
        "stat": read_text_state(Path(proc_root) / "stat"),
        "loadavg": read_text_state(Path(proc_root) / "loadavg"),
        "pressure_cpu": read_text_state(Path(proc_root) / "pressure/cpu"),
        "pressure_io": read_text_state(Path(proc_root) / "pressure/io"),
        "pressure_memory": read_text_state(Path(proc_root) / "pressure/memory"),
    }


def host_delta(before, after):
    if not before:
        return {"state": "initial"}
    def counters(host):
        result = {}
        raw = host.get("stat", {})
        if raw.get("state") == "ok":
            line = next((l for l in raw["value"].splitlines() if l.startswith("cpu ")), "")
            try:
                names = ("user", "nice", "system", "idle", "iowait", "irq", "softirq", "steal", "guest", "guest_nice")
                result.update({name: int(value) for name, value in zip(names, line.split()[1:])})
            except ValueError:
                pass
        return result
    id0, id1 = before.get("identity", {}), after.get("identity", {})
    identity0 = id0.get("sha256") if id0.get("state") == "ok" else None
    identity1 = id1.get("sha256") if id1.get("state") == "ok" else None
    result = {"cpu_ticks": monotonic_delta(counters(before), counters(after), identity0, identity1)}
    for key in ("pressure_cpu", "pressure_io", "pressure_memory"):
        result[key] = monotonic_delta(counter_values(before.get(key, {}), pressure=True),
                                      counter_values(after.get(key, {}), pressure=True), identity0, identity1)
    return result


def cpu_quota_aux(root):
    script = Path(root) / "scripts/cpu-quota.sh"
    if not script.is_file():
        return {"state": "absent", "role": "auxiliary_not_certified"}
    try:
        p = subprocess.run(
            ["bash", str(script)],
            capture_output=True,
            text=True,
            check=False,
            timeout=5,
        )
    except OSError as e:
        return {"state": "unreadable", "role": "auxiliary_not_certified", "error": str(e)}
    return {
        "state": "ok" if p.returncode == 0 else "error",
        "rc": p.returncode,
        "stdout": p.stdout.strip(),
        "role": "auxiliary_not_certified",
        "note": "reads fixed root cgroup paths and ceils; not the certified PID ceiling",
    }


def merge_godebug(existing):
    parts = [p for p in (existing or "").split(",") if p]
    keys = {}
    order = []
    for p in parts:
        k = p.split("=", 1)[0]
        if k not in keys:
            order.append(k)
        keys[k] = p
    if "schedtrace" not in keys:
        order.append("schedtrace")
    keys["schedtrace"] = "schedtrace=5000"
    keys["scheddetail"] = "scheddetail=0"
    if "scheddetail" not in order:
        order.append("scheddetail")
    return ",".join(keys[k] for k in order)


def write_test_exec(out_dir, instrument, python_exe, go_exe=None):
    path = Path(out_dir) / ("go-exec" if go_exe else "test-exec")
    body = (
        "#!/usr/bin/env bash\n"
        "set -euo pipefail\n"
        "exec {py} {inst} exec --out {out} {producer} -- {go}\"$@\"\n"
    ).format(
        producer="--producer go" if go_exe else "--producer binary",
        go=(shlex.quote(str(go_exe)) + " ") if go_exe else "",
        py=shlex.quote(python_exe),
        inst=shlex.quote(str(instrument)),
        out=shlex.quote(str(out_dir)),
    )
    path.write_text(body, encoding="utf-8")
    path.chmod(path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
    return {
        "path": str(path),
        "sha256": sha256_file(path),
        "mode": oct(path.stat().st_mode & 0o777),
        "body_sha256": sha256_bytes(body.encode("utf-8")),
    }


def is_direct_sched_line(line):
    return line.startswith("SCHED ")


def parse_scheduler_text(text):
    rows = []
    una = []
    seen_nonzero = False
    last_ms = None
    attributive = True
    for i, raw in enumerate(text.splitlines(), 1):
        if not is_direct_sched_line(raw):
            continue
        m = SCHED_RE.match(raw)
        if not m:
            attributive = False
            una.append({"line_no": i, "reason": "unparsed_direct_scheduler"})
            rows.append(
                {
                    "line_no": i,
                    "raw": raw,
                    "state": "unparsed",
                    "attributable": attributive,
                }
            )
            continue
        ms = int(m.group(1))
        if last_ms is not None and ms <= last_ms:
            attributive = False
            una.append({"line_no": i, "reason": "sched_clock_not_increasing", "ms": ms, "prev": last_ms})
        if ms == 0 and seen_nonzero:
            attributive = False
            una.append({"line_no": i, "reason": "sched_clock_restart", "ms": ms})
        if ms > 0:
            seen_nonzero = True
        last_ms = ms
        rows.append(
            {
                "line_no": i,
                "ms": ms,
                "gomaxprocs": int(m.group(2)),
                "idleprocs": int(m.group(3)),
                "threads": int(m.group(4)),
                "spinningthreads": int(m.group(5)),
                "needspinning": int(m.group(6)),
                "idlethreads": int(m.group(7)),
                "runqueue": int(m.group(8)),
                "raw": raw,
                "attributable": attributive,
            }
        )
    values = []
    for row in rows:
        if row.get("attributable") and "gomaxprocs" in row:
            if not values or values[-1] != row["gomaxprocs"]:
                values.append(row["gomaxprocs"])
    return {
        "lines": rows,
        "unattributable": una,
        "gomaxprocs_observed": values if attributive or values else [],
        "runtime_attributable": attributive and bool(values),
        "source": "binary.stderr.raw",
        "go_env_gomaxprocs_used": False,
    }


def parse_json_stream(text):
    events = []
    errors = []
    lines = text.splitlines()
    for i, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError as e:
            errors.append(
                {
                    "line_no": i,
                    "kind": "invalid_json",
                    "truncated": i == len(lines),
                    "error": e.msg,
                }
            )
            continue
        if not isinstance(obj, dict):
            errors.append({"line_no": i, "kind": "non_object"})
            continue
        action = obj.get("Action")
        kind = "test"
        if action in BUILD_ACTIONS:
            kind = "build"
        elif action in TEST_ACTIONS:
            kind = "test"
        elif action is None and "ImportPath" in obj:
            kind = "unknown_valid"
        elif action is None:
            kind = "unknown_valid"
        else:
            kind = "unknown_valid"
        events.append({"line_no": i, "kind": kind, "event": obj})
    return events, errors


def test_kind(name):
    if name.startswith("Example"):
        return "example"
    if name.startswith("Fuzz") or "/seed=" in name or "/seed#" in name:
        return "fuzz"
    if "/" in name:
        return "subtest"
    return "top_level"


def event_timing(events):
    stamps = [e.get("time") for e in events if e.get("time")]
    paused, pause, issues = [], None, []
    for event in events:
        if event["action"] == "pause":
            pause = event.get("time")
        elif event["action"] == "cont":
            end = event.get("time")
            duration = None
            if pause and end:
                try:
                    duration = (datetime.fromisoformat(end.replace("Z", "+00:00")) -
                                datetime.fromisoformat(pause.replace("Z", "+00:00"))).total_seconds()
                    if duration < 0:
                        issues.append("wall_clock_reversed")
                        duration = None
                except ValueError:
                    issues.append("invalid_timestamp")
            paused.append({"pause_utc": pause, "cont_utc": end, "wall_seconds": duration})
            pause = None
    return {"first_event_utc": stamps[0] if stamps else None,
            "last_event_utc": stamps[-1] if stamps else None, "parked_intervals": paused,
            "open_pause_utc": pause, "issues": issues,
            "clock": "Go event UTC; wall duration is not CPU or a monotonic clock"}


def summarize_events(events, errors):
    tests = {}
    packages = {}
    build = 0
    unknown = 0
    for item in events:
        ev = item["event"]
        action = ev.get("Action")
        if item["kind"] == "build":
            build += 1
            continue
        if item["kind"] != "test":
            unknown += 1
            continue
        pkg = ev.get("Package") or ""
        name = ev.get("Test")
        pkg_rec = packages.setdefault(
            pkg,
            {
                "package": pkg,
                "terminal": None,
                "elapsed": None,
                "start": False,
                "events": [],
                "outputs": [],
            },
        )
        if not name:
            pkg_rec["events"].append({"action": action, "time": ev.get("Time"), "line_no": item["line_no"]})
            if action == "start":
                pkg_rec["start"] = True
            elif action in ("pass", "fail", "skip"):
                pkg_rec["terminal"] = action
                if "Elapsed" in ev:
                    pkg_rec["elapsed"] = ev.get("Elapsed")
            elif action == "output":
                pkg_rec["outputs"].append(ev.get("Output") or "")
            continue
        rec = tests.setdefault(
            (pkg, name),
            {
                "package": pkg,
                "test": name,
                "kind": test_kind(name),
                "run": 0,
                "pause": 0,
                "cont": 0,
                "terminal": None,
                "elapsed": None,
                "skip_reason": None,
                "events": [],
                "outputs": [],
            },
        )
        if action in ("run", "pause", "cont", "pass", "fail", "skip"):
            rec["events"].append({"action": action, "time": ev.get("Time"), "line_no": item["line_no"]})
        if action == "run":
            rec["run"] += 1
        elif action == "pause":
            rec["pause"] += 1
        elif action == "cont":
            rec["cont"] += 1
        elif action in ("pass", "fail", "skip"):
            rec["terminal"] = action
            if "Elapsed" in ev:
                rec["elapsed"] = ev.get("Elapsed")
        elif action == "output":
            rec["outputs"].append(ev.get("Output") or "")
    started_no_terminal = []
    rows = []
    for rec in tests.values():
        if rec["terminal"] == "skip":
            reason = None
            for chunk in rec["outputs"]:
                for line in chunk.splitlines():
                    stripped = line.strip()
                    if stripped and not stripped.startswith("---") and "SKIP" not in stripped[:8]:
                        reason = stripped
                        break
                if reason:
                    break
            rec["skip_reason"] = reason
        compact = {
            "package": rec["package"],
            "test": rec["test"],
            "kind": rec["kind"],
            "run": rec["run"],
            "pause": rec["pause"],
            "cont": rec["cont"],
            "terminal": rec["terminal"],
            "elapsed": rec["elapsed"],
            "skip_reason": rec["skip_reason"],
            "events": rec["events"],
            "timing": event_timing(rec["events"]),
            "elapsed_semantics": "Go reported seconds; zero may be rounded, not zero physical duration",
        }
        rows.append(compact)
        if rec["run"] and rec["terminal"] is None:
            started_no_terminal.append(compact)
    top = [r for r in rows if r["kind"] == "top_level"]
    sub = [r for r in rows if r["kind"] == "subtest"]
    examples = [r for r in rows if r["kind"] == "example"]
    fuzz = [r for r in rows if r["kind"] == "fuzz"]
    pkg_list = list(packages.values())
    pkg_terminal_missing = [p for p in pkg_list if p["start"] and p["terminal"] is None]
    tests_all_pass = bool(rows) and all(r["terminal"] == "pass" for r in rows if r["terminal"])
    any_fail = any(r["terminal"] == "fail" for r in rows) or any(
        p.get("terminal") == "fail" for p in pkg_list
    )
    epilogue = False
    for p in pkg_list:
        if p.get("terminal") == "fail" and rows and all(
            r["terminal"] == "pass" for r in rows if r["terminal"] is not None
        ):
            if not started_no_terminal:
                epilogue = True
    censored = bool(started_no_terminal or pkg_terminal_missing)
    return {
        "tests": rows,
        "packages": [
            {
                "package": p["package"],
                "terminal": p["terminal"],
                "elapsed": p["elapsed"],
                "start": p["start"],
                "events": p["events"],
                "timing": event_timing(p["events"]),
            }
            for p in pkg_list
        ],
        "counters": {
            "packages": len(pkg_list),
            "top_level": len(top),
            "subtests": len(sub),
            "examples": len(examples),
            "fuzz": len(fuzz),
            "build_events": build,
            "unknown_valid": unknown,
            "parse_errors": len(errors),
            "started_without_terminal": len(started_no_terminal),
            "skips": len([r for r in rows if r["terminal"] == "skip"]),
        },
        "started_without_terminal": started_no_terminal,
        "tests_all_observed_pass": tests_all_pass and not started_no_terminal,
        "any_fail": any_fail,
        "epilogue_nonzero": epilogue,
        "censored": censored,
        "parse_errors": errors,
    }


def write_tests_jsonl(path, parsed):
    with open(path, "w", encoding="utf-8") as f:
        for rec in parsed["tests"]:
            f.write(json.dumps(rec, sort_keys=True) + "\n")


def source_snapshot(root):
    """Hash working bytes (including uncommitted inputs), without saving their contents."""
    root = Path(root)
    proc = subprocess.run(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
                          cwd=root, capture_output=True, check=False)
    hashes, errors = {}, []
    for raw in sorted(set(proc.stdout.split(b"\0"))):
        if not raw:
            continue
        rel = os.fsdecode(raw)
        path = root / rel
        selected = (path.suffix in (".go", ".mod", ".sum", ".s", ".h", ".c", ".syso")
                    or rel == "go.work" or rel in (
                        "scripts/ci-race-package-profile.py", "scripts/with-pg-env.sh",
                        "scripts/pg-test-env.sh", "scripts/cpu-quota.sh",
                        "scripts/ci-postgres-service.sh", ".github/workflows/race-package-profile.yml")
                    or any(rel.startswith(pkg + "/") for pkg in ALLOWED_PACKAGES))
        if not selected:
            continue
        try:
            hashes[rel] = sha256_file(path)
        except OSError:
            hashes[rel] = None
            errors.append(rel)
    return {"git": git_identity(root), "files": hashes,
            "scope": "workspace Go/build files, selected testdata and profiling helpers; working bytes",
            "ok": proc.returncode == 0 and bool(hashes) and not errors, "errors": errors}


def run_receipt(argv, cwd, out, label, env=None, sanitize=False, timeout=None):
    """One finite command with separate streams, live PID and monotonic/UTC receipt."""
    out = Path(out)
    rec = {"argv": argv, "cwd": str(cwd), "start_utc": utc_now(),
           "start_mono_ns": time.monotonic_ns(), "pid": None, "rc": None,
           "stdout": label + ".stdout", "stderr": label + ".stderr"}
    write_json(out / (label + ".receipt.json"), rec)
    try:
        child = subprocess.Popen(argv, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    except OSError as e:
        rec.update(started=False, error=type(e).__name__, rc=127)
        stdout, stderr = b"", b"launch failed\n"
    else:
        rec.update(started=True, pid=child.pid, identity=proc_identity(child.pid))
        write_json(out / (label + ".receipt.json"), rec)
        try:
            stdout, stderr = child.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            child.kill()
            stdout, stderr = child.communicate()
            rec["timeout"] = True
        rec["rc"] = child.returncode
    rec.update(end_utc=utc_now(), end_mono_ns=time.monotonic_ns())
    rec["elapsed_monotonic_ns"] = rec["end_mono_ns"] - rec["start_mono_ns"]
    if sanitize:
        # PG errors can echo a DSN, password or SQL literal. Do not persist those bytes
        # or their hashes. The phase, PID, RC and two stream lengths remain reviewable.
        rec["stream_policy"] = "payload_redacted_at_capture"
        rec["raw_stream_bytes"] = {"stdout": len(stdout), "stderr": len(stderr)}
        saved_out = b"[preparation stdout redacted]\n" if stdout else b""
        saved_err = b"[preparation stderr redacted]\n" if stderr else b""
    else:
        saved_out, saved_err = stdout, stderr
    (out / rec["stdout"]).write_bytes(saved_out)
    (out / rec["stderr"]).write_bytes(saved_err)
    rec["stream_sha256"] = {"stdout": sha256_bytes(saved_out), "stderr": sha256_bytes(saved_err)}
    write_json(out / (label + ".receipt.json"), rec)
    return rec, stdout, stderr


def go_list_inventory(root, out_dir):
    argv = [shutil.which("go") or "go", "list", "-json",
            *[spec["go_dir"] for spec in ALLOWED_PACKAGES.values()]]
    rec, stdout, _ = run_receipt(argv, Path(root) / "core", out_dir, "inventory",
                               sanitize=True, timeout=120)
    inventory = {"ok": False, "receipt": rec, "packages": []}
    try:
        text, decoder = stdout.decode("utf-8"), json.JSONDecoder()
        objects = []
        while text.strip():
            obj, end = decoder.raw_decode(text.lstrip())
            objects.append(obj)
            text = text.lstrip()[end:]
        expected = {v["import_path"]: k for k, v in ALLOWED_PACKAGES.items()}
        if rec["rc"] != 0 or len(objects) != 3 or {o.get("ImportPath") for o in objects} != set(expected):
            raise ValueError("closed_inventory_mismatch")
        for obj in objects:
            if obj.get("Error") or obj.get("Incomplete"):
                raise ValueError("inventory_incomplete")
            package = expected[obj["ImportPath"]]
            directory = (Path(root) / package).resolve()
            if Path(obj["Dir"]).resolve() != directory:
                raise ValueError("inventory_directory_mismatch")
            files = {}
            for key in ("GoFiles", "CgoFiles", "TestGoFiles", "XTestGoFiles", "EmbedFiles", "TestEmbedFiles", "XTestEmbedFiles"):
                files[key] = []
                for name in obj.get(key) or []:
                    path = directory / name
                    if not path.resolve().is_relative_to(directory):
                        raise ValueError("inventory_path_outside_package")
                    files[key].append({"path": path.relative_to(root).as_posix(), "sha256": sha256_file(path)})
            inventory["packages"].append({"import_path": obj["ImportPath"], "package": package, "files": files})
        inventory["ok"] = True
    except (ValueError, KeyError, TypeError, OSError) as e:
        inventory["reason"] = "invalid_or_incomplete_closed_inventory"
        inventory["error_type"] = type(e).__name__
    write_json(Path(out_dir) / "inventory.json", inventory)
    return inventory


def inventory_matches(inventory, snapshot):
    if not inventory.get("ok") or not snapshot.get("ok"):
        return False
    expected = {v["import_path"] for v in ALLOWED_PACKAGES.values()}
    rows = inventory.get("packages", [])
    if len(rows) != 3 or {r.get("import_path") for r in rows} != expected:
        return False
    if any(not any(row.get("files", {}).values()) for row in rows):
        return False
    return all(snapshot.get("files", {}).get(f["path"]) == f.get("sha256") and f.get("sha256")
               for row in rows for files in row.get("files", {}).values() for f in files)


def inspect_postgres():
    cid = os.environ.get("PG_CONTAINER_ID") or os.environ.get("OLIVARES_PG_CONTAINER_ID")
    if not cid:
        return {"observed_pg_resources": "unavailable", "reason": "no_container_id"}
    fmt = (
        "{{.Id}} {{.State.Pid}} {{.Image}} {{.State.Health.Status}} "
        "{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}} "
        "{{.HostConfig.MemorySwap}} {{.HostConfig.PidsLimit}} {{.State.Status}}"
    )
    try:
        p = subprocess.run(
            ["docker", "inspect", "--format", fmt, cid],
            capture_output=True,
            text=True,
            check=False,
            timeout=15,
        )
    except OSError:
        return {"observed_pg_resources": "unavailable", "reason": "docker_absent"}
    if p.returncode != 0:
        return {
            "observed_pg_resources": "unavailable",
            "reason": "inspect_failed",
            "rc": p.returncode,
        }
    parts = p.stdout.strip().split()
    if len(parts) < 9:
        return {"observed_pg_resources": "unavailable", "reason": "format_mismatch"}
    return {
        "observed_pg_resources": "ok",
        "id": parts[0],
        "pid": parts[1],
        "image": parts[2],
        "health": parts[3],
        "nano_cpus": parts[4],
        "memory": parts[5],
        "memory_swap": parts[6],
        "pids_limit": parts[7],
        "status": parts[8],
        "format": "closed",
    }


class Sampler:
    def __init__(self, out_dir, go_pid, proc_root="/proc", sysfs_root=None, interval=5.0, role="go"):
        self.out = Path(out_dir) / "resources.jsonl"
        self.pid, self.role = go_pid, role
        self.proc_root, self.sysfs_root = proc_root, sysfs_root
        self.interval = interval
        self.stop = threading.Event()
        self.thread, self.previous, self.previous_host = None, None, None
        self.errors = 0
        self.identity = proc_identity(go_pid, proc_root)
        self.go_start = self.identity.get("starttime")

    def _match(self, pid, start):
        ident = proc_identity(pid, self.proc_root)
        if ident.get("state") == "ok" and start is not None and ident.get("starttime") != start:
            return {"state": "gone", "pid": pid, "reason": "pid_reused",
                    "new_starttime": ident.get("starttime")}
        if ident.get("state") == "ok":
            ident["cgroup_tree"] = walk_cgroup(pid, self.proc_root, self.sysfs_root)
        return ident

    def sample(self, kind="interval"):
        try:
            current = self._match(self.pid, self.go_start)
            rec = {"kind": kind, "producer": self.role, "utc": utc_now(),
                   "mono_ns": time.monotonic_ns(), "process": current,
                   "host": host_sample(self.proc_root),
                   "delta": resource_delta(self.previous, current)}
            rec["host_delta"] = host_delta(self.previous_host, rec["host"])
            append_jsonl(self.out, rec)
            self.previous = current
            self.previous_host = rec["host"]
        except Exception as e:
            self.errors += 1
            try:
                append_jsonl(self.out, {"kind": "error", "producer": self.role,
                                       "utc": utc_now(), "error": type(e).__name__})
            except OSError:
                pass

    def loop(self):
        while not self.stop.wait(self.interval):
            self.sample()

    def start(self):
        # Synchronous initial sample: short-lived children cannot miss it due to scheduling.
        self.sample("initial")
        self.thread = threading.Thread(target=self.loop, name="resource-sampler", daemon=True)
        self.thread.start()

    def finish(self):
        self.stop.set()
        if self.thread:
            self.thread.join(timeout=self.interval + 2)
        if self.thread and self.thread.is_alive():
            self.errors += 1
        self.sample("final")
        return {"errors": self.errors, "finished": not self.thread or not self.thread.is_alive()}


def pump(src, dest_file, dest_stream):
    while True:
        chunk = src.read1(65536)
        if not chunk:
            break
        dest_file.write(chunk)
        dest_file.flush()
        if dest_stream is not None:
            dest_stream.write(chunk)
            dest_stream.flush()


def progress_line(raw):
    try:
        obj = json.loads(raw)
    except (json.JSONDecodeError, UnicodeDecodeError):
        return
    if not isinstance(obj, dict):
        return
    action = obj.get("Action")
    test = obj.get("Test")
    if action in ("pass", "fail", "skip", "run") and test:
        elapsed = obj.get("Elapsed")
        extra = "" if elapsed is None else f" elapsed={elapsed}"
        sys.stdout.write(f"{action.upper()} {test}{extra}\n")
        sys.stdout.flush()


def pprof_once(out_dir):
    out = Path(out_dir)
    existing = read_json(out / "pprof-receipts.json")
    if existing is not None:
        return existing
    binary, profile = out / "package.test", out / "cpu.pprof"
    result = {"status": "absent", "receipts": []}
    if binary.is_file() and profile.is_file() and profile.stat().st_size:
        inputs = {p.name: sha256_file(p) for p in (binary, profile)}
        for label, extra in (("flat", []), ("cum", ["-cum"])):
            argv = [shutil.which("go") or "go", "tool", "pprof", "-top", *extra, str(binary), str(profile)]
            rec, _, _ = run_receipt(argv, out, out, "pprof-" + label, timeout=120)
            rec["input_sha256"] = inputs
            rec["label"] = label
            rec["inputs_unchanged"] = inputs == {p.name: sha256_file(p) for p in (binary, profile)}
            stdout_name = "pprof-top.txt" if label == "flat" else "pprof-cumulative.txt"
            (out / stdout_name).write_bytes((out / rec["stdout"]).read_bytes())
            result["receipts"].append(rec)
        result["status"] = "ok" if all(r["rc"] == 0 and r["inputs_unchanged"] for r in result["receipts"]) else "incomplete"
    elif profile.exists():
        result["status"] = "incomplete"
    write_json(out / "pprof-receipts.json", result)
    return result


def write_sha256sums(out_dir):
    root = Path(out_dir)
    lines = []
    for path in sorted(root.rglob("*")):
        if not path.is_file() or path.name == "SHA256SUMS":
            continue
        rel = path.relative_to(root).as_posix()
        lines.append(f"{sha256_file(path)}  {rel}\n")
    (root / "SHA256SUMS").write_text("".join(lines), encoding="utf-8")


def load_streams(out_dir):
    out = Path(out_dir)
    stdout = ""
    stderr = ""
    sp = out / "go.stdout.raw.jsonl"
    if sp.is_file():
        stdout = sp.read_text(encoding="utf-8", errors="replace")
    bp = out / "binary.stderr.raw"
    if bp.is_file():
        stderr = bp.read_text(encoding="utf-8", errors="replace")
    return stdout, stderr


def producer_valid(rec):
    return (rec.get("started") is True and isinstance(rec.get("pid"), int) and rec["pid"] > 0
            and str(rec.get("starttime", "")).isdigit() and isinstance(rec.get("rc"), int)
            and rec.get("start_utc") is not None and rec.get("end_utc") is not None
            and isinstance(rec.get("elapsed_monotonic_ns"), int) and rec["elapsed_monotonic_ns"] > 0)


def resource_window(before, after):
    """Verify recorded deltas for one adjacent pair; never fill a missing delta."""
    window = {"from_line": before["_line_no"], "to_line": after["_line_no"],
              "from_state": before.get("process", {}).get("state", "unknown"),
              "to_state": after.get("process", {}).get("state", "unknown"),
              "elapsed_monotonic_ns": None, "compatible": False, "metrics": {}}
    metrics = window["metrics"]

    def metric(name, delta, required=()):
        fact = {k: delta[k] for k in ("state", "reason") if k in delta}
        if delta.get("state") == "ok" and (not delta.get("values") or
                                             not set(required).issubset(delta["values"])):
            fact = {"state": "unavailable", "reason": "required_counters_missing"}
        metrics[name] = fact or {"state": "unavailable", "reason": "delta_absent"}

    try:
        t0, t1 = before.get("mono_ns"), after.get("mono_ns")
        time_ok = isinstance(t0, int) and isinstance(t1, int) and t1 > t0
        if time_ok:
            window["elapsed_monotonic_ns"] = t1 - t0
        recorded = after.get("delta", {})
        host_recorded = after.get("host_delta", {})
        # Existing R1 arithmetic verifies identities, counter sets and monotonicity.
        # Recomputed values are used for comparison only, not persisted as new evidence.
        verified = (recorded == resource_delta(before.get("process"), after.get("process", {}))
                    and host_recorded == host_delta(before.get("host"), after.get("host", {})))
        metric("process_cpu_ticks", recorded.get("process_cpu_ticks", recorded), ("utime", "stime"))
        cg = recorded.get("cgroup", recorded)
        nodes = after.get("process", {}).get("cgroup_tree", {}).get("ancestors", [])
        delta_nodes = cg.get("nodes", [])
        if cg.get("state") != "compatible" or not nodes or len(nodes) != len(delta_nodes):
            metric("cgroup", cg)
        else:
            for node, delta_node in zip(nodes, delta_nodes):
                for key, delta in delta_node.get("counters", {}).items():
                    # Unsupported v1 interfaces remain absent, not synthetic zero deltas.
                    if node.get(key, {}).get("state") != "ok":
                        continue
                    required = ()
                    if key == "cpu.stat" and node.get("leaf"):
                        required = (("usage_usec", "nr_periods", "nr_throttled", "throttled_usec")
                                    if node.get("version") == "v2" else
                                    ("nr_periods", "nr_throttled", "throttled_time"))
                    elif key.endswith(".pressure"):
                        required = ("some.total",)
                    metric(node["path"] + "/" + key, delta, required)
            if not any(name.endswith("/cpu.stat") for name in metrics):
                metric("cgroup_cpu", {})
        metric("host_cpu_ticks", host_recorded.get("cpu_ticks", {}), ("user", "system", "idle"))
        for key in ("pressure_cpu", "pressure_io", "pressure_memory"):
            metric("host_" + key, host_recorded.get(key, {}), ("some.total",))
        window["recorded_deltas_verified"] = verified
        window["compatible"] = time_ok and verified and all(m.get("state") == "ok" for m in metrics.values())
        if not time_ok:
            window["reason"] = "sample_clock_unknown_or_not_increasing"
        elif not verified:
            window["reason"] = "recorded_deltas_missing_or_mismatched"
    except (KeyError, TypeError, ValueError, AttributeError):
        window["reason"] = "sample_or_delta_unreadable"
    return window


def resources_integrity(out, producers):
    path = out / "resources.jsonl"
    rows, problems = [], []
    if not path.is_file():
        return {"complete": False, "problems": ["samples_absent"]}
    windows, point_samples = {}, {}
    for line_no, line in enumerate(path.read_text().splitlines(), 1):
        try:
            row = json.loads(line)
            if not isinstance(row, dict):
                raise ValueError()
            row["_line_no"] = line_no
            rows.append(row)
        except ValueError:
            problems.append("sample_invalid_json")
    for role, producer in producers.items():
        role_rows = [r for r in rows if r.get("producer") == role]
        windows[role] = [resource_window(a, b) for a, b in zip(role_rows, role_rows[1:])]
        point_samples[role] = sum(r.get("process", {}).get("state") == "ok" for r in role_rows)
        if not any(w["compatible"] for w in windows[role]):
            problems.append(role + "_compatible_delta_window_missing")
        for window, row in zip(windows[role], role_rows[1:]):
            # A final exited process bounds the sampled coverage; it supplies no rate.
            # Missing/reset/migrated intervals inside that coverage remain incomplete.
            if not window["compatible"] and not (row.get("kind") == "final" and
                                                  row.get("process", {}).get("state") == "gone"):
                problems.append(role + "_delta_window_incomplete")
        if not any(r.get("kind") == "initial" for r in role_rows) or not any(r.get("kind") == "final" for r in role_rows):
            problems.append(role + "_sample_endpoints_missing")
        observed = False
        for row in role_rows:
            if row.get("kind") == "error":
                problems.append(role + "_sampler_error")
                continue
            rec = row.get("process", {})
            if rec.get("state") == "gone" and row.get("kind") == "final":
                continue  # exited process is explicitly gone, never zero
            if rec.get("state") != "ok":
                problems.append(role + "_process_unavailable")
                continue
            if rec.get("pid") != producer.get("pid") or str(rec.get("starttime")) != str(producer.get("starttime")):
                problems.append(role + "_identity_mismatch")
            observed = True
            cg = rec.get("cgroup_tree", {})
            if not cg.get("visible_limit_complete"):
                problems.append(role + "_visible_limits_incomplete")
            # Counters needed for CPU/throttling attribution must actually be present.
            cpu_nodes = [n for n in cg.get("ancestors", []) if n.get("leaf") and
                         (n.get("version") == "v2" or "cpu" in n.get("controllers", []))]
            if not cpu_nodes or any(n.get("cpu.stat", {}).get("state") != "ok" for n in cpu_nodes):
                problems.append(role + "_cpu_counters_unavailable")
            for node in cg.get("ancestors", []):
                if not node.get("leaf"):
                    continue
                required = ("memory.current", "memory.events", "cpu.pressure", "io.pressure", "memory.pressure") if node.get("version") == "v2" else (
                    ("memory.usage_in_bytes", "memory.failcnt") if "memory" in node.get("controllers", []) else ())
                if any(node.get(key, {}).get("state") != "ok" for key in required):
                    problems.append(role + "_cgroup_components_unavailable")
            for key in ("io", "schedstat"):
                if rec.get(key, {}).get("state") != "ok":
                    problems.append(role + "_" + key + "_unavailable")
            host = row.get("host", {})
            for key in ("stat", "loadavg", "pressure_cpu", "pressure_io", "pressure_memory"):
                if host.get(key, {}).get("state") != "ok":
                    problems.append("host_" + key + "_unavailable")
        if not observed:
            problems.append(role + "_never_observed")
    return {"complete": not problems, "problems": sorted(set(problems)),
            "point_samples": point_samples, "windows": windows,
            "window_scope": "adjacent observed samples only; final/gone is an unmeasured tail, not whole-lifetime coverage",
            "scope": "visible namespaces only; no host ceiling certification"}


def profile_integrity(out, binary):
    result = read_json(out / "pprof-receipts.json", {})
    receipts = result.get("receipts", [])
    inputs = {}
    for name in ("package.test", "cpu.pprof"):
        path = out / name
        if not path.is_file() or not path.stat().st_size:
            return {"complete": False, "state": "absent_or_empty"}
        inputs[name] = sha256_file(path)
    valid = result.get("status") == "ok" and len(receipts) == 2 and {r.get("label") for r in receipts} == {"flat", "cum"}
    for rec in receipts:
        valid = valid and (rec.get("rc") == 0 and isinstance(rec.get("pid"), int) and rec["pid"] > 0 and rec.get("start_utc")
                           and rec.get("end_utc") and rec.get("elapsed_monotonic_ns", 0) > 0
                           and rec.get("input_sha256") == inputs and rec.get("inputs_unchanged") is True)
        for stream in ("stdout", "stderr"):
            name = rec.get(stream)
            path = out / name if name and Path(name).name == name else None
            valid = valid and path is not None and path.is_file() and (
                sha256_file(path) == rec.get("stream_sha256", {}).get(stream))
    valid = valid and binary.get("sha256") == inputs["package.test"]
    return {"complete": bool(valid), "state": "ok" if valid else "incomplete"}


def derive_summary(out_dir):
    out = Path(out_dir)
    exit_obj = read_json(out / "exit.json", {}) or {}
    prepare = read_json(out / "prepare.json", {}) or {}
    source = read_json(out / "source.json", {}) or {}
    invocations = read_json(out / "invocations.json", {}) or {}
    go = read_json(out / "go.identity.json", {}) or {}
    binary = read_json(out / "binary.identity.json", {}) or {}
    wrapper = invocations.get("wrapper", {})
    stdout, bin_err = load_streams(out)
    events, errors = parse_json_stream(stdout)
    parsed = summarize_events(events, errors)
    write_tests_jsonl(out / "tests.jsonl", parsed)
    sched = parse_scheduler_text(bin_err)
    with (out / "runtime-scheduler.jsonl").open("w") as f:
        for row in sched.get("lines", []):
            f.write(json.dumps({**row, "pid": binary.get("pid"), "starttime": binary.get("starttime"),
                                "binary_start_utc": binary.get("start_utc")}, sort_keys=True) + "\n")
    go_started = go.get("started") is True
    binary_started = binary.get("started") is True
    go_rc = go.get("rc")
    expected = ALLOWED_PACKAGES.get(source.get("package"), {}).get("import_path")
    package = next((p for p in parsed["packages"] if p["package"] == expected), {})
    terminal = package.get("terminal")
    producer_ok = producer_valid(go) and producer_valid(binary)
    terminal_ok = bool(expected and package.get("start") and terminal in ("pass", "fail")
                       and len(parsed["packages"]) == 1)
    tests_col = "not_started" if not go_started else "unknown"
    if producer_ok and terminal_ok:
        if terminal == "fail" or go_rc != 0 or binary.get("rc") != 0 or parsed["any_fail"]:
            tests_col = "fail"
        elif not parsed["censored"] and not errors:
            tests_col = "pass"
    elif go_started and (go_rc not in (0, None) or parsed["any_fail"]):
        tests_col = "fail"
    cancelled = bool(exit_obj.get("cancelled") or go.get("signal") or binary.get("signal"))
    alarm = bool(re.search(r"panic: test timed out|SIGQUIT:|fatal error:", stdout + bin_err))
    censored = parsed["censored"] or cancelled or alarm
    before, after = source.get("before", {}), source.get("after", {})
    inventory = read_json(out / "inventory.json", {}) or {}
    source_ok = (before.get("ok") is True and after.get("ok") is True and before == after
                 and source.get("source_changed_during_measure") is False
                 and inventory_matches(inventory, before) and inventory_matches(inventory, after)
                 and source.get("inventory") == inventory)
    wrapper_for_resources = {**wrapper.get("identity", {}), **wrapper}
    resources = resources_integrity(out, {"wrapper": wrapper_for_resources, "go": go, "binary": binary})
    profile = profile_integrity(out, binary)
    producer_coherent = (exit_obj.get("go_started") == go_started and exit_obj.get("go_rc") == go_rc
                          and exit_obj.get("binary_rc") == binary.get("rc")
                          and exit_obj.get("wrapper_rc") == wrapper.get("rc")
                          and invocations.get("go") == go and invocations.get("binary") == binary)
    streams_ok = all((out / name).is_file() for name in (
        "go.stdout.raw.jsonl", "go.stderr.raw", "binary.stdout.raw", "binary.stderr.raw",
        "wrapper.stdout.raw.jsonl", "wrapper.stderr.raw")) and all(
            r.get("streams_complete") is True for r in (wrapper, go, binary))
    for producer, rec in (("wrapper", wrapper), ("go", go), ("binary", binary)):
        for stream in ("stdout", "stderr"):
            filename = producer + "." + stream + ".raw"
            if stream == "stdout" and producer in ("wrapper", "go"):
                filename += ".jsonl"
            path = out / filename
            streams_ok = streams_ok and path.is_file() and rec.get("stream_sha256", {}).get(stream) == sha256_file(path)
    runtime_ok = (producer_valid(binary) and sched["runtime_attributable"]
                  and binary.get("godebug_schedtrace") == "5000" and binary.get("godebug_scheddetail") == "0")
    wrapper_compatible = (isinstance(go_rc, int) and wrapper.get("rc") == (go_rc if go_rc >= 0 else 128 - go_rc))
    components = {"producer": bool(producer_ok and producer_coherent and wrapper.get("started") and wrapper_compatible),
                  "expected_package_terminal": terminal_ok, "parser": not errors,
                  "streams": streams_ok, "runtime": bool(runtime_ok),
                  "resources": resources["complete"] and exit_obj.get("sampler_errors") == 0
                  and all(r.get("sampler", {}).get("finished") is True and r["sampler"].get("errors") == 0 for r in (wrapper, go, binary)),
                  "profile": profile["complete"], "source_inventory": bool(source_ok),
                  "preparation": prepare.get("ok") is True, "uncensored": not censored,
                  "not_cancelled": not cancelled}
    complete = all(components.values())
    observation = "complete" if complete else "incomplete"
    if prepare.get("ok") is False and not go_started:
        observation = "prepare_failed"
    elif not go_started:
        observation = "probe_not_started"
    elif censored:
        observation = "censored"
    phases = dict(exit_obj.get("phases") or {})
    test_events = [e for t in parsed["tests"] for e in t["events"]]
    stamps = sorted(e["time"] for e in test_events if e.get("time"))
    phases.update(first_test_event_utc=stamps[0] if stamps else None,
                  last_test_event_utc=stamps[-1] if stamps else None,
                  package_events=package.get("events", []))
    summary = {"observation": observation, "tests": tests_col, "inspection_useful": bool(events or prepare or exit_obj),
               "observation_complete": complete, "integrity_ok": complete, "components": components,
               "go_started": go_started, "go_rc": go_rc, "wrapper_rc": exit_obj.get("wrapper_rc"),
               "binary_started": binary_started, "binary_rc": binary.get("rc"),
               "runtime_attributable": bool(runtime_ok),
               "gomaxprocs_from": "binary.stderr.raw" if runtime_ok else None,
               "gomaxprocs_observed": sched.get("gomaxprocs_observed", []), "go_env_GOMAXPROCS_used": False,
               "cpu_profile": profile["state"], "resources": resources, "cancelled": cancelled,
               "metadata_errors": {name: obj["_read_error"] for name, obj in (("exit", exit_obj), ("prepare", prepare), ("source", source), ("invocations", invocations), ("go", go), ("binary", binary), ("inventory", inventory)) if obj.get("_read_error")},
               "counters": parsed["counters"], "epilogue_nonzero": parsed["epilogue_nonzero"],
               "censored": censored, "parse_errors": errors, "phases": phases}
    write_json(out / "summary.json", summary)
    return summary, parsed, sched


def instrument_rc(summary, exit_obj):
    obs = summary.get("observation")
    go_rc = exit_obj.get("go_rc")
    if obs == "prepare_failed":
        return RC_PREPARE
    if obs == "probe_not_started":
        return RC_PROBE_NOT_STARTED
    if go_rc not in (0, None) and exit_obj.get("go_started"):
        return go_rc if isinstance(go_rc, int) else RC_TEST_FAIL
    if obs == "unknown":
        return RC_UNKNOWN
    if obs == "incomplete":
        return RC_INCOMPLETE
    if obs == "censored":
        return RC_CENSORED
    if summary.get("tests") == "fail":
        return RC_TEST_FAIL
    if obs == "complete" and summary.get("tests") == "pass":
        return RC_PASS
    if obs == "complete":
        return RC_TEST_FAIL if summary.get("tests") != "pass" else RC_PASS
    return RC_UNKNOWN


def effective_environment():
    debug = {}
    omitted = 0
    # Preserve arbitrary runtime settings in the child, but record only known numeric settings.
    allowed = {"schedtrace", "scheddetail", "containermaxprocs", "updatemaxprocs", "gctrace", "asyncpreemptoff"}
    for token in os.environ.get("GODEBUG", "").split(","):
        key, sep, value = token.partition("=")
        if key in allowed and sep and value.isdigit():
            debug[key] = value
        elif token:
            omitted += 1
    return {"GOFLAGS": validate_goflags(), "GORACE": validate_gorace(),
            "GOMAXPROCS": gomaxprocs_inherited(),
            "GODEBUG": {"numeric_settings": debug, "unrecorded_settings": omitted},
            "PG": dsn_presence()}


def cmd_exec(out_dir, binary_argv, producer="binary"):
    out = Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    identity_file = out / (producer + ".identity.json")
    if identity_file.exists():
        return RC_PREPARE  # one launch, including failed launches; never overwrite
    if not binary_argv:
        write_json(identity_file, {"started": False, "reason": "missing_argv"})
        return 2
    env = os.environ.copy()
    inherited = effective_environment()
    if producer == "binary":
        env["GODEBUG"] = merge_godebug(env.get("GODEBUG"))
    if producer == "go" and not env.get("GOFLAGS"):
        # Empty/unset environment can defer to Go's persistent config. Query only this
        # selector after the probe, keep its payload sanitized, and reject selection flags.
        rec, flags, _ = run_receipt([binary_argv[0], "env", "GOFLAGS"], Path.cwd(), out,
                                    "post-probe-goflags", sanitize=True, timeout=30)
        value = flags.decode("utf-8", errors="replace").strip()
        valid = rec["rc"] == 0 and (not value or bool(GOFLAGS_P.fullmatch(value)))
        inherited["GOFLAGS"] = {"ok": valid, "origin": "go_env_after_probe",
                                "state": "empty" if not value else "p_N" if valid else "incompatible"}
        if valid and value:
            inherited["GOFLAGS"]["value"] = value
        if not valid:
            write_json(identity_file, {"started": False, "reason": "configured_goflags_invalid",
                                       "effective_environment": inherited})
            return RC_PREPARE
    elif producer == "go":
        inherited["GOFLAGS"]["origin"] = "environment_after_probe"
    if producer == "go" and not prepare_overrides()["ok"]:
        write_json(identity_file, {"started": False, "reason": "post_probe_environment_invalid",
                                   "effective_environment": inherited})
        return RC_PREPARE
    stdout_path = out / ("go.stdout.raw.jsonl" if producer == "go" else "binary.stdout.raw")
    stderr_path = out / (producer + ".stderr.raw")
    start_mono, start_utc = time.monotonic_ns(), utc_now()
    obj = {"producer": producer, "argv": binary_argv, "cwd": os.getcwd(),
           "start_mono_ns": start_mono, "start_utc": start_utc,
           "supervisor": proc_identity(os.getpid()), "effective_environment": inherited,
           "started": False, "rc": None}
    if producer == "binary":
        obj.update(godebug_schedtrace="5000", godebug_scheddetail="0",
                   gomaxprocs_set_by_adapter=False)
    write_json(identity_file, obj)
    cancelled, pump_errors = {"signal": None}, []
    child = None
    def handle(signum, _frame):
        cancelled["signal"] = signum
        if child is not None:
            try:
                child.send_signal(signum)
            except OSError:
                pass
    previous = {sig: signal.signal(sig, handle) for sig in (signal.SIGINT, signal.SIGTERM)}
    with stdout_path.open("wb") as stdout_f, stderr_path.open("wb") as stderr_f:
        try:
            child = subprocess.Popen(binary_argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env)
        except OSError as e:
            obj.update(error=type(e).__name__, rc=127)
        else:
            if cancelled["signal"] is not None:
                child.send_signal(cancelled["signal"])
            ident = proc_identity(child.pid)
            obj.update(started=True, pid=child.pid, starttime=ident.get("starttime"),
                       ppid=ident.get("ppid"), exe=ident.get("exe"),
                       sha256=sha256_file(binary_argv[0]) if Path(binary_argv[0]).is_file() else None)
            write_json(identity_file, obj)
            sampler = Sampler(out, child.pid, role=producer)
            sampler.start()
            def transfer(src, dest, stream):
                try:
                    pump(src, dest, stream)
                except Exception as e:
                    pump_errors.append(type(e).__name__)
            threads = [threading.Thread(target=transfer, args=args, daemon=True) for args in (
                (child.stdout, stdout_f, sys.stdout.buffer), (child.stderr, stderr_f, sys.stderr.buffer))]
            for thread in threads:
                thread.start()
            obj["rc"] = child.wait()
            for thread in threads:
                thread.join(timeout=5)
            obj["streams_complete"] = not pump_errors and not any(t.is_alive() for t in threads)
            obj["sampler"] = sampler.finish()
    for sig, handler in previous.items():
        signal.signal(sig, handler)
    obj.update(end_utc=utc_now(), end_mono_ns=time.monotonic_ns(),
               signal=cancelled["signal"], pump_errors=pump_errors, supervisor_pid=os.getpid())
    obj["stream_sha256"] = {"stdout": sha256_file(stdout_path), "stderr": sha256_file(stderr_path)}
    obj["elapsed_monotonic_ns"] = obj["end_mono_ns"] - start_mono
    write_json(identity_file, obj)
    return obj["rc"]


def prepare_failure(out, reason, rc=RC_PREPARE):
    prep = read_json(out / "prepare.json", {})
    prep.update(ok=False, cause=reason, end_utc=utc_now(), end_mono_ns=time.monotonic_ns())
    write_json(out / "prepare.json", prep)
    write_json(out / "exit.json", {"go_started": False, "go_rc": None, "wrapper_rc": None,
                                   "binary_rc": None, "cause": "prepare", "prepare_rc": rc})
    source = read_json(out / "source.json", {})
    source["after"] = source_snapshot(repo_root())
    source["source_changed_during_measure"] = source.get("before") != source["after"]
    write_json(out / "source.json", source)
    derive_summary(out)
    write_sha256sums(out)
    return rc if rc > 0 else RC_PREPARE


def cmd_run(package, out_dir, phase="measure"):
    root = repo_root()
    spec = ALLOWED_PACKAGES.get(package)
    out = Path(out_dir).resolve()
    if spec is None:
        sys.stderr.write("prepare: package is not one of the three closed entries\n")
        return RC_PREPARE
    if phase == "allocate":
        if out.exists() and any(out.iterdir()):
            sys.stderr.write("prepare: allocation already exists\n")
            return RC_PREPARE
        out.mkdir(parents=True, exist_ok=True)
        write_json(out / "source.json", {"package": package, "spec": spec,
                   "before": source_snapshot(root), "assignment": assignment_identity(),
                   "instrument_sha256": sha256_file(Path(__file__))})
        write_json(out / "prepare.json", {"ok": None, "stages": {}, "utc": utc_now(),
                   "mono_ns": time.monotonic_ns(), "allocation_pid": os.getpid(),
                   "package_started": False})
        write_sha256sums(out)
        return 0
    if not (out / "source.json").is_file():
        # Standalone run still leaves an allocation/failure receipt, but cannot bypass PG stages.
        if cmd_run(package, out, phase="allocate") != 0:
            return RC_PREPARE
    source = read_json(out / "source.json", {})
    prep = read_json(out / "prepare.json", {})
    if source.get("package") != package or (phase != "finalize-prepare" and (out / "invocations.json").exists()):
        return RC_PREPARE
    if phase == "finalize-prepare":
        outcomes = {key: os.environ.get("RPP_" + key.upper(), "unknown")
                    for key in ("sha", "setup", "tmpdir", "resolve", "provision", "inventory", "measure")}
        prep["workflow_outcomes"] = {k: v if v in ("success", "failure", "cancelled", "skipped") else "unknown"
                                     for k, v in outcomes.items()}
        write_json(out / "prepare.json", prep)
        if not (out / "exit.json").exists():
            if (out / "invocations.json").exists():
                # Cancellation may have interrupted run after Go/test started. Preserve that fact;
                # do not fabricate a no-start receipt or termination time for an unfinished child.
                go = read_json(out / "go.identity.json", {})
                binary = read_json(out / "binary.identity.json", {})
                write_json(out / "exit.json", {"go_started": go.get("started", False),
                           "go_rc": go.get("rc"), "binary_rc": binary.get("rc"), "wrapper_rc": None,
                           "cancelled": "cancelled" in outcomes.values(), "cause": "measure_interrupted",
                           "termination_utc": None})
            else:
                return prepare_failure(out, "workflow_preparation_not_finished")
        write_sha256sums(out)
        return 0
    if (out / "exit.json").exists() or prep.get("ok") is False:
        return RC_PREPARE
    overrides = prepare_overrides()
    prep["overrides"] = overrides
    write_json(out / "prepare.json", prep)
    if not overrides["ok"]:
        return prepare_failure(out, "incompatible_overrides")
    expected = os.environ.get("EXPECTED_SHA", "").lower()
    actual = git_identity(root)
    if not HEX40.fullmatch(expected) or actual.get("commit") != expected or os.environ.get("GITHUB_SHA") != expected:
        return prepare_failure(out, "sha_guard")
    stages = prep.setdefault("stages", {})
    if phase in ("resolve", "provision"):
        if phase in stages or (phase == "provision" and stages.get("resolve", {}).get("rc") != 0):
            return prepare_failure(out, "preparation_stage_order")
        rec, _, _ = run_receipt(["bash", str(root / "scripts/ci-postgres-service.sh"), phase],
                                root, out, "prepare-" + phase, sanitize=True)
        stages[phase] = rec
        write_json(out / "prepare.json", prep)
        if rec["rc"] != 0:
            return prepare_failure(out, "postgres_" + phase, rec["rc"])
        return 0
    if stages.get("provision", {}).get("rc") != 0:
        return prepare_failure(out, "postgres_not_provisioned")
    if phase == "inventory":
        if (out / "inventory.json").exists():
            return prepare_failure(out, "inventory_already_attempted")
        # Go also reads persistent GOFLAGS. Capture this one selector without dumping go env.
        rec, flags, _ = run_receipt([shutil.which("go") or "go", "env", "GOFLAGS"], root, out,
                                    "effective-goflags", sanitize=True, timeout=30)
        value = flags.decode("utf-8", errors="replace").strip()
        prep["configured_GOFLAGS"] = {"ok": rec["rc"] == 0 and (not value or bool(GOFLAGS_P.fullmatch(value))),
                                      "value": value if GOFLAGS_P.fullmatch(value) else None,
                                      "state": "empty" if not value else "set"}
        write_json(out / "prepare.json", prep)
        if not prep["configured_GOFLAGS"]["ok"]:
            return prepare_failure(out, "configured_goflags_incompatible")
        inventory = go_list_inventory(root, out)
        source["inventory"] = inventory
        source["inventory_inputs"] = source_snapshot(root)
        write_json(out / "source.json", source)
        if not inventory_matches(inventory, source["inventory_inputs"]):
            return prepare_failure(out, "inventory_failed")
        stages["inventory"] = {"rc": 0, "utc": utc_now()}
        write_json(out / "prepare.json", prep)
        return 0
    inventory = read_json(out / "inventory.json", {})
    inputs = source_snapshot(root)
    if not inventory_matches(inventory, inputs) or inputs != source.get("inventory_inputs") or inputs != source.get("before"):
        return prepare_failure(out, "source_or_inventory_changed")
    source["go"] = go_tool_facts()
    write_json(out / "source.json", source)
    prep.update(ok=True, tmpdir=tmpdir_facts(os.environ.get("TMPDIR")), cache=cache_volumes(),
                cpu_quota_aux=cpu_quota_aux(root), pg_inspect=inspect_postgres())
    write_json(out / "prepare.json", prep)
    test_exec = write_test_exec(out, Path(__file__).resolve(), sys.executable)
    go_exec = write_test_exec(out, Path(__file__).resolve(), sys.executable, go_exe=source["go"].get("path"))
    argv = ["bash", str(root / "scripts/with-pg-env.sh"), go_exec["path"], "test",
            "-race", "-json", "-count=1", "-timeout=60m",
            "-cpuprofile=" + str(out / "cpu.pprof"), "-o=" + str(out / "package.test"),
            "-exec=" + test_exec["path"], spec["go_dir"]]
    wrapper = {"argv": argv, "cwd": str(root / "core"), "started": False, "rc": None,
               "start_utc": utc_now(), "start_mono_ns": time.monotonic_ns(),
               "phase_rc": None, "phase_rc_note": "wrapper execs adapter after probe; no separate shell exit on transition"}
    invocations = {"wrapper": wrapper, "adapters": [test_exec, go_exec], "recorded_not_reconstructed": True}
    write_json(out / "invocations.json", invocations)
    cancel, child, errors = {"signal": None}, None, []
    def handle(signum, _frame):
        cancel["signal"] = signum
        if child is not None:
            try:
                os.killpg(child.pid, signum)
            except OSError:
                pass
    previous = {sig: signal.signal(sig, handle) for sig in (signal.SIGINT, signal.SIGTERM)}
    with (out / "wrapper.stdout.raw.jsonl").open("wb") as stdout, (out / "wrapper.stderr.raw").open("wb") as stderr:
        try:
            child = subprocess.Popen(argv, cwd=root / "core", stdout=subprocess.PIPE,
                                     stderr=subprocess.PIPE, start_new_session=True)
        except OSError as e:
            wrapper.update(error=type(e).__name__, rc=127)
        else:
            wrapper.update(started=True, pid=child.pid, identity=proc_identity(child.pid))
            write_json(out / "invocations.json", invocations)
            sampler = Sampler(out, child.pid, role="wrapper")
            sampler.start()
            def copy_error():
                try:
                    # with-pg-env preparation stderr may contain diagnostics about a DSN.
                    # Go's own stderr is separately retained by its post-probe adapter.
                    for line in child.stderr:
                        stderr.write(b"[wrapper stderr redacted]\n")
                        stderr.flush()
                except Exception as e:
                    errors.append(type(e).__name__)
            thread = threading.Thread(target=copy_error, daemon=True)
            thread.start()
            try:
                for line in child.stdout:
                    stdout.write(line)
                    stdout.flush()
                    progress_line(line)
            except Exception as e:
                errors.append(type(e).__name__)
            wrapper["rc"] = child.wait()
            thread.join(timeout=5)
            wrapper["streams_complete"] = not errors and not thread.is_alive()
            wrapper["sampler"] = sampler.finish()
    for sig, handler in previous.items():
        signal.signal(sig, handler)
    wrapper.update(end_utc=utc_now(), end_mono_ns=time.monotonic_ns(), cancelled=cancel["signal"] is not None)
    # Capture is closed. Hash only the saved stdout and already-redacted stderr;
    # summarization must never backfill these authoritative producer hashes.
    wrapper["stream_sha256"] = {"stdout": sha256_file(out / "wrapper.stdout.raw.jsonl"),
                                "stderr": sha256_file(out / "wrapper.stderr.raw")}
    wrapper["elapsed_monotonic_ns"] = wrapper["end_mono_ns"] - wrapper["start_mono_ns"]
    go = read_json(out / "go.identity.json", {})
    binary = read_json(out / "binary.identity.json", {})
    invocations.update(go=go, binary=binary)
    write_json(out / "invocations.json", invocations)
    source["after"] = source_snapshot(root)
    source["source_changed_during_measure"] = source["before"] != source["after"]
    write_json(out / "source.json", source)
    pprof = pprof_once(out)
    phases = {"preparation_start_utc": prep["utc"], "preparation_start_mono_ns": prep["mono_ns"],
              "wrapper_start_utc": wrapper["start_utc"], "wrapper_end_utc": wrapper["end_utc"],
              "before_binary_ns": binary.get("start_mono_ns", 0) - wrapper["start_mono_ns"] if binary.get("start_mono_ns") else None,
              "binary_lifetime_ns": binary.get("elapsed_monotonic_ns"),
              "pprof": [{k: r.get(k) for k in ("label", "start_utc", "end_utc", "elapsed_monotonic_ns")} for r in pprof["receipts"]]}
    exit_obj = {"wrapper_rc": wrapper["rc"], "go_started": go.get("started", False),
                "go_rc": go.get("rc"), "binary_rc": binary.get("rc"), "binary_signal": binary.get("signal"),
                "sampler_errors": sum(r.get("sampler", {}).get("errors", 0) for r in (wrapper, go, binary)),
                "cancelled": cancel["signal"] is not None or bool(go.get("signal")) or bool(binary.get("signal")),
                "pprof": pprof["status"], "phases": phases}
    write_json(out / "exit.json", exit_obj)
    summary, _, _ = derive_summary(out)
    write_sha256sums(out)
    return instrument_rc(summary, exit_obj)


def cmd_summarize(out_dir, require_complete=False):
    out = Path(out_dir)
    if not out.is_dir():
        sys.stderr.write("summarize: out directory is absent\n")
        return RC_UNKNOWN
    exit_obj = read_json(out / "exit.json", {}) or {}
    summary, parsed, _sched = derive_summary(out)
    sys.stdout.write(
        json.dumps(
            {
                "observation": summary.get("observation"),
                "tests": summary.get("tests"),
                "integrity_ok": summary.get("integrity_ok"),
                "counters": summary.get("counters"),
                "gomaxprocs_observed": summary.get("gomaxprocs_observed"),
                "runtime_attributable": summary.get("runtime_attributable"),
                "cpu_profile": summary.get("cpu_profile"),
                "epilogue_nonzero": parsed.get("epilogue_nonzero"),
            },
            indent=2,
            sort_keys=True,
        )
        + "\n"
    )
    write_sha256sums(out)
    if require_complete and not summary.get("observation_complete"):
        return RC_INCOMPLETE
    if require_complete:
        return 0
    return instrument_rc(summary, exit_obj)


def main(argv):
    parser = argparse.ArgumentParser(prog="ci-race-package-profile.py")
    sub = parser.add_subparsers(dest="cmd", required=True)
    p_run = sub.add_parser("run")
    p_run.add_argument("--package", required=True)
    p_run.add_argument("--out", required=True)
    p_run.add_argument("--phase", choices=("allocate", "resolve", "provision", "inventory", "measure", "finalize-prepare"), default="measure")
    p_exec = sub.add_parser("exec")
    p_exec.add_argument("--out", required=True)
    p_exec.add_argument("--producer", choices=("go", "binary"), default="binary")
    p_exec.add_argument("binary_argv", nargs=argparse.REMAINDER)
    p_sum = sub.add_parser("summarize")
    p_sum.add_argument("--out", required=True)
    p_sum.add_argument("--require-complete", action="store_true")
    args = parser.parse_args(argv)
    if args.cmd == "run":
        return cmd_run(args.package, args.out, phase=args.phase)
    if args.cmd == "exec":
        binary = list(args.binary_argv)
        if binary and binary[0] == "--":
            binary = binary[1:]
        return cmd_exec(args.out, binary, producer=args.producer)
    if args.cmd == "summarize":
        return cmd_summarize(args.out, require_complete=args.require_complete)
    return RC_PREPARE


if __name__ == "__main__":
    result = main(sys.argv[1:])
    sys.exit(result if result >= 0 else 128 - result)
