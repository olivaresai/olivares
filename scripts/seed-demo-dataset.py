#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Populate a fresh Olivares install with the canonical Northwind estate through the public API.

The dataset is the one every release regenerates its documentation captures from. It is idempotent
(every object is looked up before it is created, so a second run changes nothing), needs no vendor
account and no network (the tools are the stand-ins in scripts/demo-tools, answering from recorded
transcripts) and carries nothing private: the people, folders and conversations are fictional.

What it creates, all through the same HTTP routes the console and the CLI use:
  providers and profiles               Anthropic and OpenAI team keys; Claude Code for Lena, Sam,
                                       Tomas and Ana, Codex for Marc and the nightly audit, OpenCode
                                       on the Anthropic team key, and Gemini CLI installed but not
                                       signed in; each signed-in profile is adopted as a named account
  folders and repositories             the five Northwind folders, each a real git repository
                                       registered read-write with DLP off
  templates and approval policies      three templates and five approval policies
  people and approvals                 the six people of the estate, three requests still waiting and
                                       nine already answered (eight approved, one denied)
  skills and MCP servers               a skills pack from payments-api; GitHub, Postgres analytics
                                       (read-only) and Sentry, configured and not enabled
  rules and channels                   egress rules and two channels with messages (when the
                                       communications directory writer is activated)
  sessions                             the recorded conversations of demo-tools/conversations.json:
                                       seven live and nine finished, $6.84 today, one unpriced
  budgets                              one $400 monthly budget; only on a build that serves FinOps
                                       (Business), and a build that refuses it is reported, not failed
  edition                              read from the engine first: a Community estate holds no approval
                                       for a Business action (scripts/lib/demo_edition.py)

The OpenCode session reports no usage, so it is the one unpriced session; the explicit $6.84 total is
carried by the Claude and Codex sessions.

Access-map entries and audit records are not written here: the access map has no write route (it is
derived from observation), and every call above lands in the signed audit ledger by itself. The capture
harness no longer boots the engine's `--seed-demo`, whose ten synthetic sessions would join the canonical
sixteen, so the map shows only what the seeded estate and its connectors observe.

Needs the sessions, governance and skills modules selected and the engine restarted since. It checks
that and says what is missing instead of changing the engine.

usage: seed-demo-dataset.py <base url> - <tenant> <work root> [--no-communications]
       [--verify-live-plane]
       (the token is read from OLIVARES_TOKEN, never from argv; <base url> must be loopback: this creates
       administrator accounts with the demo password the engine prints, so it only runs on a demo estate;
       <work root> is a disposable directory this script owns)
"""
import argparse
import datetime
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
DEMO_PASSWORD = "olivares-demo-estate"  # the published demo login the harness sets at first boot
DEMO_EMAIL = "demo@olivares.local"  # minted by first-boot setup (`POST /v1/setup`) or `serve --seed-demo`
# The six people of the Northwind estate. The capture harness runs this dataset before seed-demo-work.py and
# tells that seeder not to create people, approvals or a run: this table is their one source.
PEOPLE = (
    ("Ana Ruiz", "ana.ruiz@olivares.local", "admin"),
    ("Lena Fischer", "lena.fischer@olivares.local", "editor"),
    ("Marc Vidal", "marc.vidal@olivares.local", "editor"),
    ("Sam Okafor", "sam.okafor@olivares.local", "editor"),
    ("Priya Nair", "priya.nair@olivares.local", "editor"),
    ("Tomas Gil", "tomas.gil@olivares.local", "editor"),
)
# The tools on this server, and who each one signs in as. `key` is what conversations.json calls a
# profile; `owner` is the person whose homes and account carry the sign-in; `signed_in` false means the
# tool is installed but has no account, so no session uses it. A managed-injection profile binds the
# provider record named by `provider` instead of an own login.
# `account` is the engine name, a lowercase ASCII slug; `account_display` is the person or team
# the capture shows. The engine refuses a spaces-and-accents name at adopt, so the human label is
# patched into display_name once the account exists.
# A profile and the account it is adopted as share ONE label (modules/sessions/provider_account.go:
# a display-name PATCH writes the profile's display column), so the label is the sign-in the canonical
# sheet names, not a second invented title. The console reads "Runs as Claude Code · Lena's Claude
# account" from the driver and this label together.
PROFILES = (
    {"key": "claude-lena", "driver": "claude", "display": "Lena's Claude account",
     "account": "lena-claude", "account_display": "Lena's Claude account",
     "owner": "Lena Fischer", "auth_source": "provider_account_home", "signed_in": True},
    {"key": "claude-sam", "driver": "claude", "display": "Sam's Claude account",
     "account": "sam-claude", "account_display": "Sam's Claude account",
     "owner": "Sam Okafor", "auth_source": "provider_account_home", "signed_in": True},
    {"key": "claude-tomas", "driver": "claude", "display": "Tomas's Claude account",
     "account": "tomas-claude", "account_display": "Tomas's Claude account",
     "owner": "Tomas Gil", "auth_source": "provider_account_home", "signed_in": True},
    {"key": "claude-ana", "driver": "claude", "display": "Ana's Claude account",
     "account": "ana-claude", "account_display": "Ana's Claude account",
     "owner": "Ana Ruiz", "auth_source": "provider_account_home", "signed_in": True},
    {"key": "codex-marc", "driver": "codex", "display": "Marc's ChatGPT account",
     "account": "marc-chatgpt", "account_display": "Marc's ChatGPT account",
     "owner": "Marc Vidal", "auth_source": "provider_account_home", "signed_in": True},
    {"key": "codex-team", "driver": "codex", "display": "OpenAI team key",
     "account": "openai-team", "account_display": "OpenAI team key",
     "owner": "Sam Okafor", "auth_source": "managed_injection",
     "provider": "openai", "signed_in": True},
    {"key": "opencode-team", "driver": "opencode", "display": "Anthropic team key",
     "account": "anthropic-team", "account_display": "Anthropic team key",
     "owner": "Priya Nair", "auth_source": "managed_injection",
     "provider": "anthropic", "signed_in": True},
    {"key": "gemini-docs", "driver": "gemini-cli", "display": "Gemini CLI",
     "account": None, "account_display": None,
     "owner": "Ana Ruiz", "auth_source": "provider_account_home", "signed_in": False},
)
PROVIDERS = (
    ("anthropic", "Anthropic · team key"),
    ("openai", "OpenAI · team key"),
)

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
# No policy or request carries an expiry (or an escalation): a matching policy overwrites both windows
# from the request, and a non-zero expiry writes `expires_at`, after which `effectiveStatus` reports the
# pending row as expired — a terminal state that would empty the three requests this estate keeps
# waiting for the life of the estate (modules/governance/approvals.go:250-256,293-295,164-173).
APPROVAL_POLICIES = (
    ("Database migrations need a second person",
     {"required_approvals": 1, "match": {"action": "db.migrate"}}),
    ("New network destinations need approval",
     {"required_approvals": 1, "match": {"action": "network.connect"}}),
    ("Write access to GitHub needs approval",
     {"required_approvals": 1, "match": {"action": "github.write"}}),
    ("Production changes need an approver",
     {"required_approvals": 1, "match": {"action": "deploy.promote"}}),
    ("Evidence exports need one approver",
     {"required_approvals": 1, "match": {"action": "evidence.export"}}),
)
# There is deliberately no deny (ABAC) policy here. Creating one in a running 0.1 engine makes every consumer of
# authority evidence answer "unavailable" until the engine restarts: a skills import from a folder (503), and
# channel creation and the channel administration screen of the communications plane.
# Measured on the released 0.1 image.

REPOS = {
    "payments-api": {
        "README.md": "# payments-api\n\nBilling and refunds API for the storefront and partner integrations.\n",
        "go.mod": "module northwind.example/payments-api\n\ngo 1.26\n",
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
    "infra": {
        "README.md": "# infra\n\nInfrastructure for staging and production.\n",
        "modules/staging_lb/main.tf": "resource \"aws_lb_target_group\" \"api\" {}\n",
        "envs/staging/main.tf": "module \"staging_lb\" {\n  source = \"../../modules/staging_lb\"\n}\n",
    },
    "docs-site": {
        "README.md": "# docs-site\n\nProduct documentation.\n",
        "docs/index.md": "# Welcome\n\nStart here.\n",
        "CHANGELOG.md": "# Changelog\n",
    },
}
# name -> (folder, mount mode, DLP mode). Every canonical folder is writable and carries no DLP label:
# the estate asks people about migrations, network destinations and GitHub writes, never about a launch.
WORKSPACES = {
    "storefront-web": ("storefront-web", "rw", "off"),
    "payments-api": ("payments-api", "rw", "off"),
    "infra": ("infra", "rw", "off"),
    "docs-site": ("docs-site", "rw", "off"),
    "data-pipelines": ("data-pipelines", "rw", "off"),
}
# Requests still waiting, and requests already answered, in the canonical estate. The session column
# names the session that asked; the engine records a person's request against the person who asked it.
PENDING_APPROVALS = (
    ("db.migrate", "database", "payments-staging",
     "The staging invoice schema needs the ledger reference column; the migration runs after the backup check.",
     "Reconcile invoice export with billing ledger", "marc.vidal@olivares.local", 1),
    ("network.connect", "network_destination", "registry.terraform.io",
     "The provider upgrade reaches the Terraform registry; the session has read the lock file so far.",
     "Upgrade Terraform AWS provider to 6.x", "sam.okafor@olivares.local", 1),
    ("github.write", "repository", "storefront-web#482",
     "The checkout refactor is ready to open a pull request against the storefront.",
     "Refactor cart totals in checkout", "lena.fischer@olivares.local", 1),
)
DECIDED_APPROVALS = (
    ("db.migrate", "database", "staging-dev",
     "The staging schema change was reviewed against the backup plan.", "marc.vidal@olivares.local", "approve"),
    ("db.migrate", "database", "staging-db",
     "The billing database migration was reviewed with the rollback steps.", "sam.okafor@olivares.local", "approve"),
    ("network.connect", "network_destination", "pypi.org",
     "The payments API needs the package index during the build.", "lena.fischer@olivares.local", "approve"),
    ("network.connect", "network_destination", "api.stripe.com",
     "The refund handler calls the payment provider.", "marc.vidal@olivares.local", "approve"),
    ("github.write", "repository", "storefront-web#123",
     "The cart copy change was reviewed before the pull request.", "lena.fischer@olivares.local", "approve"),
    ("github.write", "repository", "storefront-web#456",
     "The change touched release notes and needs another owner.", "tomas.gil@olivares.local", "reject"),
    ("network.connect", "network_destination", "registry.npmjs.org",
     "The docs build installs its packages from the npm registry.", "priya.nair@olivares.local", "approve"),
    ("deploy.promote", "deployment", "payments-api/production",
     "The refund idempotency change passed the staging soak.", "marc.vidal@olivares.local", "approve"),
    ("evidence.export", "audit", "q3-access-review",
     "The access review needs the filtered ledger export.", "lena.fischer@olivares.local", "approve"),
)
MCP_SERVERS = (
    ("GitHub", "https://mcp.northwind.example/github"),
    ("Postgres analytics (read-only)", "https://mcp.northwind.example/postgres-analytics"),
    ("Sentry", "https://mcp.northwind.example/sentry"),
)

# Egress rules of the inference proxy: what may not leave the estate. One rule per class.
DLP_RULES = (
    ("secret", "deny", "Block secrets from leaving the estate"),
    ("credential", "deny", "Block credentials and keys"),
    ("pii", "deny", "Block personal data from model prompts"),
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
BUDGETS = (
    ("Northwind · monthly", 400_000_000, "monthly", "global", "alert"),
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
from redaccion import Redactor, abre, instala_excepthook  # noqa: E402
from demo_edition import BUSINESS_APPROVAL_ACTIONS, edition_of, keeps_action  # noqa: E402

# One redactor for the whole run. The operator token, the published demo password and every
# per-person token minted below are replaced in anything this process prints, including a
# traceback that reaches the interpreter's own handler.
_REDACT = Redactor()


def clip(text, limit):
    """`text` redacted, then cut: a secret the cut splits no longer equals the remembered value."""
    return _REDACT(text)[:limit]


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
                return exc.code, {"error": clip(raw, 300)}

    def events(self, path, timeout):
        """The `data:` payloads of one event-stream response, until the read goes quiet."""
        req = urllib.request.Request(self.base + path)
        req.add_header("Authorization", "Bearer " + self.token)
        req.add_header("X-Olivares-Tenant", self.tenant)
        with abre(req, timeout=timeout) as resp:
            for raw in resp:
                line = raw.decode("utf-8", "replace")
                if not line.startswith("data: "):
                    continue
                try:
                    yield json.loads(line[len("data: "):])
                except ValueError:
                    continue

    def ok(self, what, method, path, body=None, headers=None, token=None, accept=(200, 201, 202)):
        code, resp = self.call(method, path, body, headers, token)
        if code not in accept:
            raise SeedError("%s: HTTP %s %s" % (what, code, clip(json.dumps(resp), 300)))
        return resp

    def items(self, what, path):
        """Every item of a list route, following `has_more` and `cursor`."""
        out, cursor = [], None
        for _ in range(200):
            sep = "&" if "?" in path else "?"
            url = path + sep + "limit=100" + ("&cursor=" + urllib.parse.quote(cursor) if cursor else "")
            page = self.ok(what, "GET", url)
            if "items" not in page:
                raise SeedError("%s: the list answered without `items`: %s" % (what, clip(json.dumps(page), 200)))
            out.extend(page["items"] or [])
            cursor = page.get("cursor") or page.get("next_cursor")
            if page.get("has_more") and not cursor:
                raise SeedError("%s: the list says more follows but gives no cursor" % what)
            if not page.get("has_more"):
                return out
        raise SeedError("%s: the list never ends" % what)


def say(text):
    print("  " + text, flush=True)


def person_email(display):
    for name, email, _ in PEOPLE:
        if name == display:
            return email
    raise SeedError("no person named %s in the canonical estate" % display)


def person_token(api, email):
    code, resp = api.call("POST", "/v1/auth/login", {"email": email, "password": DEMO_PASSWORD}, anonymous=True)
    if code != 200 or not resp.get("token"):
        raise SeedError("login of %s: HTTP %s" % (email, code))
    _REDACT.recuerda(resp["token"])
    return resp["token"]


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
    by_kind = {p["kind"]: p["provider_ref"] for p in api.items("providers", "/v1/m/sessions/providers")
               if p["kind"] in {kind for kind, _ in PROVIDERS}}
    missing = [kind for kind, _ in PROVIDERS if kind not in by_kind]
    if missing:
        raise SeedError("providers: %s did not read back" % ", ".join(missing))
    ctx["providers"] = by_kind
    say("providers: %d registered (%s)" % (len(by_kind), ", ".join(sorted(by_kind))))


def refuse_link(path):
    if os.path.islink(path):
        raise SeedError("%s is a symbolic link; the work root must be a directory this script owns" % path)


def owned_path(root, *parts):
    """A path this script owns: no link at the leaf, and the resolved path stays under the work root.

    Checking the leaf alone is not enough: `repos` or `homes` itself can be a link to a directory this
    script does not own, and the writes below would land there. The realpath comparison closes every
    component at once."""
    raw = os.path.join(root, *parts)
    refuse_link(raw)
    path, base = os.path.realpath(raw), os.path.realpath(root)
    if os.path.commonpath([base, path]) != base:
        raise SeedError("%s resolves outside the work root %s; the work root must be a directory this "
                        "script owns" % (path, base))
    return path


def home_for(ctx, key, driver):
    """The profile's config and user homes, which the engine revalidates. Gemini is left as installed:
    no settings or credential file is written for it, so it has no sign-in and no session uses it."""
    base = owned_path(ctx["root"], "homes", key)
    config = os.path.join(base, ".gemini" if driver == "gemini-cli" else "config")
    user = os.path.join(base, "user")
    os.makedirs(config, exist_ok=True)
    os.makedirs(user, exist_ok=True)
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
    accounts = {a["account_ref"]: a for a in api.items("accounts", "/v1/m/sessions/provider-accounts")}
    tokens = {}
    for p in PROFILES:
        config, user = home_for(ctx, p["key"], p["driver"])
        body = {"driver": p["driver"], "config_home": config, "user_home": user,
                "display_name": p["display"], "auth_source": p["auth_source"]}
        if p["auth_source"] == "managed_injection":
            body["provider_record_ref"] = ctx["providers"][p["provider"]]
        code, created = api.call("POST", "/v1/m/sessions/provider-profiles", body)
        if code == 201:
            ref = created["profile_ref"]
        elif code == 409:
            existing = find_profile(api, p["driver"], config, user)
            if not existing:
                raise SeedError("profile %s: the homes are taken by a profile that cannot be read back" % p["display"])
            ref = existing["profile_ref"]
        else:
            raise SeedError("profile %s: HTTP %s %s" % (p["display"], code, clip(json.dumps(created), 300)))
        if p["account"] and (ref not in accounts or accounts[ref].get("display_name") != p["account_display"]):
            email = person_email(p["owner"])
            if email not in tokens:
                tokens[email] = person_token(api, email)
            if ref not in accounts:
                accounts[ref] = api.ok("account " + p["account"], "POST",
                                       "/v1/m/sessions/provider-accounts/%s/adopt" % ref,
                                       {"name": p["account"]}, token=tokens[email])
            if accounts[ref].get("display_name") != p["account_display"]:
                accounts[ref] = api.ok("account " + p["account"] + " label", "PATCH",
                                       "/v1/m/sessions/provider-accounts/%s" % ref,
                                       {"display_name": p["account_display"]}, token=tokens[email])
        profiles[p["key"]] = ref
    for p in PROFILES:
        readiness = api.ok("readiness " + p["key"], "GET",
                           "/v1/m/sessions/provider-profiles/%s/launch-readiness" % profiles[p["key"]])
        if p["signed_in"] and readiness.get("configuration_state") != "ready":
            raise SeedError("profile %s is not launch-ready: %s" % (p["key"], clip(json.dumps(readiness), 300)))
        if not p["signed_in"]:
            say("profile %s: installed, not signed in; no session uses it" % p["key"])
    # Read the two lists back: the launch used the ref the create answered, but the console and the
    # captures show the labels and auth sources the engine stored. A PATCH that changed nothing would
    # otherwise pass silently on the answer alone.
    final_profiles = {item["profile_ref"]: item
                      for item in api.items("profiles", "/v1/m/sessions/provider-profiles")}
    if set(final_profiles) != set(profiles.values()):
        extra = sorted(set(final_profiles) - set(profiles.values()))
        raise SeedError("profiles: the estate holds %d profiles, not the canonical %d%s"
                        % (len(final_profiles), len(PROFILES),
                           " (%s)" % ", ".join(extra) if extra else ""))
    final_accounts = {a["account_ref"]: a
                      for a in api.items("accounts", "/v1/m/sessions/provider-accounts")}
    wanted_accounts = {profiles[p["key"]]: p["account_display"] for p in PROFILES if p["account"]}
    if set(final_accounts) != set(wanted_accounts):
        raise SeedError("accounts: the estate holds %d accounts, not the canonical %d"
                        % (len(final_accounts), len(wanted_accounts)))
    wrong = []
    for p in PROFILES:
        item = final_profiles[profiles[p["key"]]]
        if item.get("display_name") != p["display"] or item.get("auth_source") != p["auth_source"]:
            wrong.append("%s reads back as %r (%s), expected %r (%s)"
                         % (p["key"], item.get("display_name"), item.get("auth_source"),
                            p["display"], p["auth_source"]))
        if p["account"] and final_accounts[profiles[p["key"]]].get("display_name") != p["account_display"]:
            wrong.append("account of %s reads back as %r, expected %r"
                         % (p["key"], final_accounts[profiles[p["key"]]].get("display_name"),
                            p["account_display"]))
    if wrong:
        raise SeedError("profiles: " + "; ".join(wrong))
    ctx["profiles"] = profiles
    say("profiles: %d launch-ready, each a named account" % sum(1 for p in PROFILES if p["signed_in"]))


# ------------------------------------------------------------------------------ folders and repositories
GIT_ENV = {"GIT_AUTHOR_NAME": "Northwind Labs", "GIT_AUTHOR_EMAIL": "dev@northwind.example",
           "GIT_COMMITTER_NAME": "Northwind Labs", "GIT_COMMITTER_EMAIL": "dev@northwind.example",
           "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_SYSTEM": os.devnull}


def git(repo, *args):
    # Only PATH and the identity: no token, and no inherited GIT_DIR that would point git at another repository.
    env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "LC_ALL": "C"}
    env.update(GIT_ENV)
    subprocess.run(["git", "-C", repo] + list(args), check=True, env=env, stdout=subprocess.DEVNULL,
                   stderr=subprocess.PIPE)


def make_repo(root, name, files):
    final = owned_path(root, "repos", name)
    if os.path.isdir(os.path.join(final, ".git")):
        return final
    # Built aside and renamed when complete, so an interrupted run never leaves a repository without history.
    repo = owned_path(root, "repos", name + ".partial")
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
    refs, roots = {}, {}
    for name, (folder, mode, dlp) in WORKSPACES.items():
        root = make_repo(ctx["root"], folder, REPOS[folder])
        roots[name] = root
        if root in existing:
            # A folder registered with another mode would render another badge and let a launch ask a
            # question the canonical estate never asks. Reuse only the canonical registration.
            wrong = [("%s is %r" % (field, existing[root].get(field)))
                     for field, want in (("mount_mode", mode), ("dlp_mode", dlp))
                     if existing[root].get(field) != want]
            if wrong:
                raise SeedError("folder %s is registered differently: %s; expected mount_mode %r and "
                                "dlp_mode %r" % (root, ", ".join(wrong), mode, dlp))
            refs[name] = existing[root]["workspace_ref"]
            continue
        created = api.ok("workspace " + name, "POST", "/v1/m/sessions/workspaces", {
            "name": name, "root_path": root, "mount_mode": mode, "container_target": "/workspace",
            "dlp_mode": dlp})
        refs[name] = created["workspace_ref"]
    final = api.items("workspaces", "/v1/m/sessions/workspaces")
    by_root = {w["root_path"]: w for w in final}
    missing = sorted(set(roots.values()) - set(by_root))
    extra = sorted(set(by_root) - set(roots.values()))
    if missing or extra or len(final) != len(WORKSPACES):
        raise SeedError("folders: the estate holds %d, the canonical estate %d%s%s"
                        % (len(final), len(WORKSPACES),
                           "; missing " + ", ".join(missing) if missing else "",
                           "; outside it " + ", ".join(extra) if extra else ""))
    for name, (folder, mode, dlp) in WORKSPACES.items():
        w = by_root[roots[name]]
        if w.get("mount_mode") != mode or w.get("dlp_mode") != dlp:
            raise SeedError("folder %s reads back as mount_mode %r and dlp_mode %r, expected %r and %r"
                            % (name, w.get("mount_mode"), w.get("dlp_mode"), mode, dlp))
        if w.get("workspace_ref") != refs[name]:
            raise SeedError("folder %s reads back with another ref than the one created" % name)
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


def step_demo_estate(api, ctx):
    """Refuse to create the public-password estate outside a disposable tenant.

    The loopback guard in `main` stops a remote URL, not a tunnel or reverse proxy to a real
    installation. `demo@olivares.local` exists only when the engine started with `--seed-demo` or
    when the capture harness created it through first-boot setup, so its presence in the roster is
    the operator's statement that this tenant is disposable; without it, this seeder would create
    six accounts whose password is published.
    """
    emails = {(m.get("email") or "").lower() for m in api.items("members", "/v1/members")}
    if DEMO_EMAIL not in emails:
        raise SeedError("this tenant has no %s account: neither `olivares serve --seed-demo` nor the "
                        "capture harness's first-boot setup created it, so the demo estate is not seeded here"
                        % DEMO_EMAIL)


def step_edition(api, ctx):
    """Read the edition once: a demo estate holds only what its edition does (scripts/lib/demo_edition.py)."""
    code, info = api.call("GET", "/v1/server-info")
    try:
        ctx["edition"] = edition_of(code, info)
    except ValueError as exc:
        raise SeedError("edition: %s" % exc)
    say("edition: %s" % ctx["edition"])


def step_policies(api, ctx):
    wanted = {name: spec for name, spec in APPROVAL_POLICIES
              if keeps_action(ctx.get("edition"), spec["match"]["action"])}
    have = {p["name"] for p in api.items("policies", "/v1/m/governance/policies")}
    for name, spec in wanted.items():
        if name not in have:
            api.ok("policy " + name, "POST", "/v1/m/governance/policies",
                   {"name": name, "kind": "approval", "enabled": True, "spec": spec})
    final = {p["name"]: p for p in api.items("policies", "/v1/m/governance/policies")}
    if set(final) != set(wanted):
        raise SeedError("policies: the estate holds %s, the canonical estate %s"
                        % (", ".join(sorted(final)) or "none", ", ".join(sorted(wanted)) or "none"))
    wrong = []
    for name, spec in wanted.items():
        policy = final[name]
        if policy.get("kind") != "approval" or not policy.get("enabled"):
            wrong.append("%s reads back as kind %r, enabled %r"
                         % (name, policy.get("kind"), policy.get("enabled")))
        for field in ("expires_in_seconds", "escalate_in_seconds"):
            if (policy.get("spec") or {}).get(field):
                wrong.append("%s carries %s" % (name, field))
    if wrong:
        raise SeedError("policies: " + "; ".join(wrong))
    say("policies: %d" % len(final))


# ------------------------------------------------------------------------------ people and approvals
def step_people(api, ctx):
    have = {m["email"].lower(): m for m in api.items("members", "/v1/members")}
    for display, email, role in PEOPLE:
        if email not in have:
            api.ok("member " + email, "POST", "/v1/users", {
                "email": email, "display_name": display, "password": DEMO_PASSWORD, "role": role,
                "tenant": api.tenant, "superadmin": False})
    # The roster is on the People screen: read it back and refuse a name or role the capture would
    # show beside the canonical one.
    final = {m["email"].lower(): m for m in api.items("members", "/v1/members")}
    extra = sorted(set(final) - {email for _, email, _ in PEOPLE} - {DEMO_EMAIL})
    if extra:
        raise SeedError("people: the estate holds members outside the canonical estate: %s"
                        % ", ".join(extra))
    wrong = []
    for display, email, role in PEOPLE:
        member = final.get(email)
        if member is None:
            wrong.append("%s is missing" % email)
        elif member.get("display_name") != display or member.get("role") != role:
            wrong.append("%s reads back as %r (%s), expected %r (%s)"
                         % (email, member.get("display_name"), member.get("role"), display, role))
    if wrong:
        raise SeedError("people: " + "; ".join(wrong))
    say("people: %d members" % len(final))


def approval_status(a):
    return a.get("effective_status") or a.get("status") or a.get("state")


def step_approvals(api, ctx):
    edition = ctx.get("edition")
    queue = api.items("approvals", "/v1/m/governance/approvals")
    if any(a.get("subject_ref") is None for a in queue):
        raise SeedError("approvals: the list does not carry subject_ref, so a request cannot be told apart "
                        "from one already seeded")
    canonical = {(action, subject) for action, _, subject, _, _, _, _ in PENDING_APPROVALS}
    canonical |= {(action, subject) for action, _, subject, _, _, _ in DECIDED_APPROVALS}
    have = {}
    for a in queue:
        key = (a.get("action"), a.get("subject_ref"))
        if key not in canonical:
            if not keeps_action(edition, key[0]):
                continue  # a Business request is not this Community estate's
            raise SeedError("approval %s %s is not part of the canonical estate: start from a fresh estate" % key)
        have[key] = a
    tokens = {}

    def token_for(email):
        if email not in tokens:
            tokens[email] = person_token(api, email)
        return tokens[email]

    def session_ref_for(name):
        """The provider session id the engine recorded for the run that asked: the approval's link to it."""
        ref = (ctx.get("session_refs") or {}).get(name)
        if not ref:
            raise SeedError("approval of session %r: the seeder captured no provider session id, so the "
                            "request could not name the session that asked" % name)
        return ref

    for action, kind, subject, reason, session, who, required in PENDING_APPROVALS:
        if not keeps_action(edition, action) or (action, subject) in have:
            continue
        api.ok("approval " + subject, "POST", "/v1/m/governance/approvals", {
            "action": action, "subject_kind": kind, "subject_ref": subject, "reason": reason,
            "session_ref": session_ref_for(session), "required_approvals": required},
            token=token_for(who))
        have[(action, subject)] = {"action": action, "subject_ref": subject}
    reviewer = next(p for p in PEOPLE if p[2] == "admin")
    reviewer_token = None
    for action, kind, subject, reason, who, decision in DECIDED_APPROVALS:
        if not keeps_action(edition, action):
            continue
        existing = have.get((action, subject))
        if existing is None:
            existing = api.ok("approval " + subject, "POST", "/v1/m/governance/approvals", {
                "action": action, "subject_kind": kind, "subject_ref": subject, "reason": reason,
                "required_approvals": 1}, token=token_for(who))
            have[(action, subject)] = existing
            pending = True
        else:
            pending = approval_status(existing) == "pending"
        # A request created but not yet decided (an interrupted run) is decided now, not skipped.
        if not pending:
            continue
        if reviewer_token is None:
            reviewer_token = person_token(api, reviewer[1])
        note = ("Reviewed the change and the run behind it; go ahead." if decision == "approve" else
                "The change is not ready for this branch; keep it in review.")
        api.ok("decision on " + subject, "POST", "/v1/m/governance/approvals/%s/decisions" % existing["id"],
               {"decision": decision, "note": note}, token=reviewer_token)
    final = api.items("approvals", "/v1/m/governance/approvals")
    if edition == "community":
        leaked = sorted({a.get("action") for a in final if a.get("action") in BUSINESS_APPROVAL_ACTIONS})
        if leaked:
            raise SeedError("approvals: a Community estate holds Business request kinds: %s" % ", ".join(leaked))
    states = {}
    for a in final:
        states[approval_status(a)] = states.get(approval_status(a), 0) + 1
    say("approvals: %d requests, states %s" % (len(final), states))


def step_verify_approvals(api, ctx):
    """Read the queue back once every step has run and require the canonical state.

    The queue can exist and still be wrong: a pending request someone denied, or an approval where
    the estate says a denial. With only the counts printed, the seeder (and the capture harness,
    which does not run the live snapshot) accepted the wrong estate.
    """
    edition = ctx.get("edition")
    final = api.items("approvals", "/v1/m/governance/approvals")
    wanted, waiting = {}, {}
    for action, _, subject, _, session, _, _ in PENDING_APPROVALS:
        if keeps_action(edition, action):
            wanted[(action, subject)] = "pending"
            waiting[(action, subject)] = session
    for action, _, subject, _, _, decision in DECIDED_APPROVALS:
        if keeps_action(edition, action):
            wanted[(action, subject)] = "approved" if decision == "approve" else "rejected"
    actual = {(a.get("action"), a.get("subject_ref")): a for a in final}
    wrong = ["%s %s is %s, expected %s"
             % (action, subject, approval_status(actual.get((action, subject)))
                if (action, subject) in actual else "missing", state)
             for (action, subject), state in wanted.items()
             if (action, subject) not in actual or approval_status(actual[(action, subject)]) != state]
    for key, session in waiting.items():
        row = actual.get(key)
        want_ref = (ctx.get("session_refs") or {}).get(session)
        if row is None or not want_ref or row.get("session_ref") != want_ref:
            wrong.append("%s %s names session %r, expected the run's provider id %r"
                         % (key[0], key[1], (row or {}).get("session_ref"), want_ref))
    if len(final) != len(wanted):
        wrong.append("%d requests in the queue, expected %d" % (len(final), len(wanted)))
    if wrong:
        raise SeedError("approvals: the queue is not the canonical estate: " + "; ".join(wrong))
    say("approvals: the canonical queue reads back (%d waiting, %d decided)"
        % (sum(1 for state in wanted.values() if state == "pending"),
           sum(1 for state in wanted.values() if state != "pending")))


# ------------------------------------------------------------------------------ skills and MCP servers
def step_skills(api, ctx):
    packs = api.items("skills", "/v1/m/skills/packs")
    if not any(p["name"] == "Platform team skills" for p in packs):
        api.ok("skills pack", "POST", "/v1/m/skills/packs", {
            "name": "Platform team skills",
            "source": {"kind": "workspace", "workspace_ref": ctx["workspaces"]["payments-api"],
                       "directory": ".agents/skills"}})
    say("skills: %d pack(s)" % len(api.items("skills", "/v1/m/skills/packs")))


def mcp_servers(gateway):
    """The gateway's server list, or an error: an answer without it is not an empty gateway."""
    servers = gateway.get("servers")
    if not isinstance(servers, list):
        raise SeedError("mcp: the gateway answer carries no servers list")
    return servers


def step_mcp(api, ctx):
    gateway = api.ok("mcp gateway", "GET", "/v1/console/mcp-gateway")
    have = {s.get("name") for s in mcp_servers(gateway)}
    version = gateway.get("version", 0)
    for name, url in MCP_SERVERS:
        if name in have:
            continue
        resp = api.ok("mcp " + name, "POST", "/v1/console/mcp-gateway/servers", {
            "version": version,
            "server": {"name": name, "transport": "streamable_http", "url": url, "enabled": False,
                       "trust": {"issuer": "https://auth.northwind.example", "resource": url}}})
        version = resp.get("version", version + 1)
    servers = mcp_servers(api.ok("mcp", "GET", "/v1/console/mcp-gateway"))
    missing = [name for name, _ in MCP_SERVERS if name not in {s.get("name") for s in servers}]
    if missing:
        raise SeedError("mcp: %s missing from the gateway" % ", ".join(missing))
    say("mcp: %d server(s) configured" % len(servers))


# ------------------------------------------------------------------------------ recorded conversations
def run_detail(api, ref):
    """What the run itself says about the child, for an error that would otherwise only speculate."""
    code, run = api.call("GET", "/v1/m/sessions/runs/" + ref)
    if code != 200:
        return " (the run read answered HTTP %s: %s)" % (code, clip(json.dumps(run), 200))
    return " (state %s%s%s)" % (
        run.get("state"), ", exit %s" % run.get("exit_code") if run.get("exit_code") is not None else "",
        ", " + str(run["reason"]) if run.get("reason") else "")


def carries(value, marker):
    """True when any string inside a decoded JSON value holds `marker`."""
    if isinstance(value, str):
        return marker in value
    if isinstance(value, dict):
        return any(carries(item, marker) for item in value.values())
    if isinstance(value, list):
        return any(carries(item, marker) for item in value)
    return False


def attach_marker(api, ref, marker, limit=30.0):
    """Wait until the run's bridged output carries `marker`, the last line the recorded turn writes.

    The ring is the engine's own record of what the child wrote (`runtime_bridge.go:66`) and attach
    replays it from the first frame (`runtime_api.go:839`). It is the proof a turn needs: the child
    consumed the prompt only if it wrote the recorded answer, which is a stronger claim than "some
    counter moved". The activity stamp cannot carry it: a turn whose usage report adds no delta
    credits nothing (`runtime_usage.go:creditUsage`), and the engine writes frame activity at most
    every ten seconds (`activityWriteInterval`, `runtime_bridge.go:24`), so a prompt the child read
    inside that window leaves the stamp where it was. Every attempt reopens the stream from
    `from=0`, so a timeout between frames costs nothing and the read stays bounded by `limit`."""
    deadline = time.time() + limit
    while True:
        ended = False
        try:
            for event in api.events("/v1/m/sessions/runs/%s/attach?from=0" % ref, timeout=2):
                line = event.get("line")
                try:
                    frame = json.loads(line) if isinstance(line, str) else event
                except ValueError:
                    frame = line
                if carries(frame, marker):
                    return
                if event.get("type") in ("end", "notice"):
                    ended = True
                    break
        except urllib.error.HTTPError as exc:
            raise SeedError("run %s: the engine refused the output stream with HTTP %s%s"
                            % (ref, exc.code, run_detail(api, ref)))
        except TimeoutError:
            pass  # the two-second read went quiet between frames; the ring still holds what the child wrote
        except urllib.error.URLError as exc:
            # A refused connection is not a quiet read: the stream never opened, and waiting for the
            # marker cannot succeed. Name the cause instead of reporting the turn as never consumed.
            raise SeedError("run %s: the engine did not accept the output stream: %s%s"
                            % (ref, exc.reason, run_detail(api, ref))) from exc
        except OSError as exc:
            raise SeedError("run %s: the output stream failed: %s%s"
                            % (ref, exc, run_detail(api, ref))) from exc
        if ended:
            raise SeedError("run %s: its output stream ended before it wrote %r; the turn was never "
                            "consumed%s" % (ref, marker[:80], run_detail(api, ref)))
        if time.time() >= deadline:
            raise SeedError("run %s: the child did not write %r within %d seconds of the input; the turn "
                            "was never consumed%s" % (ref, marker[:80], limit, run_detail(api, ref)))
        time.sleep(0.2)


def settle(api, ref, name, final_turn, limit=30.0):
    """Wait until a turn that reports a result has ended, so the next prompt is accepted.

    The answer marker proves what the child wrote; the result frame that ends the turn is a
    separate write, and an input posted in between is refused with 409 (measured on the builder).
    A run the child ended early is named here, not left to the next input's refusal."""
    deadline = time.time() + limit
    while True:
        run = api.ok("run", "GET", "/v1/m/sessions/runs/" + ref)
        state = run.get("state")
        if state == "waiting_approval":
            raise SeedError("session %s waits for a launch approval; the canonical folders are read-write "
                            "with DLP off, so a launch never asks" % name)
        if state in ("idle", "stopped"):
            if state == "stopped" and not final_turn:
                raise SeedError("session %s stopped after a turn that was not its last" % name)
            return run
        if time.time() > deadline:
            raise SeedError("session %s: the run was still %r after its turn answered" % (name, state))
        time.sleep(0.15)


def turn_texts(turn):
    """Every string the recorded turn makes the child write, one per step.

    The stand-ins emit a step's `say` text or its tool command's output on every transport
    (`demo-tool.py`, `claude`/`codex`/`acp`), so either is a marker the attach ring can carry."""
    return [step["say"] if "say" in step else step["tool"]["output"] for step in turn["steps"]]


def turn_marker(turn, written):
    """The last line this turn writes, when no earlier turn of the same session wrote it.

    Attach replays the run from its first frame, so the marker must be one only this turn writes.
    The last step also says the whole turn was answered, not just started, before the next prompt
    goes in."""
    text = turn_texts(turn)[-1]
    if text and text not in written:
        return text
    raise SeedError("turn %r ends with a line an earlier turn of its session already wrote; the "
                    "attach proof cannot tell them apart" % turn["prompt"])


def dataset_sessions():
    with open(os.path.join(HERE, "demo-tools", "conversations.json"), encoding="utf8") as fh:
        return [s for s in json.load(fh)["sessions"] if not s.get("fixture")]


# The Northwind estate's figures, carried by the recorded sessions: seven live and nine finished, $6.84 today, and the
# OpenCode session reports no cost, so it is the one unpriced session.
CANONICAL_TODAY_MICRO_USD = 6_840_000
CANONICAL_LIVE = 7
CANONICAL_FINISHED = 9


def step_sessions(api, ctx):
    sessions = dataset_sessions()
    expected = {s["name"]: s for s in sessions}
    profiles = {p["key"]: p for p in PROFILES}
    have = {r["name"]: r for r in api.items("runs", "/v1/m/sessions/runs") if r.get("name") in expected}
    for name, row in have.items():
        s = expected[name]
        state = row.get("state")
        if s.get("ending") in ("hold", "linger"):
            # A held turn never reported a result, so it is running; a lingering one ended its turn
            # and waits for the next input, so it is idle. Both stay live, but they are not the same
            # state, and the capture shows the difference.
            want = "running" if s["ending"] == "hold" else "idle"
            if state != want:
                raise SeedError("session %s exists and is %s; the canonical estate leaves it %s: start from a "
                                "fresh estate" % (name, state, want))
        elif state != "stopped" or row.get("exit_code") != 0:
            raise SeedError("session %s exists and is %s (exit %s), not stopped cleanly: start from a fresh estate"
                            % (name, state, row.get("exit_code")))
        if not row.get("run_ref"):
            raise SeedError("session %s: the run list does not carry run_ref" % name)
        if row.get("provider_profile_ref") != ctx["profiles"][s["profile"]]:
            raise SeedError("session %s reads back on profile %r, expected %r"
                            % (name, row.get("provider_profile_ref"), ctx["profiles"][s["profile"]]))
        if row.get("workspace_ref") != ctx["workspaces"][s["folder"]]:
            raise SeedError("session %s reads back in folder %r, expected %r"
                            % (name, row.get("workspace_ref"), ctx["workspaces"][s["folder"]]))
        provider_sid = row.get("claude_session_id") or api.ok(
            "run", "GET", "/v1/m/sessions/runs/" + row["run_ref"]).get("claude_session_id") or ""
        if not provider_sid:
            raise SeedError("session %s: the run carries no provider session id, so a request cannot name "
                            "the session that asked" % name)
        ctx.setdefault("session_refs", {})[name] = provider_sid
    tokens = {}

    def token_for(display):
        email = person_email(display)
        if email not in tokens:
            tokens[email] = person_token(api, email)
        return tokens[email]

    started = 0
    for s in sessions:
        if s["name"] in have:
            continue
        profile = profiles[s["profile"]]
        token = token_for(profile["owner"])
        body = {"name": s["name"], "transport": "stream-json", "permission_mode": "default", "effort": "medium",
                "model": s["model"], "workspace_ref": ctx["workspaces"][s["folder"]], "isolation": "native",
                # The stand-in reads this one name: without it a Claude session would announce the id of
                # the engine's observed demo session and be joined to it instead of standing alone.
                "env_allow": ["DEMO_SESSION_UNIQUE"], "provider_profile_ref": ctx["profiles"][s["profile"]]}
        if s.get("template"):
            # A template that lists allowed tools runs under dontAsk, so the launch asks for no less.
            body["template_id"] = ctx["templates"][s["template"]]
            body["permission_mode"] = "dontAsk"
        ref = api.ok("session " + s["name"], "POST", "/v1/m/sessions/runs", body, token=token)["run_ref"]
        run = api.ok("run", "GET", "/v1/m/sessions/runs/" + ref)
        if run.get("state") == "waiting_approval":
            raise SeedError("session %s waits for a launch approval; the canonical folders are read-write with "
                            "DLP off, so a launch never asks" % s["name"])
        written = []
        for i, turn in enumerate(s["turns"]):
            # The recorded answer in the run's own output stream is what proves the child consumed
            # this prompt; the activity stamp does not move on every turn (see attach_marker).
            marker = turn_marker(turn, written)
            api.ok("session %s: input" % s["name"], "POST", "/v1/m/sessions/runs/%s/input" % ref,
                   {"text": turn["prompt"]}, token=token)
            attach_marker(api, ref, marker)
            if not turn.get("hold"):
                settle(api, ref, s["name"], i == len(s["turns"]) - 1)
            written.extend(turn_texts(turn))
        if s.get("ending") in ("hold", "linger"):
            # A held turn never reported a result, so the plane still sees work in flight; a lingering
            # one ended its turn and waits for the next input. Both stay live, but they are not the same
            # state, and the capture shows it. The last answer can be on screen before the result frame
            # that ends the turn lands, so the state gets a bounded moment to settle.
            want_state = "running" if s["ending"] == "hold" else "idle"
            deadline = time.time() + 30
            while True:
                final = api.ok("run", "GET", "/v1/m/sessions/runs/" + ref)
                if final.get("state") == want_state:
                    break
                if final.get("state") == "stopped" or time.time() > deadline:
                    raise SeedError("session %s ends as %r but the canonical estate has it %s waiting %s"
                                    % (s["name"], final.get("state"), want_state,
                                       "on an open turn" if want_state == "running" else "for its next input"))
                time.sleep(0.3)
        else:
            deadline = time.time() + 30
            while True:
                final = api.ok("run", "GET", "/v1/m/sessions/runs/" + ref)
                if final.get("state") == "waiting_approval":
                    raise SeedError("session %s waits for a launch approval; the canonical folders are read-write "
                                    "with DLP off, so a launch never asks" % s["name"])
                if final.get("state") not in ("running", "idle", "pending"):
                    break
                if time.time() > deadline:
                    raise SeedError("session %s did not end within 30 seconds after its last turn" % s["name"])
                time.sleep(0.3)
            if final.get("state") != "stopped" or final.get("exit_code") != 0:
                raise SeedError("session %s ended as %s (exit %s): %s" % (
                    s["name"], final.get("state"), final.get("exit_code"), final.get("reason")))
        provider_sid = final.get("claude_session_id") or ""
        if not provider_sid:
            raise SeedError("session %s: the run carries no provider session id, so a request cannot name "
                            "the session that asked" % s["name"])
        ctx.setdefault("session_refs", {})[s["name"]] = provider_sid
        started += 1
    returned = [r for r in api.items("runs", "/v1/m/sessions/runs") if r.get("name") in expected]
    if len(returned) != len(expected):
        raise SeedError("sessions: the run list carries %d canonical names, expected %d"
                        % (len(returned), len(expected)))
    rows = {r["name"]: r for r in returned}
    missing = sorted(set(expected) - set(rows))
    if missing:
        raise SeedError("sessions: %s missing from the run list" % ", ".join(missing))
    for name, s in expected.items():
        want = s.get("cost_micro_usd")
        got = rows[name].get("cost_micro_usd")
        if got != want:
            raise SeedError("session %s reports cost %s, the canonical estate says %s" % (name, got, want))
        if rows[name].get("provider_profile_ref") != ctx["profiles"][s["profile"]]:
            raise SeedError("session %s reads back on profile %r, expected %r"
                            % (name, rows[name].get("provider_profile_ref"), ctx["profiles"][s["profile"]]))
        if rows[name].get("workspace_ref") != ctx["workspaces"][s["folder"]]:
            raise SeedError("session %s reads back in folder %r, expected %r"
                            % (name, rows[name].get("workspace_ref"), ctx["workspaces"][s["folder"]]))
    # Sum what the engine returned, not the table the seeder just read: the two agree today, and the
    # day they stop agreeing is the day the captures would show a figure nobody seeded.
    total = sum(r.get("cost_micro_usd") for r in rows.values() if r.get("cost_micro_usd") is not None)
    unpriced = sum(1 for r in rows.values() if r.get("cost_micro_usd") is None)
    live = sum(1 for r in rows.values() if r.get("state") in ("running", "idle"))
    finished = len(rows) - live
    if (total, unpriced, live, finished) != (CANONICAL_TODAY_MICRO_USD, 1, CANONICAL_LIVE, CANONICAL_FINISHED):
        raise SeedError("sessions: %d finished, %d live, $%.2f today, %d unpriced; the canonical estate is "
                        "9 finished, 7 live, $6.84 today, 1 unpriced" % (finished, live, total / 1e6, unpriced))
    # The home merges every observed row with the runs: a row the plane did not launch itself is
    # counted beside these sixteen and moves the captured figures.
    run_refs = {r.get("run_ref") for r in rows.values()}
    observed = api.items("live", "/v1/m/sessions/live")
    outside = [r for r in observed if r.get("attribution") != "managed" or r.get("run_ref") not in run_refs]
    if outside:
        raise SeedError("sessions: %d observed row(s) outside the canonical estate (%s); the home would "
                        "count them: start from a fresh estate"
                        % (len(outside), ", ".join(sorted({r.get("live_ref") or r.get("session_ref") or "-"
                                                           for r in outside})[:5])))
    # The three requests below name the session that asked by its provider id. The home (and the approval
    # page) can only follow that name when the live list holds the run's own managed row under it; a
    # request pointing at a row the plane did not launch would open onto nothing.
    by_sid = {r.get("session_ref"): r for r in observed}
    for action, _, subject, _, session, _, _ in PENDING_APPROVALS:
        if not keeps_action(ctx.get("edition"), action):
            continue
        sid = (ctx.get("session_refs") or {}).get(session)
        row = by_sid.get(sid)
        if row is None or row.get("run_ref") != rows[session].get("run_ref"):
            raise SeedError("approval %s %s names session %r, which is not the managed live row of its run"
                            % (action, subject, sid))
    say("sessions: %d finished, %d live (%d new), $%.2f today, %d unpriced"
        % (finished, live, started, total / 1e6, unpriced))


# ------------------------------------------------------------------------------ rules and channels
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
            raise SeedError("channel %s: HTTP %s %s" % (slug, code, clip(json.dumps(resp), 300)))
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
def step_budgets(api, ctx):
    code, resp = api.call("GET", "/v1/m/finops/budgets")
    if code == 404 and (resp.get("error") or {}).get("code") == "module_not_enabled":
        say("budgets: the finops module is not enabled on this engine; nothing seeded")
        return
    if code != 200:
        raise SeedError("budgets: HTTP %s %s" % (code, clip(json.dumps(resp), 300)))
    have = {b["name"]: b for b in resp.get("items") or []}
    for name, limit, period, dimension, action in BUDGETS:
        existing = have.get(name)
        if existing is not None:
            # A budget with the canonical name and another limit would leave the capture comparing
            # against a figure nobody wrote. Read the fields back; do not trust the name alone.
            wrong = [(field, existing.get(field), want) for field, want in (
                ("limit_micro_usd", limit), ("period", period), ("dimension", dimension), ("action", action))
                if existing.get(field) != want]
            if wrong:
                raise SeedError("budget %s is not the canonical one: %s" % (
                    name, ", ".join("%s is %r, expected %r" % item for item in wrong)))
            continue
        code, made = api.call("POST", "/v1/m/finops/budgets", {
            "name": name, "limit_micro_usd": limit, "period": period, "dimension": dimension,
            "action": action, "enabled": True, "thresholds": [0.5, 0.8, 1.0]})
        # Only Community lacks FinOps; a paid edition's 501 is a regression and stays an error.
        if code == 501 and ctx.get("edition") == "community" and "Business feature" in json.dumps(made):
            say("budgets: FinOps is a Business capability on this build; nothing seeded")
            return
        if code not in (200, 201):
            raise SeedError("budget %s: HTTP %s %s" % (name, code, clip(json.dumps(made), 300)))
    say("budgets: %d" % len(api.items("budgets", "/v1/m/finops/budgets")))


# ------------------------------------------------------------------------------ after adoption
LIVE_STATES = ("active", "idle", "silent_evasion")


def parse_moment(text):
    """An RFC 3339 timestamp as an aware datetime, or None when it cannot be read."""
    if not isinstance(text, str) or not text:
        return None
    try:
        at = datetime.datetime.fromisoformat(text.replace("Z", "+00:00"))
    except ValueError:
        return None
    return at if at.tzinfo is not None else None


def verify_live_plane(api, midnight):
    """Refuse any observed row the home would count beside the canonical sixteen.

    `step_sessions` makes this check before adoption: every observed row is then a row of a
    canonical run. The adoption seed adds rows with no run behind them afterwards, and the home
    counts those on their own. Their timestamps derive from an anchor at UTC midnight, so a row can
    land on today's date and move the today figure, or still read as live and move the live tile.
    A row whose timestamp cannot be read is refused: the capture cannot prove it is not counted.
    """
    run_refs = {r.get("run_ref") for r in api.items("runs", "/v1/m/sessions/runs")}
    observed = api.items("live", "/v1/m/sessions/live")
    outside = [r for r in observed
               if r.get("attribution") != "managed" or r.get("run_ref") not in run_refs]
    moving = []
    for row in outside:
        ref = row.get("live_ref") or row.get("session_ref") or "-"
        state = row.get("cc_state")
        if state in LIVE_STATES:
            moving.append("%s reads as %s" % (ref, state))
            continue
        at = parse_moment(row.get("last_event_at"))
        if at is None:
            moving.append("%s has an unreadable last_event_at %r" % (ref, row.get("last_event_at")))
        elif at >= midnight:
            moving.append("%s last active at %s, at or after the local midnight %s"
                          % (ref, at.isoformat(), midnight.isoformat()))
    if moving:
        raise SeedError("live plane: %d observed row(s) without a canonical run would be counted by the "
                        "home: %s" % (len(moving), "; ".join(moving[:5])))
    say("live plane: %d observed row(s) outside the canonical runs, all ended before local midnight"
        % len(outside))


STEPS = (("edition", step_edition), ("estate", step_demo_estate), ("modules", step_modules),
         ("providers", step_providers),
         ("people", step_people), ("profiles", step_profiles), ("workspaces", step_workspaces),
         ("templates", step_templates), ("skills", step_skills), ("mcp", step_mcp),
         ("policies", step_policies), ("communications", step_communications), ("dlp", step_dlp),
         ("sessions", step_sessions), ("approvals", step_approvals), ("budgets", step_budgets),
         ("verify-approvals", step_verify_approvals))


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("base_url")
    parser.add_argument("token", help="must be -: the bearer token is read from OLIVARES_TOKEN")
    parser.add_argument("tenant")
    parser.add_argument("work_root", help="disposable directory for the repositories and tool homes")
    parser.add_argument("--no-communications", action="store_true",
                        help="leave the channels and messages out, for an engine whose directory writer "
                             "is not activated; the capture harness always activates it")
    parser.add_argument("--verify-live-plane", action="store_true",
                        help="read the live plane back after the adoption seed and refuse any observed "
                             "row the home would count beside the canonical estate; makes no writes")
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
    ctx = {"root": os.path.realpath(args.work_root), "no_communications": args.no_communications}
    instala_excepthook(_REDACT, LABEL)
    _REDACT.recuerda(token, DEMO_PASSWORD)
    try:
        if args.verify_live_plane:
            midnight = datetime.datetime.now().astimezone().replace(
                hour=0, minute=0, second=0, microsecond=0)
            verify_live_plane(api, midnight)
        else:
            for _, fn in STEPS:
                fn(api, ctx)
    except SeedError as exc:
        print(_REDACT("%s: ERROR: %s" % (LABEL, exc)), file=sys.stderr)
        return 1
    except (urllib.error.URLError, OSError) as exc:
        print(_REDACT("%s: ERROR: the engine at %s did not answer: %s" % (LABEL, args.base_url, exc)),
              file=sys.stderr)
        return 1
    except subprocess.CalledProcessError as exc:
        print(_REDACT("%s: ERROR: git failed: %s" % (LABEL, clip((exc.stderr or b"").decode(errors="replace"), 300))),
              file=sys.stderr)
        return 1
    print("%s: done" % LABEL)
    return 0


if __name__ == "__main__":
    sys.exit(main())
