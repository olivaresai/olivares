---
title: Quickstart
description: From zero to a populated read/write access graph with a real
  Permitted-vs-Observed drift result in about five minutes — first on the
  bundled demo estate, then on a real pgAudit connector to prove it is not a
  demo.
slug: 2026-06/start/quickstart
---

This is the fast path to seeing what Olivares AI is *for*: a **read/write access
map** of your estate and the **Permitted-vs-Observed drift** on top of it — the gap
between the access an agent is *granted* and the access it is *observed* using.

You will reach that result twice, in about five minutes total:

1. **In one minute, on the bundled demo estate** — the instant "what does it even
   look like" on-ramp (synthetic observations, flowing through the real engine).
2. **Then on a real connector** — the same graph and drift, this time parsed verbatim
   from a PostgreSQL **pgAudit** log, to prove the hero runs on genuine data, not a demo.

Every command below is run, exactly as written, by `scripts/quickstart-smoke.sh`
([reproducibility](#5-reproduce-this-yourself)) — so this page cannot quietly drift
from the binary.

It is a learning path, not a production deployment. For the real install (no default
credentials, a one-time setup token, TLS), go to [self-hosting](/2026-06/how-to/self-hosting/).
For a guided UI walkthrough, see the [zero-to-graph tutorial](/2026-06/tutorials/zero-to-graph/).

:::caution[Demo mode is for learning only]
`--seed-demo` provisions a demo administrator with a **public, source-tree password**
and synthetic data, and it **refuses to start on a non-loopback address**. Never use
it for a real install — the genuine first-run path is step 3 below and in
[self-hosting](/2026-06/how-to/self-hosting/).
:::

## 1. Build the single binary

From a checkout of the repository (needs Go 1.26+; the store is pure-Go SQLite, so no
C toolchain):

```bash
task build                      # compiles ./bin/olivares with the web UI embedded
./bin/olivares version
```

`task build` produces one self-contained artifact at `./bin/olivares` — the
engine, the embedded web UI and the first-party connector plugins. The **container and
Kubernetes installs wrap this same binary**: a published image plus a Compose file
([self-hosting](/2026-06/how-to/self-hosting/)), or a flat manifest you `kubectl apply -f
deploy/manifests/install.yaml` (no Helm required). The hero you see below is identical
on all three — only the demo seed differs (loopback-only, never in a real install).

## 2. Boot the demo estate (loopback only)

```bash
DATA="$(mktemp -d)"
./bin/olivares serve --insecure --seed-demo \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$DATA"
```

`--insecure` serves plaintext HTTP on loopback (fine for a local demo; **TLS is on by
default** otherwise). You will see honest `WARN` lines for the seams that are
deny-closed out of the box (no judge, no embedder, no approval gate, no real sources),
then a **DEMO MODE** banner with the credentials:

```text
demo@olivares.local / olivares-demo-estate
```

The synthetic estate flows through the **real** event bus exactly as a live pgAudit or
OpenTelemetry collector would — only the observations are seeded.

## 3. Reach the access graph and its drift (the hero)

Leave the server running; in a second terminal, log in, resolve the demo tenant, and
fetch the graph and its drift:

```bash
BASE=http://127.0.0.1:8901
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

TENANT="$(curl -sf "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;[print(o["tenant_id"]) for o in json.load(sys.stdin)["items"] if o["slug"]=="demo"]')"

# The read/write access map — module III:
curl -sf "$BASE/v1/m/accessmap/graph?limit=200" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool

# The Permitted-vs-Observed drift — the killer feature:
curl -sf "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

The demo estate returns exactly **20 nodes and 13 edges**, and the drift surfaces
**8 unexpected accesses** and **2 unused grants**. Every edge carries the product's
honesty axes, so you can read each finding without guessing:

* **`mode`** — `read` / `write` / `readwrite` / `unknown`: the R/W classification, taken
  verbatim from the signal, never inferred.
* **`attribution_tier`** — `firm` / `approximate` / `unknown`: how firmly the access is
  tied to a *specific* agent or workload identity. In the demo, **6 edges are firm and
  7 approximate** — e.g. an agent reading a resource it was never granted
  (`appdb.public.secrets`, *firm*) versus a shared-pool identity writing logs
  (`appdb.public.logs`, honestly *approximate*).
* **`coverage_tier`** — `clean` / `lossy` / `opaque` / `mixed`: the fidelity of the
  *resource's* signal, orthogonal to attribution.

:::tip[This is the differentiator]
The **diff between Permitted and Observed** is *least-privilege drift* — the thing you
want to find before an auditor or an attacker does. The seed proves it is real, not
"everything is drift": the 3 granted **and** observed edges reconcile and drop out of
the drift result; only genuine gaps remain (8 unexpected accesses + 2 grants that are
declared but never exercised). And the product never fabricates a label it cannot
prove — attribution that is merely `approximate` says so, instead of inventing a `firm`
agent.
:::

The same graph renders in the embedded web UI at `http://127.0.0.1:8901` (log in with
the demo credentials and switch to the **Demo Estate** organization).

Stop the demo server (`Ctrl-C`) before the next step.

## 4. Prove it on a real connector (not a demo)

The hero is not seeded magic: it runs on whatever your sources observe. Here you wire
the **real pgAudit connector** — the same code path a production install uses — against
a PostgreSQL audit log, with **no demo seed**.

First, a small `pgAudit` csvlog (three real audit lines: two reads and a write by one
application). In production pgAudit writes these to the Postgres log; here a file stands
in for that tail:

```bash
WORK="$(mktemp -d)"
python3 - "$WORK/postgresql.csv" <<'PY'
import csv, sys
def row(ts, user, db, msg, app):
    r = [''] * 26
    r[0], r[1], r[2] = ts, user, db
    r[11] = 'LOG'; r[13] = msg; r[22] = app; r[23] = 'client backend'
    return r
rows = [
    row("2026-06-09 09:00:01.001 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,1,1,READ,SELECT,TABLE,public.customers", "billing-agent"),
    row("2026-06-09 09:00:02.002 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,2,1,WRITE,INSERT,TABLE,public.orders", "billing-agent"),
    row("2026-06-09 09:00:03.003 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,3,1,READ,SELECT,TABLE,public.secrets", "billing-agent"),
]
with open(sys.argv[1], 'w', newline='') as f:
    csv.writer(f).writerows(rows)
PY
```

Now do a **real first-run**: boot once with no default credentials, claim the one-time
setup token, and create a tenant to attach the connector to.

```bash
BASE=http://127.0.0.1:8901
./bin/olivares serve --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$WORK/data" > "$WORK/server.log" 2>&1 &
SERVER=$!
sleep 2

# The one-time setup token is printed to stdout on first boot (look for `olst_…` on the
# server's console, or read it from the redirected log):
SETUP="$(grep -oE 'olst_[A-Z0-9]+' "$WORK/server.log" | head -1)"

curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$SETUP\",\"email\":\"admin@local\",\"password\":\"correct-horse-battery-staple\"}"

TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

TENANT="$(curl -sf -X POST "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"Production","slug":"prod"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')"
echo "tenant: $TENANT"

kill "$SERVER"                  # stop the first-run server; we restart it with pgAudit wired
```

Connectors are wired from one operator config file, by value, never persisted by the
engine. Point pgAudit at the log for your tenant and **restart** with the config:

```bash
cat > "$WORK/sources.json" <<JSON
{"sources":[{"name":"salesdb-pgaudit","kind":"pgaudit","tenant":"$TENANT",
  "config":{"log_path":"$WORK/postgresql.csv","format":"csvlog"}}]}
JSON

OLIVARES_SOURCES_CONFIG="$WORK/sources.json" ./bin/olivares serve --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$WORK/data"
```

The boot log prints `ingest: wired source … kind=pgaudit`. In a second terminal, log in
again and read the graph — this time the edges are **genuinely parsed**, not seeded:

```bash
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

curl -sf "$BASE/v1/m/accessmap/graph?limit=200" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
curl -sf "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

You get **3 edges** — `salesdb.public.customers` (read), `…orders` (write),
`…secrets` (read) — each with `signal_source: pg_audit` and `coverage_tier: clean`
(pgAudit reports R/W verbatim), and the drift flags all **3 as unexpected accesses**
(no grant is wired yet, so every observed access is drift).

:::note[Honest by default: approximate until you wire identity]
These real edges land as `attribution_tier: approximate`, not `firm` — the pgAudit
signal names a database role/application, not a *governed agent*. That is the honest
default: the product will not claim it firmly attributed an access to an agent it
cannot prove. You earn `firm` by wiring an identity source (LDAP/IdP/SPIFFE) that binds
the credential to an agent or workload identity — see
[connect a source](/2026-06/how-to/connect-a-source/). The demo estate shows `firm` edges
precisely because it pre-binds its agents.
:::

:::note[The endpoint shape]
The Permitted-vs-Observed result is served at `/v1/m/accessmap/drift` (there is no
`/diff`). The `/v1/m/accessmap/*` routes are reachable but **deliberately not** part of
the served OpenAPI document; the [API reference](/reference/api/) documents the core
REST surface.
:::

## 5. Reproduce this yourself

Everything above is asserted, end to end, against the real binary:

```bash
task smoke:quickstart          # or: scripts/quickstart-smoke.sh
```

It boots the demo estate **and** the real pgAudit path, runs the exact commands on this
page, and checks the numbers (20 nodes / 13 edges, 8 unexpected + 2 unused, 3 real
pgAudit edges). If the install→value path or the drift result ever stops being true,
the smoke fails — that is the contract that keeps this page honest. It completes in a
few seconds of wall clock; the human-walked path above is the documented **five
minutes**.

## Next steps

* **Run it for real:** the getting-started tutorials walk every install
  scenario end to end —
  [single node (systemd)](/2026-06/tutorials/getting-started/single-node/),
  [Docker Compose](/2026-06/tutorials/getting-started/docker-compose/),
  [Kubernetes/Helm](/2026-06/tutorials/getting-started/kubernetes/) and
  [air-gapped](/2026-06/tutorials/getting-started/air-gapped/);
  [self-hosting](/2026-06/how-to/self-hosting/) is the decision page across them.
* **Feed it real signals:** [connect a source](/2026-06/how-to/connect-a-source/) and the
  [connectors catalog](/2026-06/reference/connectors/) — what each source observes, its honest
  coverage tier, and how to wire identity so attribution becomes `firm`.
* **Harden it:** [security hardening](/2026-06/how-to/security-hardening/) — secure defaults,
  human-in-the-loop approvals, and verifying a release before you run it.
* **Know the limits:** [Honesty & limits](/2026-06/start/honesty-and-limits/) — what runs
  today, what is design-stage, and what the product deliberately does not do.
