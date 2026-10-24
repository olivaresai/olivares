#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""upgrade-journey.py: an upgrade keeps the data, and the documented rollback works.

The journey docs/UPGRADE-AND-ROLLBACK.md describes, run on a real old and a real new
engine over ONE data directory:

  seed      on the OLD engine: 2 accounts, 1 provider key, 3 sessions (each answers a turn),
            then a DR bundle taken with the old engine (UPGRADE-AND-ROLLBACK.md section 3).
  upgrade   the NEW engine over the same data: `version` is the new one, every row is still
            there, the provider key still opens its provider, both accounts still sign in and
            one session resumes and answers a turn. A write is made after the bundle.
  rollback  the OLD engine over the upgraded data (a swap alone: observed, section 5 says it may be
            refused), then the documented recovery: stop, `dr restore --in-place` of the
            pre-upgrade bundle with the old engine, start it. The seeded data is back and the
            post-upgrade write is gone.

REAL: the engines, their API, the session runtime, the store, the DR commands.
DOUBLE (named, never a replacement for the engine): the agent CLI (a `claude` stand-in that
speaks stream-json) and the provider (an HTTP server that accepts exactly one bearer key).

Two backends carry the same flow:
  process   two engine binaries over a data directory (any host with python3).
  compose   two images through deploy/compose/docker-compose.yml, one named volume (needs docker).

  scripts/upgrade-journey.py process --old OLD_BIN --new NEW_BIN --new-version 0.4
  scripts/upgrade-journey.py compose --old IMAGE --new IMAGE --new-version 0.4

Exit 0 = the journey holds; 1 = it does not (the step is named); 2 = could not look (no engine,
no docker, a daemon that does not answer, a Compose too old for the override layer).
"""
import argparse
import json
import os
import pathlib
import re
import shutil
import signal
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

PASSWORD = "upgrade-journey-pw-1"
ADMIN = "admin@upgrade.invalid"
MEMBER = "member@upgrade.invalid"
LATE = "after-backup@upgrade.invalid"
PROVIDER_KEY = "sk-upgrade-journey-0123456789"
REPLY = "journey-reply"
SESSIONS = 3
PROVIDER_PORT = 18080  # the compose backend's provider double, in the engine's network namespace

# The agent CLI double: an init frame, then one reply per prompt line.
CLAUDE_STUB = f"""#!/bin/sh
sid="journey-$$"
printf '{{"type":"system","subtype":"init","session_id":"%s","cwd":"%s"}}\\n' "$sid" "$PWD"
while IFS= read -r line; do
	printf '{{"type":"assistant","session_id":"%s","message":{{"role":"assistant","content":[{{"type":"text","text":"{REPLY}"}}]}}}}\\n' "$sid"
	printf '{{"type":"result","subtype":"success","is_error":false,"result":"{REPLY}","session_id":"%s"}}\\n' "$sid"
done
"""

# The provider double, run by this file as `provider PORT KEY` wherever the engine can reach it.
# It answers a model list to the one bearer it was given and refuses anything else.
PROVIDER_SERVER = """
import http.server, json, sys
port, key = int(sys.argv[1]), sys.argv[2]
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        ok = self.headers.get("Authorization") == "Bearer " + key or self.headers.get("x-api-key") == key
        body = json.dumps({"data": [{"id": "journey-model", "object": "model"}]} if ok else {"error": "bad key"}).encode()
        self.send_response(200 if ok else 401)
        self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(body)))
        self.end_headers(); self.wfile.write(body)
http.server.ThreadingHTTPServer(("127.0.0.1", port), H).serve_forever()
"""


class Fail(Exception):
    pass


class CouldNotLook(Exception):
    pass


def say(msg):
    print(f"upgrade-journey: {msg}", flush=True)


def free_port():
    import socket
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class Api:
    """The engine's HTTP API as a signed-in person."""

    def __init__(self, base):
        self.base, self.token, self.tenant = base, "", ""
        # A compose engine answers TLS with its first-boot self-signed certificate on loopback.
        self.ctx = ssl.create_default_context()
        self.ctx.check_hostname, self.ctx.verify_mode = False, ssl.CERT_NONE

    def call(self, method, path, body=None, want=(200, 201, 202, 204)):
        headers = {"content-type": "application/json"}
        if self.token:
            headers["authorization"] = "Bearer " + self.token
        if self.tenant:
            headers["X-Olivares-Tenant"] = self.tenant
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=60, context=self.ctx) as r:
                status, raw = r.status, r.read()
        except urllib.error.HTTPError as e:
            status, raw = e.code, e.read()
        except (urllib.error.URLError, TimeoutError) as e:
            raise Fail(f"{method} {path}: the engine did not answer: {e}")
        if status not in want:
            raise Fail(f"{method} {path}: HTTP {status} {raw.decode(errors='replace')[:300]}")
        text = raw.decode(errors="replace").strip()
        return json.loads(text) if text[:1] in ("{", "[") else {}

    def wait_healthy(self, engine):
        for _ in range(120):
            try:
                urllib.request.urlopen(self.base + "/healthz", timeout=2, context=self.ctx)
                return
            except Exception:
                engine.require_alive()
                time.sleep(1)
        raise Fail("engine start: no /healthz answer within 120 s")

    def sign_in(self, email):
        out = self.call("POST", "/v1/auth/login", {"email": email, "password": PASSWORD})
        self.token = out.get("token", "")
        if not self.token:
            raise Fail(f"sign in as {email}: no token in the answer")

    def frames(self, ref, seconds=3):
        """The session's output lines from the live attach stream."""
        got = []
        req = urllib.request.Request(f"{self.base}/v1/m/sessions/runs/{ref}/attach?from=0",
                                     headers={"authorization": "Bearer " + self.token, "X-Olivares-Tenant": self.tenant})
        try:
            with urllib.request.urlopen(req, timeout=seconds, context=self.ctx) as r:
                for raw in r:
                    line = raw.decode(errors="replace").strip()
                    if line.startswith("data:"):
                        try:
                            got.append(json.loads(line[5:]).get("line", ""))
                        except ValueError:
                            pass
        except urllib.error.HTTPError as e:
            raise Fail(f"attach {ref}: HTTP {e.code}")
        except urllib.error.URLError as e:
            if not isinstance(e.reason, TimeoutError):
                raise Fail(f"attach {ref}: {e}")
        except TimeoutError:
            pass  # the stream is read until its timeout; what arrived is what counts
        return got

    def turn(self, ref, text):
        """Send one prompt; the answer is the double's reply frame, or the step fails."""
        before = sum(REPLY in l for l in self.frames(ref))
        user = {"type": "user", "message": {"role": "user", "content": text}}
        self.call("POST", f"/v1/m/sessions/runs/{ref}/input", {"line": json.dumps(user)})
        for _ in range(20):
            if sum(REPLY in l for l in self.frames(ref)) > before:
                return
            time.sleep(0.5)
        raise Fail(f"session {ref}: no answer to a turn")


class ProcessBackend:
    """Engine binaries over one data directory on this host."""

    def __init__(self, work, old, new, skip_turns):
        self.skip_turns = skip_turns
        self.work = pathlib.Path(work)
        self.data = self.work / "data"
        self.home = self.work / "home"
        self.stubdir = self.work / "stub"
        self.project = self.work / "project"
        for d in (self.data, self.home, self.stubdir, self.project):
            d.mkdir(parents=True, exist_ok=True)
        os.chmod(self.data, 0o700)
        os.chmod(self.home, 0o700)
        (self.stubdir / "claude").write_text(CLAUDE_STUB)
        os.chmod(self.stubdir / "claude", 0o755)
        self.binaries = {"old": old, "new": new}
        self.proc = None
        self.current = ""
        self.out = self.work / "engine.out"
        self.data_arg = str(self.data)  # the engine's own path to the data directory
        self.bundle_dir = self.work / "bundles"
        self.bundle_dir.mkdir(exist_ok=True)
        self.bundle_arg = str(self.bundle_dir)
        self.provider = None

    def start(self, which):
        port, grpc = free_port(), free_port()
        env = dict(os.environ, HOME=str(self.home), PATH=f"{self.stubdir}:{os.environ['PATH']}")
        self.current = which
        self.out.write_text("")
        self.proc = subprocess.Popen(
            [self.binaries[which], "serve", "--insecure", "--data-dir", str(self.data),
             "--listen", f"127.0.0.1:{port}", "--grpc-listen", f"127.0.0.1:{grpc}"],
            stdout=open(self.out, "w"), stderr=open(self.work / f"engine-{which}.log", "w"), env=env, start_new_session=True)
        api = Api(f"http://127.0.0.1:{port}")
        api.wait_healthy(self)
        return api

    def require_alive(self):
        if self.proc.poll() is not None:
            raise Fail(f"the engine exited rc={self.proc.returncode}: {self.tail()}")

    def tail(self):
        try:
            return " | ".join((self.work / f"engine-{self.current}.log").read_text().strip().splitlines()[-3:])
        except OSError:
            return ""

    def stop(self):
        if self.proc and self.proc.poll() is None:
            os.killpg(self.proc.pid, signal.SIGTERM)
            try:
                self.proc.wait(30)
            except subprocess.TimeoutExpired:
                os.killpg(self.proc.pid, signal.SIGKILL)
                self.proc.wait()
                self.proc = None
                raise Fail("the engine did not stop within 30 s of SIGTERM")
        self.proc = None

    def setup_token(self):
        for _ in range(40):
            m = re.search(r"olst_[A-Za-z0-9]+", self.out.read_text())
            if m:
                return m.group(0)
            time.sleep(0.5)
        raise Fail("setup token: the first boot printed none")

    def cli(self, which, args):
        env = dict(os.environ, HOME=str(self.home))
        return subprocess.run([self.binaries[which], *args], capture_output=True, text=True, env=env, timeout=600)

    def where_provider(self, key):
        port = free_port()
        self.provider = subprocess.Popen([sys.executable, "-I", "-c", PROVIDER_SERVER, str(port), key], start_new_session=True)
        return f"http://127.0.0.1:{port}"

    def workspace_path(self):
        return str(self.project)

    def close(self):
        self.stop()
        if self.provider and self.provider.poll() is None:
            os.killpg(self.provider.pid, signal.SIGTERM)


class ComposeBackend:
    """Two images through deploy/compose/docker-compose.yml over one named volume."""

    def __init__(self, work, old, new, repo, skip_turns):
        self.skip_turns = skip_turns
        self.work = pathlib.Path(work)
        self.images = {"old": old, "new": new}
        self.repo = repo
        self.project = f"upgrade-journey-{os.getpid()}"
        self.override = self.work / "journey.override.yml"
        # The doubles below are bind-mounted and read by the image's uid 65532, not by this user;
        # the directory holds nothing else.
        os.chmod(self.work, 0o755)
        stub = self.work / "stub"
        stub.mkdir(exist_ok=True)
        (stub / "claude").write_text(CLAUDE_STUB)
        os.chmod(stub / "claude", 0o755)
        (self.work / "provider.py").write_text(PROVIDER_SERVER)
        # The base file pins container_name and publishes fixed host ports; this run takes
        # neither (the layer the first-hour gate uses), puts the agent CLI double first on the
        # engine's PATH, and keeps the data in the named volume the upgrade reuses. The provider
        # double is a service in the engine's own network namespace, so it is recreated with the
        # engine and the engine of either image reaches it on its loopback.
        self.override.write_text(f"""services:
  olivares:
    container_name: !reset null
    ports: !override
      - "127.0.0.1::8443"
    environment:
      PATH: /journey-stub:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
    volumes:
      - {stub}:/journey-stub:ro
  provider:
    image: ${{OLIVARES_IMAGE}}
    network_mode: service:olivares
    entrypoint: [python3, /provider.py, "{PROVIDER_PORT}", "{PROVIDER_KEY}"]
    volumes:
      - {self.work}/provider.py:/provider.py:ro
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
""")
        self.data_arg = "/var/lib/olivares"
        self.bundle_arg = "/var/lib/olivares/journey-bundles"
        self.current = ""

    def compose(self, *args, image, check=True):
        env = dict(os.environ, OLIVARES_IMAGE=image, OLIVARES_BIND="127.0.0.1")
        for var in ("COMPOSE_FILE", "COMPOSE_PROFILES"):
            env.pop(var, None)
        cmd = ["docker", "compose", "-p", self.project, "-f", "deploy/compose/docker-compose.yml", "-f", str(self.override), *args]
        r = subprocess.run(cmd, cwd=self.repo, capture_output=True, text=True, env=env, timeout=900)
        if check and r.returncode != 0:
            raise Fail(f"docker compose {' '.join(args)}: rc={r.returncode} {r.stderr.strip()[-400:]}")
        return r

    def start(self, which):
        self.current = which
        image = self.images[which]
        # The documented upgrade pulls; an image that is already here (a local candidate) is not pulled.
        if subprocess.run(["docker", "image", "inspect", image], capture_output=True).returncode != 0:
            self.compose("pull", "--quiet", image=image)
        self.compose("up", "--wait", "--wait-timeout", "120", "-d", image=image)
        port = self.compose("port", "olivares", "8443", image=image).stdout.strip().rsplit(":", 1)[-1]
        api = Api(f"https://127.0.0.1:{port}")
        api.wait_healthy(self)
        return api

    def require_alive(self):
        pass  # `up --wait` already returned healthy; wait_healthy confirms the published port answers

    def stop(self):
        if self.current:
            self.compose("stop", image=self.images[self.current])

    def setup_token(self):
        logs = self.compose("logs", "--no-color", "olivares", image=self.images["old"]).stdout
        m = re.search(r"olst_[A-Z0-9]+", logs)
        if not m:
            raise Fail("setup token: the first boot printed none")
        return m.group(0)

    def cli(self, which, args):
        # A one-off container over the same volume and image: the engine is stopped, as the
        # documented backup and restore steps require for restore.
        return self.compose("run", "--rm", "--no-deps", "-T", "--entrypoint", "/usr/local/bin/olivares", "olivares", *args,
                            image=self.images[which], check=False)

    def where_provider(self, key):
        return f"http://127.0.0.1:{PROVIDER_PORT}"

    def workspace_path(self):
        return "/project"

    def close(self):
        r = self.compose("down", "--volumes", "--remove-orphans", image=self.images["old"], check=False)
        if r.returncode != 0:
            print(f"upgrade-journey: WARNING: compose down left resources behind (project {self.project}): "
                  f"{r.stderr.strip()[-300:]}", file=sys.stderr)


def cli_ok(backend, which, args, what):
    r = backend.cli(which, args)
    if r.returncode != 0:
        raise Fail(f"{what}: rc={r.returncode} {(r.stderr or r.stdout).strip()[-400:]}")
    return r.stdout


def bundle_args(backend):
    return ["--data-dir", backend.data_arg, "--engine", "sqlite", "--passphrase-file", f"{backend.bundle_arg}/pass"]


def seed(backend, state):
    """The OLD engine: 2 accounts, 1 provider key, 3 sessions that answered a turn; then a DR bundle."""
    api = backend.start("old")
    token = backend.setup_token()
    su = api.call("POST", "/v1/setup", {"token": token, "email": ADMIN, "password": PASSWORD})
    api.tenant = (su.get("organization") or {}).get("tenant_id", "")
    api.sign_in(ADMIN)
    api.call("POST", "/v1/users", {"email": MEMBER, "display_name": "Journey member", "password": PASSWORD,
                                   "tenant": api.tenant, "role": "editor"})
    base_url = backend.where_provider(PROVIDER_KEY)
    prov = api.call("POST", "/v1/m/sessions/providers", {"kind": "openai_compatible", "display_name": "journey key",
                                                         "base_url": base_url, "api_key": PROVIDER_KEY})
    if not prov.get("key_hint"):
        raise Fail(f"seed: the provider record has no key_hint to compare after the upgrade: {prov}")
    state["provider"] = {"ref": prov["provider_ref"], "key_hint": prov["key_hint"]}
    acct = api.call("POST", "/v1/m/sessions/provider-profiles",
                    {"driver": "claude", "config_home": "", "user_home": "", "display_name": "journey",
                     "auth_source": "provider_account_home"})
    profile = acct.get("ref") or acct.get("profile_ref") or acct.get("id")
    ws = api.call("POST", "/v1/m/sessions/workspaces", {"name": "project", "root_path": backend.workspace_path(),
                                                        "mount_mode": "rw", "dlp_mode": "off"})
    ws_ref = ws.get("ref") or ws.get("workspace_ref") or ws.get("id")
    runs = []
    for n in range(SESSIONS):
        run = api.call("POST", "/v1/m/sessions/runs", {"provider_profile_ref": profile, "workspace_ref": ws_ref,
                                                       "transport": "stream-json", "permission_mode": "default",
                                                       "name": f"journey-{n}"})
        if not backend.skip_turns:
            api.turn(run["run_ref"], f"say hello {n}")
            api.call("POST", f"/v1/m/sessions/runs/{run['run_ref']}/stop", {})
        runs.append(run["run_ref"])
    listed = {r["run_ref"]: r for r in api.call("GET", "/v1/m/sessions/runs").get("items", [])}
    state["run_facts"] = {ref: (listed[ref].get("name"), listed[ref].get("workspace_ref")) for ref in runs}
    state.update(tenant=api.tenant, runs=runs, profile=profile, workspace=ws_ref)
    say(f"seeded on the old engine: 2 accounts, 1 provider key ({state['provider']['key_hint']!r}), {SESSIONS} sessions")
    backend.stop()
    # The pre-upgrade DR bundle (UPGRADE-AND-ROLLBACK.md section 3), taken with the OLD engine.
    state["old_version"] = json.loads(cli_ok(backend, "old", ["version", "--output", "json"], "olivares version on the old engine"))["version"]
    prepare_bundle_dir(backend)
    cli_ok(backend, "old", ["dr", "backup", *bundle_args(backend), "--out", f"{backend.bundle_arg}/pre-upgrade.drbundle"],
           "dr backup with the old engine")
    say("pre-upgrade DR bundle taken with the old engine")


def prepare_bundle_dir(backend):
    if isinstance(backend, ProcessBackend):
        (backend.bundle_dir / "pass").write_text("journey dr passphrase\n")
        os.chmod(backend.bundle_dir / "pass", 0o600)
    else:
        # The image's own python3 (no shell is assumed); the passphrase file is private to the volume.
        backend.compose("run", "--rm", "--no-deps", "-T", "--entrypoint", "python3", "olivares", "-I", "-c",
                        f"import os; d = {backend.bundle_arg!r}; os.makedirs(d, mode=0o700, exist_ok=True); "
                        f"open(d + '/pass', 'w').write('journey dr passphrase\\n'); os.chmod(d + '/pass', 0o600)",
                        image=backend.images["old"])


def check_data(api, state, who):
    """Every seeded row is there, through the API a person uses."""
    emails = {u.get("email") for u in api.call("GET", "/v1/users").get("items", [])}
    for email in (ADMIN, MEMBER):
        if email not in emails:
            raise Fail(f"{who}: account {email} is missing (have {sorted(e for e in emails if e)})")
    roles = {m.get("email"): m.get("role") for m in api.call("GET", "/v1/members").get("items", [])}
    if roles.get(ADMIN) != "owner" or roles.get(MEMBER) != "editor":
        raise Fail(f"{who}: roles changed: {roles}")
    api.sign_in(MEMBER)
    api.sign_in(ADMIN)
    spaces = {w.get("workspace_ref"): w for w in api.call("GET", "/v1/m/sessions/workspaces").get("items", [])}
    if spaces.get(state["workspace"], {}).get("state") != "active":
        raise Fail(f"{who}: the registered folder is missing or not active")
    profiles = {x.get("profile_ref"): x for x in api.call("GET", "/v1/m/sessions/provider-profiles").get("items", [])}
    if profiles.get(state["profile"], {}).get("state") != "active":
        raise Fail(f"{who}: the Claude Code account is missing or not active")
    prov = api.call("GET", f"/v1/m/sessions/providers/{state['provider']['ref']}")
    if prov.get("state") == "revoked" or prov.get("key_hint") != state["provider"]["key_hint"]:
        raise Fail(f"{who}: the provider key row changed: {prov}")
    tested = api.call("POST", f"/v1/m/sessions/providers/{state['provider']['ref']}/test", {})
    if tested.get("probe_state") != "ok":
        raise Fail(f"{who}: the stored key no longer opens its provider: {tested.get('probe_state')} {tested.get('probe_detail')}")
    have = {r.get("run_ref"): r for r in api.call("GET", "/v1/m/sessions/runs").get("items", [])}
    missing = [r for r in state["runs"] if r not in have]
    if missing:
        raise Fail(f"{who}: sessions missing: {missing}")
    for ref, (name, workspace) in state["run_facts"].items():
        if (have[ref].get("name"), have[ref].get("workspace_ref")) != (name, workspace):
            raise Fail(f"{who}: session {ref} changed: {have[ref].get('name')!r} in {have[ref].get('workspace_ref')!r}, "
                       f"was {name!r} in {workspace!r}")
    audit = api.call("GET", "/v1/audit/verify")
    if audit.get("ok") is not True:
        raise Fail(f"{who}: the audit chain does not verify: {audit}")


def upgrade(backend, state, expect_version):
    """The NEW engine over the same data."""
    api = backend.start("new")
    out = json.loads(cli_ok(backend, "new", ["version", "--output", "json"], "olivares version on the new engine"))
    if out.get("version") != expect_version:
        raise Fail(f"olivares version = {out.get('version')!r}, want {expect_version!r}")
    if out["version"] == state["old_version"]:
        raise Fail(f"the new engine reports the old version {state['old_version']!r}: nothing was upgraded")
    api.tenant = state["tenant"]
    api.sign_in(ADMIN)
    check_data(api, state, "after the upgrade")
    ref = state["runs"][0]
    if not backend.skip_turns:
        api.call("POST", f"/v1/m/sessions/runs/{ref}/resume", {})
        api.turn(ref, "answer after the upgrade")
        api.call("POST", f"/v1/m/sessions/runs/{ref}/stop", {})
    # A write after the bundle: recovery to the bundle must not keep it (section 5).
    api.call("POST", "/v1/users", {"email": LATE, "display_name": "After backup", "password": PASSWORD,
                                   "tenant": state["tenant"], "role": "editor"})
    if LATE not in {u.get("email") for u in api.call("GET", "/v1/users").get("items", [])}:
        raise Fail("the account written after the bundle is not listed: the rollback check below would prove nothing")
    # Read back from disk by a fresh process: the write the restore must later remove is real.
    backend.stop()
    api = backend.start("new")
    api.tenant = state["tenant"]
    api.sign_in(ADMIN)
    check_data(api, state, "after restarting the new engine")
    if LATE not in {u.get("email") for u in api.call("GET", "/v1/users").get("items", [])}:
        raise Fail("the account written after the bundle did not survive a restart of the new engine")
    turn = "NOT RUN (--skip-turns)" if backend.skip_turns else f"session {ref} resumed and answered"
    say(f"upgraded: version {out['version']}, every row present and still there after a restart, {turn}")
    backend.stop()


def rollback(backend, state):
    """The OLD engine again. A swap alone is only observed (an old engine may refuse an advanced
    store, section 5); the documented recovery is what must work: stop, restore the pre-upgrade
    bundle with the old engine, start it."""
    try:
        api = backend.start("old")
        api.tenant = state["tenant"]
        api.sign_in(ADMIN)
        check_data(api, state, "after a swap to the old engine")
        swap = "the old engine opened the upgraded data and every row is present"
    except Fail as e:
        swap = f"the old engine did not hold over the upgraded data ({str(e)[:160]})"
    backend.stop()
    say(f"swap alone: {swap}")
    cli_ok(backend, "old", ["dr", "restore", "--in", f"{backend.bundle_arg}/pre-upgrade.drbundle", "--data-dir", backend.data_arg,
                            "--engine", "sqlite", "--passphrase-file", f"{backend.bundle_arg}/pass", "--in-place",
                            "--operator", "journey@upgrade.invalid", "--reason", "upgrade journey rollback"],
           "dr restore --in-place with the old engine")
    api = backend.start("old")
    api.tenant = state["tenant"]
    api.sign_in(ADMIN)
    check_data(api, state, "after the restore")
    emails = {u.get("email") for u in api.call("GET", "/v1/users").get("items", [])}
    if LATE in emails:
        raise Fail("after the restore: the account written after the bundle is still there")
    say("rollback by restore: the old engine runs on the pre-upgrade bundle, every seeded row is present, "
        "the account written after the bundle is gone")
    backend.stop()


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="backend", required=True)
    for name in ("process", "compose"):
        s = sub.add_parser(name)
        s.add_argument("--old", required=True, help="old engine binary" if name == "process" else "old image (digest)")
        s.add_argument("--new", required=True, help="new engine binary" if name == "process" else "new image (digest)")
        s.add_argument("--new-version", required=True, help="what `olivares version` must report on the new engine")
        s.add_argument("--skip-turns", action="store_true",
                       help="check the session rows but start no session: for a host that cannot confine sessions "
                            "(no Landlock), where the engine refuses to launch one; the turn is then reported NOT RUN")
        s.add_argument("--keep", action="store_true", help="keep the work directory")
    args = p.parse_args()
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))  # runs the finally blocks below
    if not args.new_version.strip():
        p.error("--new-version must not be empty")
    work = tempfile.mkdtemp(prefix="upgrade-journey.")
    os.chmod(work, 0o700)
    repo = str(pathlib.Path(__file__).resolve().parent.parent)
    try:
        if args.backend == "process":
            for b in (args.old, args.new):
                if not os.access(b, os.X_OK):
                    raise CouldNotLook(f"no executable engine at {b}")
            backend = ProcessBackend(work, args.old, args.new, args.skip_turns)
        else:
            if not shutil.which("docker"):
                raise CouldNotLook("docker is not installed")
            if subprocess.run(["docker", "info"], capture_output=True).returncode != 0:
                raise CouldNotLook("the docker daemon does not answer")
            cv = subprocess.run(["docker", "compose", "version", "--short"], capture_output=True, text=True)
            have = tuple(int(x) for x in re.findall(r"\d+", cv.stdout)[:3])
            if cv.returncode != 0 or have < (2, 24, 4):
                raise CouldNotLook(f"docker compose 2.24.4 or newer is needed for the !reset/!override layer, found {cv.stdout.strip() or 'none'}")
            backend = ComposeBackend(work, args.old, args.new, repo, args.skip_turns)
        state = {}
        try:
            seed(backend, state)
            upgrade(backend, state, args.new_version)
            rollback(backend, state)
        finally:
            try:
                backend.close()
            except Exception as e:  # cleanup must not replace the journey's own verdict
                print(f"upgrade-journey: WARNING: cleanup failed: {e}", file=sys.stderr)
        turns = "; session turns NOT RUN (--skip-turns)" if args.skip_turns else ""
        say(f"PASS: upgrade keeps the data; the documented rollback to the old engine works{turns}")
        return 0
    except Fail as e:
        print(f"upgrade-journey: FAIL: {e}", file=sys.stderr)
        return 1
    except (KeyError, ValueError, subprocess.TimeoutExpired) as e:
        print(f"upgrade-journey: FAIL: unexpected answer or timeout: {type(e).__name__}: {e}", file=sys.stderr)
        return 1
    except CouldNotLook as e:
        print(f"upgrade-journey: COULD NOT LOOK: {e}", file=sys.stderr)
        return 2
    finally:
        if not args.keep:
            shutil.rmtree(work, onerror=lambda _f, path, _e: print(f"upgrade-journey: WARNING: could not remove {path}", file=sys.stderr))
        else:
            print(f"upgrade-journey: kept {work}", file=sys.stderr)


if __name__ == "__main__":
    sys.exit(main())
