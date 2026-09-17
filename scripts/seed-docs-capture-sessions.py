#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Register one local fixture provider profile and launch the five docs-capture
sessions through the real Olivares CLI.

This is not a replacement runtime. It POSTs the existing provider-profiles API,
invokes the built `olivares agent session create` with an explicit
`--provider-profile`, and corroborates each run and its managed live row through
the same authenticated API. A missing profile, rejected workspace, timeout or
partial outcome returns nonzero. It never falls back to an unprofiled launch,
never adopts a profile by name, and never prints token values.
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

_ETIQUETA = "seed-docs-capture-sessions"

SESIONES = (
    ("billing-migration review", "claude-opus-4-8"),
    ("entitlement audit sweep", "claude-opus-4-8"),
    ("deploy-values reconcile", "claude-sonnet-4-5"),
    ("incident postmortem draft", "claude-opus-4-8"),
    ("dependency upgrade scan", "claude-sonnet-4-5"),
)

VALID_RUN_STATES = frozenset({"running", "idle"})


def _busca_lib():
    candidatos = [
        os.environ.get("OLIVARES_LIB_DIR", ""),
        os.path.join(os.path.dirname(os.path.abspath(__file__)), "lib"),
    ]
    raiz = os.getcwd()
    for _ in range(6):
        candidatos.append(os.path.join(raiz, "scripts", "lib"))
        raiz = os.path.dirname(raiz) or "/"
    for c in candidatos:
        if c and os.path.isfile(os.path.join(c, "redaccion.py")):
            return c
    return None


_lib = _busca_lib()
if _lib is None:
    print(
        "%s: cannot look: scripts/lib/redaccion.py is missing" % _ETIQUETA,
        file=sys.stderr,
    )
    sys.exit(2)
sys.path.insert(0, _lib)
from redaccion import Redactor, abre  # noqa: E402


def utc_now():
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def write_json(path, payload):
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf8") as fh:
        json.dump(payload, fh, indent=2, ensure_ascii=False)
        fh.write("\n")
    os.replace(tmp, path)


def redact_write(path, text, redactor):
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    with open(path, "w", encoding="utf8") as fh:
        fh.write(redactor(text if text is not None else ""))
        if text and not str(text).endswith("\n"):
            fh.write("\n")


class Caller:
    def __init__(self, server, token, tenant):
        self.server = server.rstrip("/")
        self.token = token
        self.tenant = tenant

    def request(self, method, path, body=None, timeout=20):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.server + path, data=data, method=method)
        req.add_header("Authorization", "Bearer " + self.token)
        req.add_header("X-Olivares-Tenant", self.tenant)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with abre(req, timeout=timeout) as resp:
                raw = resp.read().decode()
                parsed = json.loads(raw) if raw else {}
                return resp.status, parsed, raw
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode(errors="replace")
            try:
                parsed = json.loads(raw) if raw else {}
            except json.JSONDecodeError:
                parsed = {"error": raw[:400]}
            return exc.code, parsed, raw
        except urllib.error.URLError as exc:
            return 0, {"error": str(exc)}, ""


def load_token(args):
    if args.token_file:
        try:
            with open(args.token_file, encoding="utf8") as fh:
                token = fh.read().strip()
        except OSError as exc:
            print("%s: cannot read --token-file: %s" % (_ETIQUETA, exc), file=sys.stderr)
            sys.exit(2)
        if not token:
            print("%s: --token-file is empty" % _ETIQUETA, file=sys.stderr)
            sys.exit(2)
        return token
    token = os.environ.get("OLIVARES_TOKEN", "").strip()
    if not token:
        print(
            "%s: cannot look: set OLIVARES_TOKEN or pass --token-file "
            "(never pass the value on argv)" % _ETIQUETA,
            file=sys.stderr,
        )
        sys.exit(2)
    return token


def argv_receipt(argv):
    # Never persist a literal token flag value. The CLI receives the bearer from
    # OLIVARES_TOKEN in the child environment.
    return list(argv)


def parse_run_json(stdout):
    text = (stdout or "").strip()
    if not text:
        return None
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        start = text.find("{")
        end = text.rfind("}")
        if start >= 0 and end > start:
            try:
                return json.loads(text[start : end + 1])
            except json.JSONDecodeError:
                return None
        return None


def corroborate(api, run_ref, expected, deadline, poll):
    encoded = urllib.parse.quote(run_ref, safe="")
    last = {"run": None, "live": None, "reason": "not-started"}
    until = time.time() + deadline
    while True:
        status, run, _ = api.request("GET", "/v1/m/sessions/runs/" + encoded)
        last["run"] = {"http_status": status, "dto": run}
        if status != 200:
            last["reason"] = "run-read-http-%s" % status
        else:
            state = run.get("state")
            live_ref = run.get("live_ref") or ""
            if state in ("failed", "stopped", "cleaned") and not live_ref:
                last["reason"] = "run-terminal-without-live"
                return False, last
            if (
                state in VALID_RUN_STATES
                and live_ref
                and run.get("provider_profile_ref") == expected["profile_ref"]
                and run.get("provider_driver") == expected["driver"]
                and run.get("provider_environment_ref") == expected["environment_ref"]
                and run.get("workspace_ref") == expected["workspace_ref"]
            ):
                live_status, live, _ = api.request(
                    "GET",
                    "/v1/m/sessions/live/by-id/"
                    + urllib.parse.quote(live_ref, safe=""),
                )
                last["live"] = {"http_status": live_status, "dto": live}
                if live_status != 200:
                    last["reason"] = "live-read-http-%s" % live_status
                elif (
                    live.get("attribution") == "managed"
                    and live.get("run_ref") == run_ref
                    and live.get("provider_profile_ref") == expected["profile_ref"]
                    and live.get("canonical_sid")
                    and live.get("environment_ref") == expected["environment_ref"]
                ):
                    last["reason"] = "ok"
                    last["live_ref"] = live_ref
                    last["canonical_sid"] = live.get("canonical_sid")
                    last["provider_conversation_id"] = run.get(
                        "provider_conversation_id"
                    ) or run.get("claude_session_id")
                    last["provider_auth_source"] = run.get("provider_auth_source")
                    last["state"] = state
                    return True, last
                else:
                    last["reason"] = "live-fields-mismatch"
            else:
                last["reason"] = "run-not-initialized"
        if time.time() >= until:
            if last["reason"] == "run-not-initialized":
                last["reason"] = "deadline-exceeded"
            return False, last
        time.sleep(poll)


def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Register a local fixture profile and launch the five docs-capture sessions."
    )
    parser.add_argument("--server", required=True, help="control-plane base URL")
    parser.add_argument("--tenant", required=True, help="demo tenant id")
    parser.add_argument("--workspace", required=True, help="sessions.workspace reference")
    parser.add_argument("--olivares-bin", required=True, help="path to the built olivares CLI")
    parser.add_argument(
        "--scratch",
        required=True,
        help="owned directory for this run's user_home and config_home",
    )
    parser.add_argument(
        "--receipts",
        required=True,
        help="directory outside the engine scratch that keeps sanitized receipts",
    )
    parser.add_argument(
        "--token-file",
        default="",
        help="lab token file (preferred over printing a value); else OLIVARES_TOKEN",
    )
    parser.add_argument("--deadline-seconds", type=float, default=45.0)
    parser.add_argument("--poll-seconds", type=float, default=0.2)
    args = parser.parse_args(argv)

    token = load_token(args)
    redactor = Redactor()
    redactor.recuerda(token)
    redactor.recuerda_url(args.server)

    if not str(args.workspace).strip():
        print("%s: workspace reference is empty; refusing to launch" % _ETIQUETA, file=sys.stderr)
        return 1
    bin_path = os.path.abspath(args.olivares_bin)
    if not os.path.isfile(bin_path) or not os.access(bin_path, os.X_OK):
        print("%s: cannot look: olivares binary is not executable: %s" % (_ETIQUETA, bin_path), file=sys.stderr)
        return 2

    scratch = os.path.abspath(args.scratch)
    receipts = os.path.abspath(args.receipts)
    os.makedirs(scratch, exist_ok=True)
    os.makedirs(receipts, exist_ok=True)
    user_home = os.path.join(scratch, "user_home")
    config_home = os.path.join(scratch, "config_home")
    os.makedirs(user_home, exist_ok=True)
    os.makedirs(config_home, exist_ok=True)

    api = Caller(args.server, token, args.tenant)
    profile_body = {
        "driver": "claude",
        "display_name": "Docs capture fixture",
        "user_home": user_home,
        "config_home": config_home,
        "auth_source": "managed_injection",
    }
    status, created, raw = api.request("POST", "/v1/m/sessions/provider-profiles", profile_body)
    write_json(
        os.path.join(receipts, "profile-create.json"),
        {
            "http_status": status,
            "profile_ref": created.get("profile_ref"),
            "driver": created.get("driver"),
            "environment_ref": created.get("environment_ref"),
            "auth_source": created.get("auth_source"),
            "state": created.get("state"),
            "local_environment": created.get("local_environment"),
        },
    )
    if status != 201:
        print(
            "%s: provider profile create returned %s; refusing to adopt another profile"
            % (_ETIQUETA, status),
            file=sys.stderr,
        )
        redact_write(os.path.join(receipts, "profile-create.raw.txt"), raw, redactor)
        return 1
    profile_ref = created.get("profile_ref")
    environment_ref = created.get("environment_ref")
    if not profile_ref or not environment_ref:
        print("%s: 201 without profile_ref/environment_ref; refusing" % _ETIQUETA, file=sys.stderr)
        return 1

    expected = {
        "profile_ref": profile_ref,
        "environment_ref": environment_ref,
        "driver": "claude",
        "workspace_ref": args.workspace,
    }

    attempts = []
    created_ok = 0
    corroborated = 0
    run_refs = []
    live_refs = []
    canonical_sids = []
    conversation_ids = []

    child_env = os.environ.copy()
    child_env["OLIVARES_TOKEN"] = token
    child_env["OLIVARES_TENANT"] = args.tenant
    child_env["OLIVARES_SERVER_URL"] = args.server
    # Token travels as env, never as an argv value. DEMO_SESSION_UNIQUE is an
    # engine-side allowlisted name; the CLI only transmits the name.

    for index, (name, model) in enumerate(SESIONES):
        adir = os.path.join(receipts, "attempt-%d" % index)
        os.makedirs(adir, exist_ok=True)
        argv = [
            bin_path,
            "agent",
            "session",
            "create",
            "--name",
            name,
            "--model",
            model,
            "--workspace",
            args.workspace,
            "--transport",
            "stream-json",
            "--env-allow",
            "DEMO_SESSION_UNIQUE",
            "--server",
            args.server,
            "--tenant",
            args.tenant,
            "--provider-profile",
            profile_ref,
            "--insecure",
            "-o",
            "json",
        ]
        meta = {
            "attempt_index": index,
            "name": name,
            "model": model,
            "bin": bin_path,
            "argv": argv_receipt(argv),
            "token_transport": "env:OLIVARES_TOKEN",
            "provider_profile_ref": profile_ref,
            "workspace_ref": args.workspace,
            "start": utc_now(),
        }
        start = time.time()
        proc = subprocess.Popen(
            argv,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=child_env,
            text=True,
        )
        stdout, stderr = proc.communicate()
        rc = proc.returncode
        meta["pid"] = proc.pid
        meta["end"] = utc_now()
        meta["elapsed_s"] = round(time.time() - start, 4)
        meta["direct_rc"] = rc
        redact_write(os.path.join(adir, "stdout.txt"), stdout, redactor)
        redact_write(os.path.join(adir, "stderr.txt"), stderr, redactor)

        parsed = parse_run_json(stdout) if rc == 0 else None
        run_ref = (parsed or {}).get("run_ref") if parsed else None
        meta["run_ref"] = run_ref
        if rc == 0 and run_ref:
            created_ok += 1
            ok, detail = corroborate(
                api, run_ref, expected, args.deadline_seconds, args.poll_seconds
            )
            meta["corroboration"] = {
                "ok": ok,
                "reason": detail.get("reason"),
                "state": detail.get("state"),
                "live_ref": detail.get("live_ref"),
                "canonical_sid": detail.get("canonical_sid"),
                "provider_conversation_id": detail.get("provider_conversation_id"),
                "provider_auth_source": detail.get("provider_auth_source"),
                "run_http_status": (detail.get("run") or {}).get("http_status"),
                "live_http_status": (detail.get("live") or {}).get("http_status"),
            }
            # Persist DTO facts, not secrets.
            run_dto = ((detail.get("run") or {}).get("dto")) or {}
            live_dto = ((detail.get("live") or {}).get("dto")) or {}
            write_json(
                os.path.join(adir, "run.json"),
                {
                    "run_ref": run_dto.get("run_ref"),
                    "state": run_dto.get("state"),
                    "provider_profile_ref": run_dto.get("provider_profile_ref"),
                    "provider_driver": run_dto.get("provider_driver"),
                    "provider_environment_ref": run_dto.get("provider_environment_ref"),
                    "workspace_ref": run_dto.get("workspace_ref"),
                    "live_ref": run_dto.get("live_ref"),
                    "provider_auth_source": run_dto.get("provider_auth_source"),
                    "provider_conversation_id": run_dto.get("provider_conversation_id"),
                    "claude_session_id": run_dto.get("claude_session_id"),
                },
            )
            write_json(
                os.path.join(adir, "live.json"),
                {
                    "live_ref": live_dto.get("live_ref"),
                    "attribution": live_dto.get("attribution"),
                    "run_ref": live_dto.get("run_ref"),
                    "provider_profile_ref": live_dto.get("provider_profile_ref"),
                    "canonical_sid": live_dto.get("canonical_sid"),
                    "environment_ref": live_dto.get("environment_ref"),
                    "provider": live_dto.get("provider"),
                },
            )
            if ok:
                corroborated += 1
                run_refs.append(run_ref)
                live_refs.append(detail.get("live_ref"))
                canonical_sids.append(detail.get("canonical_sid"))
                conversation_ids.append(detail.get("provider_conversation_id"))
        else:
            meta["corroboration"] = {"ok": False, "reason": "create-rc-%s" % rc}
        write_json(os.path.join(adir, "meta.json"), meta)
        attempts.append(
            {
                "attempt_index": index,
                "direct_rc": rc,
                "run_ref": run_ref,
                "corroborated": bool((meta.get("corroboration") or {}).get("ok")),
                "reason": (meta.get("corroboration") or {}).get("reason"),
            }
        )

    unique_ok = (
        len(set(run_refs)) == 5
        and len(set(live_refs)) == 5
        and len(set(canonical_sids)) == 5
        and all(run_refs)
        and all(live_refs)
        and all(canonical_sids)
    )
    summary = {
        "attempted": len(SESIONES),
        "created": created_ok,
        "corroborated": corroborated,
        "unique_run_live_canonical": unique_ok,
        "profile_ref": profile_ref,
        "environment_ref": environment_ref,
        "workspace_ref": args.workspace,
        "driver": "claude",
        "auth_source": "managed_injection",
        "olivares_bin": bin_path,
        "attempts": attempts,
        "run_refs": run_refs,
        "live_refs": live_refs,
        "canonical_sids": canonical_sids,
        "fixture_limitation": (
            "demo-agent.sh is a local stand-in protocol; these runs are not an "
            "authenticated vendor CLI, inference, tool-use or billing evidence"
        ),
    }
    write_json(os.path.join(receipts, "summary.json"), summary)
    line = (
        "%s: attempted=%d created=%d corroborated=%d unique=%s profile=%s"
        % (
            _ETIQUETA,
            summary["attempted"],
            summary["created"],
            summary["corroborated"],
            unique_ok,
            profile_ref,
        )
    )
    print(redactor(line))
    if corroborated == 5 and unique_ok:
        return 0
    print(
        "%s: refusing: need five corroborated distinct run/live/canonical identities"
        % _ETIQUETA,
        file=sys.stderr,
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())
