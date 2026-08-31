<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Adopter checklist — install to evidence

Use this checklist to exercise the shortest operational path from a source checkout
to a drift finding, a policy denial and a verified evidence export. Run the commands
from the repository root. The demo estate is loopback-only and uses public demo
credentials; do not use it as a production deployment.

Start the timer immediately before the first build command. Stop it after the audit
verification succeeds. Record wall-clock time, including operator actions and any
investigation needed to complete a milestone.

## Milestone record

| Complete | Milestone | Started at | Completed at | Measured time |
|---|---|---|---|---|
| [ ] | Install and boot |  |  |  |
| [ ] | First drift finding |  |  |  |
| [ ] | First deny |  |  |  |
| [ ] | First evidence export |  |  |  |
| [ ] | **Time-to-hero total** |  |  |  |

## 1. Install and boot

- [ ] Build the single binary and confirm that it reports its version.

```bash
task build                      # compiles ./bin/olivares with the web UI embedded
./bin/olivares version
```

- [ ] Start the loopback-only demo estate and leave this process running.

```bash
DATA="$(mktemp -d)"
./bin/olivares serve --insecure --seed-demo \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$DATA"
```

- [ ] Confirm that the demo-mode banner and the following public credentials appear.

```text
demo@olivares.local / olivares-demo-estate
```

- [ ] Record the milestone timestamps and measured time in the table above.

## 2. Produce the first drift finding

- [ ] In a second terminal, authenticate and resolve the demo tenant.

```bash
BASE=http://127.0.0.1:8901
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

TENANT="$(curl -sf "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;[print(o["tenant_id"]) for o in json.load(sys.stdin)["items"] if o["slug"]=="demo"]')"
```

- [ ] Retrieve the read/write access map and the permitted-versus-observed drift.

```bash
# The read/write access map — module III:
curl -sf "$BASE/v1/m/accessmap/graph?limit=200" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool

# The Permitted-vs-Observed drift:
curl -sf "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

- [ ] Confirm that the demo access map contains 20 nodes and 13 edges.
- [ ] Confirm that the drift result contains 8 unexpected accesses and 2 unused
  grants; select and record the first unexpected access as evaluation evidence.
- [ ] Record the milestone timestamps and measured time in the table above.

## 3. Produce the first deny

- [ ] Open `http://127.0.0.1:8901`, sign in with the demo credentials and switch to
  the **Demo Estate** organization.
- [ ] Create a `forbid` policy through the console. The evaluation guide also defines
  the create operation as `POST /v1/m/governance/policies`; `PUT /policies/{id}` is
  update, not create.
- [ ] Submit the next governed request for the configured tool.
- [ ] Confirm that the request returns 403 and that the deny event appears in the
  audit ledger.
- [ ] Record the policy identifier, denied operation and audit event reference.
- [ ] Record the milestone timestamps and measured time in the table above.

## 4. Export and verify the first evidence

- [ ] Export the audit ledger in CEF format. The audit commands read the store
  directly, so pass the estate's data directory and the tenant explicitly
  (`--tenant` is required).

```bash
./bin/olivares audit export --data-dir "$DATA" --tenant "$TENANT" --format cef
```

- [ ] Confirm that the export contains CEF event lines, including the deny evidence.
- [ ] Verify the ledger.

```bash
./bin/olivares audit verify --data-dir "$DATA" --tenant "$TENANT"
```

- [ ] Confirm that verification passes.
- [ ] Record the export location or evidence reference.
- [ ] Record the milestone timestamps and measured time in the table above.
- [ ] Calculate **Time-to-hero total** from the start of the build through successful
  audit verification and enter it in the final row of the milestone record.

## Completion record

| Field | Value |
|---|---|
| Operator |  |
| Environment |  |
| Date |  |
| First drift evidence reference |  |
| First deny evidence reference |  |
| First export evidence reference |  |
| Time-to-hero total |  |
| Outcome | Pass / Fail / Blocked |

