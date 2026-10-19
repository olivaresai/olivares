#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Populate a fresh Olivares install with a realistic, timeless demo team through the public API.

The dataset is the one every release regenerates its documentation captures from. It is idempotent
(every object is looked up before it is created, so a second run changes nothing), needs no vendor
account and no network (the tools are the stand-ins in scripts/demo-tools, answering from recorded
transcripts) and carries nothing private: the names, folders and conversations are fictional.

What it creates, all through the same HTTP routes the console and the CLI use:
  providers and provider profiles       Claude Code (two teams), Codex (two teams), Gemini CLI,
                                        OpenCode, and OpenCode on a local model; each adopted as a
                                        named account
  folders and repositories              five real git repositories, registered as workspaces
  templates and approval policies       three templates and three approval policies
  people and approvals                  four members, pending approvals and decided ones
  skills and MCP servers                a skills pack from a repository; two MCP servers, configured
                                        and not enabled (enabling one tests it, and a demo has no
                                        server to reach)
  rules, models and channels            egress rules, an owned model, two channels with messages
                                        (when the communications directory writer is activated)
  finished conversations                the recorded sessions of demo-tools/conversations.json
  budgets                               only on a build that serves FinOps (Business); a build that
                                        refuses it is reported, not failed
  edition                               read from the engine first: a Community estate holds no approval
                                        for a Business action (scripts/lib/demo_edition.py)

Access-map entries and audit records are not written here: the access map has no write route (it is
derived from observation), so the engine's own `--seed-demo` graph is what a capture shows, and every
call above lands in the signed audit ledger by itself.

Needs the sessions, governance and skills modules selected and the engine restarted since, and the
local model endpoint (scripts/demo-tools/demo-ollama.py) answering. It checks both and says what is
missing instead of changing the engine.

usage: seed-demo-dataset.py <base url> - <tenant> <work root> [--ollama-url URL]
       (the token is read from OLIVARES_TOKEN, never from argv; <base url> must be loopback: this creates
       administrator accounts with the demo password the engine prints, so it only runs on a demo estate;
       <work root> is a disposable directory this script owns)
"""
import argparse
import json
import os
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

LABEL = "seed-demo-dataset"
HERE = os.path.dirname(os.path.abspath(__file__))
DEMO_PASSWORD = "olivares-demo-estate"  # the published demo login; the engine prints it at start
# The demo estate's own team. The same people and addresses seed-demo-work.py creates, matched by email, so
# this runs after it; seed-demo-work.py itself answers 409 when these accounts already exist.
PEOPLE = (
    ("Rehema Okafor", "r.okafor@olivares.local", "admin"),
    ("Johan Lindqvist", "j.lindqvist@olivares.local", "editor"),
    ("Mei Tanaka", "m.tanaka@olivares.local", "editor"),
    ("Sami Abadi", "s.abadi@olivares.local", "viewer"),
)
# key, driver, display name, account name. The key is what conversations.json calls a profile.
PROFILES = (
    ("claude-platform", "claude", "Claude Code · Platform team", "claude-platform"),
    ("claude-web", "claude", "Claude Code · Web team", "claude-web"),
    ("codex-platform", "codex", "Codex · Platform team", "codex-platform"),
    ("codex-data", "codex", "Codex · Data team", "codex-data"),
    ("gemini-docs", "gemini-cli", "Gemini CLI · Docs team", "gemini-docs"),
    ("opencode-infra", "opencode", "OpenCode · Infrastructure", "opencode-infra"),
    ("opencode-local", "opencode", "OpenCode · Local models", "opencode-local"),
)
PROVIDERS = (
    ("anthropic", "Anthropic · team key"),
    ("openai", "OpenAI · team key"),
    ("gemini", "Google · Gemini key"),
)
LOCAL_PROVIDER = "Local models (Ollama)"

TEMPLATES = (
    ("Code review (read-only)", "Read the code and report findings. Nothing is written and every call is recorded.",
     {"settings": {"permission_mode": "dontAsk", "effort": "high"},
      "policies": {"allowed_tools": ["Read", "Grep"], "record_io": True}}),
    ("Incident response", "A two-hour session for an on-call engineer: read access, shell access, full recording.",
     {"settings": {"permission_mode": "dontAsk", "effort": "high"},
      "policies": {"allowed_tools": ["Read", "Grep", "Bash"], "max_session_duration_minutes": 120, "record_io": True}}),
    ("Release notes", "Summarize merged work for customers. Read-only and short.",
     {"settings": {"permission_mode": "dontAsk", "effort": "low"},
      "policies": {"allowed_tools": ["Read", "Bash"], "max_session_duration_minutes": 30}}),
)
APPROVAL_POLICIES = (
    ("Production changes need an approver",
     {"required_approvals": 1, "expires_in_seconds": 86400, "escalate_in_seconds": 14400,
      "match": {"action": "deploy.promote"}}),
    ("Evidence exports need one approver",
     {"required_approvals": 1, "expires_in_seconds": 259200, "match": {"action": "evidence.export"}}),
    ("New MCP servers need review",
     {"required_approvals": 1, "expires_in_seconds": 43200, "match": {"action": "mcp.enable"}}),
)
# There is deliberately no deny (ABAC) policy here. Creating one in a running 0.1 engine makes every consumer of
# authority evidence answer "unavailable" until the engine restarts: a skills import from a folder (503), and
# channel creation and the channel administration screen of the communications plane.
# Measured on the released 0.1 image.

REPOS = {
    "payments-api": {
        "README.md": "# payments-api\n\nBilling and refunds API for the storefront and partner integrations.\n",
        "go.mod": "module acme.example/payments-api\n\ngo 1.26\n",
        "cmd/api/main.go": "package main\n\nfunc main() {}\n",
        "internal/api/router.go": "package api\n\n// Middleware order: request ID, authentication, audit, handler.\n",
        "internal/api/refunds.go": "package api\n\n// CreateRefund handles POST /v1/refunds.\n",
        "internal/billing/refund.go": "package billing\n\n// Refund creates a refund for a captured payment.\n",
        "docs/runbooks/throttling.md": "# Throttling\n\nManual throttling is done at the load balancer.\n",
        ".agents/skills/release-notes/SKILL.md": (
            "---\nname: release-notes\ndescription: Draft customer-facing release notes from merged pull requests\n---\n\n"
            "Group merged work by audience and write one short sentence per change.\n"),
        ".agents/skills/api-review/SKILL.md": (
            "---\nname: api-review\ndescription: Review an API change for compatibility and error handling\n---\n\n"
            "Check status codes, field names and idempotency before approving a change.\n"),
        ".agents/skills/incident-summary/SKILL.md": (
            "---\nname: incident-summary\ndescription: Summarize an incident timeline for the postmortem\n---\n\n"
            "Write what happened, the impact, the fix and the follow-ups in plain sentences.\n"),
    },
    "storefront-web": {
        "README.md": "# storefront-web\n\nThe customer-facing storefront.\n",
        "package.json": "{\n  \"name\": \"storefront-web\",\n  \"private\": true,\n  \"scripts\": {\"test:e2e\": \"playwright test\"}\n}\n",
        "e2e/checkout.spec.ts": "// Places an order and expects the confirmation page.\n",
        "src/checkout/Checkout.tsx": "export function Checkout() {\n  return null\n}\n",
    },
    "data-pipelines": {
        "README.md": "# data-pipelines\n\nNightly exports and reporting jobs.\n",
        "export_orders.py": "def export_orders(day):\n    return []\n",
        "tests/test_export.py": "def test_export_runs():\n    assert True\n",
        "sql/shipments.sql": "SELECT * FROM shipments;\n",
    },
    "infra-live": {
        "README.md": "# infra-live\n\nLive infrastructure for staging and production.\n",
        "modules/staging_lb/main.tf": "resource \"aws_lb_target_group\" \"api\" {}\n",
        "envs/staging/main.tf": "module \"staging_lb\" {\n  source = \"../../modules/staging_lb\"\n}\n",
    },
    "docs-site": {
        "README.md": "# docs-site\n\nProduct documentation.\n",
        "docs/index.md": "# Welcome\n\nStart here.\n",
        "CHANGELOG.md": "# Changelog\n",
    },
}
# name -> (folder, mount mode, DLP mode). A session that writes to a folder with DLP labels asks a
# person before it starts, which is what payments-api shows: that launch is approved by a second admin.
WORKSPACES = {
    "payments-api": ("payments-api", "rw", "label"),
    "storefront-web": ("storefront-web", "rw", "off"),
    "data-pipelines": ("data-pipelines", "rw", "off"),
    "infra-live": ("infra-live", "ro", "label"),
    "docs-site": ("docs-site", "rw", "off"),
}
# Approvals: (action, subject kind, subject, reason, required approvals, decision by person index or None)
APPROVALS = (
    ("workspace.write", "workspace", "payments-api",
     "Codex asks to switch payments-api from read-only to read-write so it can apply the refund idempotency change.",
     1, None),
    ("mcp.enable", "mcp_server", "issue-tracker",
     "Enable the issue tracker connection for storefront-web. It can search issues and add comments.",
     1, None),
    ("deploy.promote", "deployment", "payments-api/production",
     "Promote the refund idempotency change to production after the staging soak.",
     1, None),
    ("workspace.write", "workspace", "data-pipelines",
     "Allow the Data team's Codex to write the batched export change in data-pipelines.", 1, "approve"),
    ("evidence.export", "audit", "q3-access-review",
     "Export the audit ledger for the external access review. Leaves the estate, so it needs a named approver.",
     1, "reject"),
)


# Egress rules of the inference proxy: what may not leave the estate. One rule per class.
DLP_RULES = (
    ("secret", "deny", "Block secrets from leaving the estate"),
    ("credential", "deny", "Block credentials and keys"),
    ("pii", "deny", "Block personal data from model prompts"),
)
OWNED_MODELS = (
    ("support-triage", "fine_tuned", "Fine-tuned for support ticket triage"),
)
# Channels of the team, and the messages one person left another in them (slug, subject, text, sender index).
CHANNELS = (
    ("platform-releases", "Platform releases", "Release coordination for the platform team", "coordination"),
    ("incident-review", "Incident review", "Follow-ups from the last on-call rotation", "incident"),
)
MESSAGES = (
    ("platform-releases", "Refund idempotency change is in staging",
     "The staging soak finished. Please review the production approval before the release window."),
    ("platform-releases", "Rate limiting plan is ready for review",
     "The per-client rate limiting plan is written up. I would like a second pair of eyes on the Redis fallback."),
    ("incident-review", "Throttling runbook updated",
     "I replaced the manual load balancer steps with the limiter settings. Please read it before your next rotation."),
)


class SeedError(Exception):
    pass


def _find_lib():
    candidates = [os.environ.get("OLIVARES_LIB_DIR", ""), os.path.join(HERE, "lib")]
    for c in candidates:
        if c and os.path.isfile(os.path.join(c, "redaccion.py")):
            return c
    return None


_LIB = _find_lib()
if _LIB is None:
    print("%s: cannot find scripts/lib/redaccion.py, the shared output boundary; refusing to run" % LABEL,
          file=sys.stderr)
    sys.exit(2)
sys.path.insert(0, _LIB)
from redaccion import Redactor, abre  # noqa: E402
from demo_edition import edition_of, keeps_action  # noqa: E402


class Api:
    def __init__(self, base, token, tenant):
        self.base, self.token, self.tenant = base.rstrip("/"), token, tenant

    def call(self, method, path, body=None, headers=None, token=None, anonymous=False):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method)
        if not anonymous:
            req.add_header("Authorization", "Bearer " + (token or self.token))
        req.add_header("X-Olivares-Tenant", self.tenant)
        if method != "GET":
            req.add_header("Idempotency-Key", str(uuid.uuid4()))
        if data is not None:
            req.add_header("Content-Type", "application/json")
        for k, v in (headers or {}).items():
            req.add_header(k, v)
        try:
            with abre(req, timeout=60) as resp:
                raw = resp.read().decode()
                return resp.status, (json.loads(raw) if raw else {})
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode(errors="replace")
            try:
                return exc.code, json.loads(raw)
            except ValueError:
                return exc.code, {"error": raw[:300]}

    def ok(self, what, method, path, body=None, headers=None, token=None, accept=(200, 201, 202)):
        code, resp = self.call(method, path, body, headers, token)
        if code not in accept:
            raise SeedError("%s: HTTP %s %s" % (what, code, json.dumps(resp)[:300]))
        return resp

    def items(self, what, path):
        """Every item of a list route, following `has_more` and `cursor`."""
        out, cursor = [], None
        for _ in range(200):
            sep = "&" if "?" in path else "?"
            url = path + sep + "limit=100" + ("&cursor=" + urllib.parse.quote(cursor) if cursor else "")
            page = self.ok(what, "GET", url)
            if "items" not in page:
                raise SeedError("%s: the list answered without `items`: %s" % (what, json.dumps(page)[:200]))
            out.extend(page["items"] or [])
            cursor = page.get("cursor") or page.get("next_cursor")
            if page.get("has_more") and not cursor:
                raise SeedError("%s: the list says more follows but gives no cursor" % what)
            if not page.get("has_more"):
                return out
        raise SeedError("%s: the list never ends" % what)


def say(text):
    print("  " + text, flush=True)


# ------------------------------------------------------------------------------ modules
# The modules the dataset reads and writes. Selecting them is a setting (Settings, Edition and modules, or
# PUT /v1/console/modules) that takes effect on a restart: a module started in place after the engine is up
# is not wired to the sessions module, and importing a skills pack from a folder answers 503 until the engine
# restarts. So this step checks and says what to do, and never switches a module itself.
REQUIRED_MODULES = ("sessions", "governance", "skills")


def step_modules(api, ctx):
    state = {m["name"]: m for m in api.ok("modules", "GET", "/v1/console/modules")["modules"]}
    off = [m for m in REQUIRED_MODULES if not state.get(m, {}).get("running")]
    if off:
        raise SeedError("modules: %s not running. Select them in Settings, Edition and modules, restart the "
                        "engine, then run this again" % ", ".join(off))
    say("modules: %s running" % ", ".join(REQUIRED_MODULES))


# ------------------------------------------------------------------------------ providers and profiles
def step_providers(api, ctx):
    have = {(p["kind"], p["display_name"].casefold()) for p in api.items("providers", "/v1/m/sessions/providers")}
    for kind, name in PROVIDERS:
        if (kind, name.casefold()) in have:
            continue
        # A placeholder, not a credential: the engine seals it and no demo tool ever sends it anywhere.
        api.ok("provider " + name, "POST", "/v1/m/sessions/providers", {
            "kind": kind, "display_name": name, "api_key": "demo-placeholder-%s-key-000001" % kind})
    if ("ollama", LOCAL_PROVIDER.casefold()) not in have:
        api.ok("provider " + LOCAL_PROVIDER, "POST", "/v1/m/sessions/providers", {
            "kind": "ollama", "display_name": LOCAL_PROVIDER, "base_url": ctx["ollama_url"]})
    by_name = {p["display_name"]: p for p in api.items("providers", "/v1/m/sessions/providers")}
    local = by_name[LOCAL_PROVIDER]
    tested = api.ok("test " + LOCAL_PROVIDER, "POST", "/v1/m/sessions/providers/%s/test" % local["provider_ref"], {})
    if tested.get("probe_state") != "ok" or not tested.get("models"):
        raise SeedError("providers: the local model endpoint %s answered %s; start scripts/demo-tools/"
                        "demo-ollama.py first" % (ctx["ollama_url"], tested.get("probe_state") or "nothing"))
    ctx["local_provider_ref"] = local["provider_ref"]
    say("providers: %d registered, local models %s" % (len(by_name), ", ".join(tested["models"])))


def refuse_link(path):
    if os.path.islink(path):
        raise SeedError("%s is a symbolic link; the work root must be a directory this script owns" % path)


def home_for(ctx, key, driver):
    base = os.path.join(ctx["root"], "homes", key)
    refuse_link(base)
    config = os.path.join(base, ".gemini" if driver == "gemini-cli" else "config")
    user = os.path.join(base, "user")
    os.makedirs(config, exist_ok=True)
    os.makedirs(user, exist_ok=True)
    if driver == "gemini-cli":
        # Gemini CLI's sign-in is two files it wrote itself; the placeholders below say "Google account".
        with open(os.path.join(config, "settings.json"), "w", encoding="utf8") as fh:
            json.dump({"security": {"auth": {"selectedType": "oauth-personal"}}}, fh)
        with open(os.path.join(config, "oauth_creds.json"), "w", encoding="utf8") as fh:
            json.dump({"note": "demo placeholder, not a credential"}, fh)
    return os.path.realpath(config), os.path.realpath(user)


def find_profile(api, driver, config, user):
    """The profile whose immutable identity (driver and both homes) matches, or None."""
    for item in api.items("profiles", "/v1/m/sessions/provider-profiles?state=active&driver=" + driver):
        ref = urllib.parse.quote(item["profile_ref"], safe="")
        code, conf = api.call("GET", "/v1/m/sessions/provider-profiles/%s/configuration" % ref)
        if code == 200 and conf.get("config_home") == config and conf.get("user_home") == user:
            return item
    return None


def step_profiles(api, ctx):
    profiles = {}
    accounts = {a["account_ref"] for a in api.items("accounts", "/v1/m/sessions/provider-accounts")}
    for key, driver, display, account in PROFILES:
        config, user = home_for(ctx, key, driver)
        body = {"driver": driver, "config_home": config, "user_home": user, "display_name": display}
        if key == "opencode-local":
            body.update({"auth_source": "managed_injection", "provider_record_ref": ctx["local_provider_ref"]})
        else:
            body["auth_source"] = "provider_account_home"
        code, created = api.call("POST", "/v1/m/sessions/provider-profiles", body)
        if code == 201:
            ref = created["profile_ref"]
        elif code == 409:
            existing = find_profile(api, driver, config, user)
            if not existing:
                raise SeedError("profile %s: the homes are taken by a profile that cannot be read back" % display)
            ref = existing["profile_ref"]
        else:
            raise SeedError("profile %s: HTTP %s %s" % (display, code, json.dumps(created)[:300]))
        if ref not in accounts:
            api.ok("account " + account, "POST", "/v1/m/sessions/provider-accounts/%s/adopt" % ref, {"name": account})
        profiles[key] = ref
    for key, ref in profiles.items():
        readiness = api.ok("readiness " + key, "GET", "/v1/m/sessions/provider-profiles/%s/launch-readiness" % ref)
        if readiness.get("configuration_state") != "ready":
            raise SeedError("profile %s is not launch-ready: %s" % (key, json.dumps(readiness)[:300]))
    ctx["profiles"] = profiles
    say("profiles: %d launch-ready, each a named account" % len(profiles))


# ------------------------------------------------------------------------------ folders and repositories
GIT_ENV = {"GIT_AUTHOR_NAME": "Acme Platform", "GIT_AUTHOR_EMAIL": "dev@acme.example",
           "GIT_COMMITTER_NAME": "Acme Platform", "GIT_COMMITTER_EMAIL": "dev@acme.example",
           "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_SYSTEM": os.devnull}


def git(repo, *args):
    # Only PATH and the identity: no token, and no inherited GIT_DIR that would point git at another repository.
    env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "LC_ALL": "C"}
    env.update(GIT_ENV)
    subprocess.run(["git", "-C", repo] + list(args), check=True, env=env, stdout=subprocess.DEVNULL,
                   stderr=subprocess.PIPE)


def make_repo(root, name, files):
    final = os.path.join(root, "repos", name)
    refuse_link(final)
    if os.path.isdir(os.path.join(final, ".git")):
        return os.path.realpath(final)
    # Built aside and renamed when complete, so an interrupted run never leaves a repository without history.
    repo = final + ".partial"
    shutil.rmtree(repo, ignore_errors=True)
    os.makedirs(repo)
    git(repo, "init", "-q", "-b", "main")
    for rel, text in files.items():
        path = os.path.join(repo, rel)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf8") as fh:
            fh.write(text)
    git(repo, "add", "-A")
    git(repo, "commit", "-q", "-m", "Initial commit")
    with open(os.path.join(repo, "README.md"), "a", encoding="utf8") as fh:
        fh.write("\nSee the runbooks and the team channel for how this repository is operated.\n")
    git(repo, "commit", "-q", "-am", "Document where to find the runbooks")
    shutil.rmtree(final, ignore_errors=True)
    os.rename(repo, final)
    return os.path.realpath(final)


def step_workspaces(api, ctx):
    existing = {w["root_path"]: w for w in api.items("workspaces", "/v1/m/sessions/workspaces")}
    refs = {}
    for name, (folder, mode, dlp) in WORKSPACES.items():
        root = make_repo(ctx["root"], folder, REPOS[folder])
        if root in existing:
            refs[name] = existing[root]["workspace_ref"]
            continue
        created = api.ok("workspace " + name, "POST", "/v1/m/sessions/workspaces", {
            "name": name, "root_path": root, "mount_mode": mode, "container_target": "/workspace",
            "dlp_mode": dlp})
        refs[name] = created["workspace_ref"]
    ctx["workspaces"] = refs
    say("folders: %d git repositories registered" % len(refs))


# ------------------------------------------------------------------------------ templates and policies
def step_templates(api, ctx):
    have = {t["name"]: t for t in api.items("templates", "/v1/m/sessions/templates")}
    for name, description, body in TEMPLATES:
        if name not in have:
            api.ok("template " + name, "POST", "/v1/m/sessions/templates",
                   {"name": name, "description": description, "body": body})
    ctx["templates"] = {t["name"]: t["id"] for t in api.items("templates", "/v1/m/sessions/templates")}
    say("templates: %d available" % len(ctx["templates"]))


def step_edition(api, ctx):
    """Read the edition once: a demo estate holds only what its edition does (scripts/lib/demo_edition.py)."""
    code, info = api.call("GET", "/v1/server-info")
    try:
        ctx["edition"] = edition_of(code, info)
    except ValueError as exc:
        raise SeedError("edition: %s" % exc)
    say("edition: %s" % ctx["edition"])


def step_policies(api, ctx):
    have = {p["name"] for p in api.items("policies", "/v1/m/governance/policies")}
    for name, spec in APPROVAL_POLICIES:
        if not keeps_action(ctx.get("edition"), spec["match"]["action"]):
            continue
        if name not in have:
            api.ok("policy " + name, "POST", "/v1/m/governance/policies",
                   {"name": name, "kind": "approval", "enabled": True, "spec": spec})
    say("policies: %d" % len(api.items("policies", "/v1/m/governance/policies")))


# ------------------------------------------------------------------------------ people and approvals
def step_people(api, ctx):
    have = {m["email"].lower(): m for m in api.items("members", "/v1/members")}
    for display, email, role in PEOPLE:
        if email not in have:
            api.ok("member " + email, "POST", "/v1/users", {
                "email": email, "display_name": display, "password": DEMO_PASSWORD, "role": role,
                "tenant": api.tenant, "superadmin": False})
    say("people: %d members" % len(api.items("members", "/v1/members")))


def person_token(api, email):
    code, resp = api.call("POST", "/v1/auth/login", {"email": email, "password": DEMO_PASSWORD}, anonymous=True)
    if code != 200 or not resp.get("token"):
        raise SeedError("login of %s: HTTP %s" % (email, code))
    return resp["token"]


def approval_status(a):
    return a.get("effective_status") or a.get("status") or a.get("state")


def step_approvals(api, ctx):
    queue = api.items("approvals", "/v1/m/governance/approvals")
    if any(a.get("subject_ref") is None for a in queue):
        raise SeedError("approvals: the list does not carry subject_ref, so a request cannot be told apart "
                        "from one already seeded")
    have = {(a.get("action"), a.get("subject_ref")): a for a in queue}
    reviewer = next(p for p in PEOPLE if p[2] == "admin")
    for action, kind, subject, reason, required, decision in APPROVALS:
        if not keeps_action(ctx.get("edition"), action):
            continue
        existing = have.get((action, subject))
        if existing is None:
            existing = api.ok("approval " + subject, "POST", "/v1/m/governance/approvals", {
                "action": action, "subject_kind": kind, "subject_ref": subject, "reason": reason,
                "required_approvals": required, "expires_in_seconds": 604800})
            pending = True
        else:
            pending = approval_status(existing) == "pending"
        # A request created but not yet decided (an interrupted run) is decided now, not skipped.
        if decision and pending:
            note = ("Reviewed the diff and the staging run; go ahead." if decision == "approve" else
                    "The access review does not need the full ledger. Export the filtered view instead.")
            api.ok("decision on " + subject, "POST", "/v1/m/governance/approvals/%s/decisions" % existing["id"],
                   {"decision": decision, "note": note}, token=person_token(api, reviewer[1]))
    states = {}
    for a in api.items("approvals", "/v1/m/governance/approvals"):
        states[approval_status(a)] = states.get(approval_status(a), 0) + 1
    say("approvals: %d requests, states %s" % (sum(states.values()), states))


# ------------------------------------------------------------------------------ skills and MCP servers
def step_skills(api, ctx):
    packs = api.items("skills", "/v1/m/skills/packs")
    if not any(p["name"] == "Platform team skills" for p in packs):
        api.ok("skills pack", "POST", "/v1/m/skills/packs", {
            "name": "Platform team skills",
            "source": {"kind": "workspace", "workspace_ref": ctx["workspaces"]["payments-api"],
                       "directory": ".agents/skills"}})
    say("skills: %d pack(s)" % len(api.items("skills", "/v1/m/skills/packs")))


def step_mcp(api, ctx):
    gateway = api.ok("mcp gateway", "GET", "/v1/console/mcp-gateway")
    have = {s["name"] for s in gateway.get("servers") or []}
    version = gateway.get("version", 0)
    for name, url in (("Issue tracker", "https://mcp.example.com/issues"),
                      ("Repository search", "https://mcp.example.com/repos")):
        if name in have:
            continue
        resp = api.ok("mcp " + name, "POST", "/v1/console/mcp-gateway/servers", {
            "version": version,
            "server": {"name": name, "transport": "streamable_http", "url": url, "enabled": False,
                       "trust": {"issuer": "https://auth.example.com", "resource": url}}})
        version = resp.get("version", version + 1)
    say("mcp: %d server(s) configured" % len(api.ok("mcp", "GET", "/v1/console/mcp-gateway").get("servers") or []))


# ------------------------------------------------------------------------------ finished conversations
def run_stamp(api, ref):
    run = api.ok("run", "GET", "/v1/m/sessions/runs/" + ref)
    return (run.get("last_activity_at"), run.get("input_tokens"), run.get("output_tokens"), run.get("last_event_seq"))


def quiet(api, ref, before, patience=0.6, floor=3.0, limit=30.0):
    """Wait until the run's stamp moved off `before` (read before the input) and held still for `patience`.
    A tool that reports nothing countable gets `floor` seconds; a run that never settles is an error."""
    started, changed_at, seen = time.time(), None, before
    while time.time() - started < limit:
        stamp = run_stamp(api, ref)
        if stamp != seen:
            seen, changed_at = stamp, time.time()
        elif changed_at is not None and time.time() - changed_at >= patience:
            return seen
        elif changed_at is None and time.time() - started >= floor:
            return seen
        time.sleep(0.15)
    raise SeedError("run %s kept changing for %d seconds after an input" % (ref, limit))


def pending_launches(api):
    return {a["id"] for a in api.items("approvals", "/v1/m/governance/approvals")
            if a.get("action") == "sessions.run.launch"
            and (a.get("effective_status") or a.get("status")) == "pending"}


def approve_launch(api, name, before, note):
    """A launch that writes to a classified folder waits for a person; a second admin approves it.
    The request is the one that appeared since `before`: a waiting run has no session identity to match."""
    for _ in range(40):
        new = pending_launches(api) - before
        if new:
            break
        time.sleep(0.25)
    else:
        raise SeedError("session %s waits for approval but no new launch request is pending" % name)
    reviewer = next(p for p in PEOPLE if p[2] == "admin")
    api.ok("approval of " + name, "POST", "/v1/m/governance/approvals/%s/decisions" % sorted(new)[0],
           {"decision": "approve", "note": note}, token=person_token(api, reviewer[1]))


def step_sessions(api, ctx):
    with open(os.path.join(HERE, "demo-tools", "conversations.json"), encoding="utf8") as fh:
        sessions = json.load(fh)["sessions"]
    have = {r["name"]: r for r in api.items("runs", "/v1/m/sessions/runs")}
    started = 0
    for s in sessions:
        if s["name"] in have:
            if have[s["name"]].get("state") != "stopped":
                raise SeedError("session %s exists and is %s, not stopped: start from a fresh estate" % (
                    s["name"], have[s["name"]].get("state")))
            continue
        body = {"name": s["name"], "transport": "stream-json", "permission_mode": "default", "effort": "medium",
                "model": s["model"], "workspace_ref": ctx["workspaces"][s["folder"]], "isolation": "native",
                # The stand-in reads this one name: without it a Claude session would announce the id of
                # the engine's observed demo session and be joined to it instead of standing alone.
                "env_allow": ["DEMO_SESSION_UNIQUE"], "provider_profile_ref": ctx["profiles"][s["profile"]]}
        if s.get("template"):
            # A template that lists allowed tools runs under dontAsk, so the launch asks for no less.
            body["template_id"] = ctx["templates"][s["template"]]
            body["permission_mode"] = "dontAsk"
        before = pending_launches(api)
        ref = api.ok("session " + s["name"], "POST", "/v1/m/sessions/runs", body)["run_ref"]
        run = api.ok("run", "GET", "/v1/m/sessions/runs/" + ref)
        if run.get("state") == "waiting_approval":
            approve_launch(api, s["name"], before, "The folder holds internal code only. Approved for the platform team.")
            for _ in range(60):
                run = api.ok("run", "GET", "/v1/m/sessions/runs/" + ref)
                if run.get("state") != "waiting_approval":
                    break
                time.sleep(0.25)
        for turn in s["turns"]:
            before = run_stamp(api, ref)
            api.ok("session %s: input" % s["name"], "POST", "/v1/m/sessions/runs/%s/input" % ref,
                   {"text": turn["prompt"]})
            quiet(api, ref, before)
        deadline = time.time() + 30
        while True:
            final = api.ok("run", "GET", "/v1/m/sessions/runs/" + ref)
            if final.get("state") not in ("running", "idle", "pending"):
                break
            if time.time() > deadline:
                raise SeedError("session %s did not end within 30 seconds after its last turn" % s["name"])
            time.sleep(0.3)
        if final.get("state") != "stopped" or final.get("exit_code") != 0:
            raise SeedError("session %s ended as %s (exit %s): %s" % (
                s["name"], final.get("state"), final.get("exit_code"), final.get("reason")))
        started += 1
    states = {}
    for r in api.items("runs", "/v1/m/sessions/runs"):
        if r["name"] in {s["name"] for s in sessions}:
            states[r.get("state")] = states.get(r.get("state"), 0) + 1
    if set(states) != {"stopped"} or sum(states.values()) != len(sessions):
        raise SeedError("sessions: every recorded conversation must have finished, got %s of %d" % (states, len(sessions)))
    say("sessions: %d finished conversations (%d new)" % (len(sessions), started))


# ------------------------------------------------------------------------------ rules, models, channels
def module_running(api, name):
    state = {m["name"]: m for m in api.ok("modules", "GET", "/v1/console/modules")["modules"]}
    return bool(state.get(name, {}).get("running"))


def step_dlp(api, ctx):
    if not module_running(api, "inferenceproxy"):
        say("dlp: the inferenceproxy module is not running; nothing seeded")
        return
    for klass, action, note in DLP_RULES:
        # Upserted by class: a second run rewrites the same rule.
        api.ok("dlp rule " + klass, "PUT", "/v1/m/inferenceproxy/dlp/rules",
               {"class": klass, "action": action, "note": note})
    rules = api.ok("dlp rules", "GET", "/v1/m/inferenceproxy/dlp/rules")
    have = {r["class"] for r in rules.get("items") or rules.get("rules") or []}
    missing = [k for k, _, _ in DLP_RULES if k not in have]
    if missing:
        raise SeedError("dlp: rules did not read back: %s" % ", ".join(missing))
    say("dlp: %d egress rules" % len(have))


def step_models(api, ctx):
    if not module_running(api, "models"):
        say("models: the models module is not running; nothing seeded")
        return
    have = {m["name"] for m in api.items("models", "/v1/m/models/owned-models")}
    for name, kind, note in OWNED_MODELS:
        if name not in have:
            api.ok("model " + name, "POST", "/v1/m/models/owned-models", {
                "name": name, "kind": kind, "status": "active", "visibility": "internal", "note": note})
    say("models: %d owned" % len(api.items("models", "/v1/m/models/owned-models")))


def inbox_messages(inbox):
    """The (channel, subject) of every message an inbox page holds."""
    out = set()
    for item in inbox.get("items") or []:
        message = item.get("message") or {}
        out.add((message.get("channel_id"), (message.get("content") or {}).get("subject")))
    return out


def step_communications(api, ctx):
    if ctx.get("no_communications"):
        say("communications: skipped by --no-communications")
        return
    workspaces = api.ok("workspaces", "GET", "/v1/workspaces").get("items") or []
    default = [w for w in workspaces if w.get("is_default")] or workspaces
    if not default:
        raise SeedError("communications: the engine has no workspace")
    workspace = default[0]["id"]
    me = api.ok("whoami", "GET", "/v1/auth/whoami")["actor"].split(":", 1)[1]
    members = {m["email"].lower(): m["user_id"] for m in api.items("members", "/v1/members")}
    sender_email = next(p[1] for p in PEOPLE if p[2] == "admin")
    sender = members[sender_email]
    channels = {c["slug"]: c["id"] for c in api.items(
        "channels", "/v1/m/sessions/channels?workspace_id=" + urllib.parse.quote(workspace))}
    for slug, name, description, kind in CHANNELS:
        if slug in channels:
            continue
        code, resp = api.call("POST", "/v1/m/sessions/channels", {
            "workspace_id": workspace, "slug": slug, "name": name, "description": description, "kind": kind,
            "initial_grants": [
                {"subject": {"kind": "user", "ref": me}, "can_read": True, "can_write": True, "can_admin": True},
                {"subject": {"kind": "user", "ref": sender}, "can_read": True, "can_write": True,
                 "can_admin": False}]})
        if code == 503 and resp.get("code") == "evidence_unavailable":
            # The directory writer needs its one-time activation of the store, and an engine whose authority
            # evidence is unavailable refuses every communications route. Say which, and stop.
            raise SeedError("channel %s: the communications plane is unavailable (HTTP 503 evidence_unavailable): "
                            "activate the directory writer or restart the engine, or pass --no-communications"
                            % slug)
        if code not in (200, 201):
            raise SeedError("channel %s: HTTP %s %s" % (slug, code, json.dumps(resp)[:300]))
        channels[slug] = resp["channel"]["id"]
    # A message is matched by its channel and subject in the owner's inbox, so a run that stopped after a
    # channel or after one message sends only what is missing.
    inbox_path = "/v1/m/sessions/inbox?workspace_id=%s&limit=200" % urllib.parse.quote(workspace)
    inbox = api.ok("inbox", "GET", inbox_path)
    if inbox.get("has_more"):
        raise SeedError("inbox: more than 200 deliveries on a demo estate; start from a fresh one")
    sent = inbox_messages(inbox)
    token = person_token(api, sender_email)
    for slug, subject, text in MESSAGES:
        if (channels[slug], subject) in sent:
            continue
        api.ok("message " + subject, "POST", "/v1/m/sessions/messages/send", {
            "channel_id": channels[slug], "recipient": {"kind": "user", "ref": me}, "urgency": "normal",
            "content": {"subject": subject, "blocks": [{"type": "text", "format": "plain", "text": text}]}},
            token=token)
    inbox = api.ok("inbox", "GET", inbox_path)
    have = inbox_messages(inbox)
    missing = [subject for slug, subject, _ in MESSAGES if (channels[slug], subject) not in have]
    if missing:
        raise SeedError("communications: the owner's inbox does not hold %s" % ", ".join(repr(m) for m in missing))
    say("communications: %d channels, %d messages in the inbox" % (len(CHANNELS), len(inbox.get("items") or [])))


# ------------------------------------------------------------------------------ budgets
BUDGETS = (
    ("Platform team · monthly", 1_500_000_000, "monthly", "global", "alert"),
    ("Experiments sandbox · daily", 40_000_000, "daily", "global", "block"),
)


def step_budgets(api, ctx):
    code, resp = api.call("GET", "/v1/m/finops/budgets")
    if code == 404 and (resp.get("error") or {}).get("code") == "module_not_enabled":
        say("budgets: the finops module is not enabled on this engine; nothing seeded")
        return
    if code != 200:
        raise SeedError("budgets: HTTP %s %s" % (code, json.dumps(resp)[:300]))
    have = {b["name"] for b in resp.get("items") or []}
    for name, limit, period, dimension, action in BUDGETS:
        if name in have:
            continue
        code, made = api.call("POST", "/v1/m/finops/budgets", {
            "name": name, "limit_micro_usd": limit, "period": period, "dimension": dimension,
            "action": action, "enabled": True, "thresholds": [0.5, 0.8, 1.0]})
        if code == 501 and "Business feature" in json.dumps(made):
            say("budgets: FinOps is a Business capability on this build; nothing seeded")
            return
        if code not in (200, 201):
            raise SeedError("budget %s: HTTP %s %s" % (name, code, json.dumps(made)[:300]))
    say("budgets: %d" % len(api.items("budgets", "/v1/m/finops/budgets")))


STEPS = (("edition", step_edition), ("modules", step_modules), ("providers", step_providers), ("profiles", step_profiles),
         ("workspaces", step_workspaces), ("templates", step_templates), ("skills", step_skills),
         ("mcp", step_mcp), ("policies", step_policies), ("people", step_people),
         ("communications", step_communications), ("approvals", step_approvals), ("dlp", step_dlp),
         ("models", step_models), ("sessions", step_sessions), ("budgets", step_budgets))


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("base_url")
    parser.add_argument("token", help="must be -: the bearer token is read from OLIVARES_TOKEN")
    parser.add_argument("tenant")
    parser.add_argument("work_root", help="disposable directory for the repositories and tool homes")
    parser.add_argument("--ollama-url", default="http://127.0.0.1:11434",
                        help="the local model endpoint the demo provider points at")
    parser.add_argument("--no-communications", action="store_true",
                        help="leave the channels and messages out, for an engine whose directory writer "
                             "is not activated; the capture harness always activates it")
    args = parser.parse_args()
    if args.token != "-":
        parser.error("the token is read from OLIVARES_TOKEN; pass - here, never the value")
    token = os.environ.get("OLIVARES_TOKEN", "")
    if not token:
        parser.error("OLIVARES_TOKEN is required")
    host = urllib.parse.urlsplit(args.base_url).hostname or ""
    if host not in ("127.0.0.1", "localhost", "::1"):
        parser.error("refusing %r: the demo dataset creates accounts with a published password and only "
                     "runs against a loopback demo engine" % host)
    os.makedirs(args.work_root, exist_ok=True)
    api = Api(args.base_url, token, args.tenant)
    ctx = {"root": os.path.realpath(args.work_root), "ollama_url": args.ollama_url.rstrip("/"),
           "no_communications": args.no_communications}
    redact = Redactor()
    redact.recuerda(token, DEMO_PASSWORD)
    try:
        for _, fn in STEPS:
            fn(api, ctx)
    except SeedError as exc:
        print(redact("%s: ERROR: %s" % (LABEL, exc)), file=sys.stderr)
        return 1
    except (urllib.error.URLError, OSError) as exc:
        print(redact("%s: ERROR: the engine at %s did not answer: %s" % (LABEL, args.base_url, exc)),
              file=sys.stderr)
        return 1
    except subprocess.CalledProcessError as exc:
        print(redact("%s: ERROR: git failed: %s" % (LABEL, (exc.stderr or b"").decode(errors="replace")[:300])),
              file=sys.stderr)
        return 1
    print("%s: done" % LABEL)
    return 0


if __name__ == "__main__":
    sys.exit(main())
