<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Terminal generation evidence on `sessions.run_event`

Two nullable columns and one versioned payload encoding. They record **which runtime
generation a terminal transition retired** and **what was actually observed about that
process** — two different facts that the ledger previously could not tell apart, because
both terminal writers clear `runtime_launch_id` inside the same mutation that seals the
event.

## Columns

| Column | Kind | Present when |
| --- | --- | --- |
| `retired_runtime_launch_id` | UUID, nullable | a terminal transition retired a recorded generation |
| `terminal_observation` | text, nullable | a terminal transition recorded an observation |

Both are added by the engine's descriptor reconciler, not by a module SQL migration, so a
fresh database and an upgraded one cannot disagree. No append-only row is ever rewritten.

## Observations

| Value | Means |
| --- | --- |
| `process_exit_observed` | the owned `Process.Wait` returned a nil error, so under the `Process` contract that process exited. Says nothing about unrelated descendants or remote hosts. |
| `process_wait_unverified` | `Wait` returned an error. The terminal run state is still recorded; collection was **not** confirmed. The error text is never stored. |
| `handle_lost_unconfirmed` | orphan recovery made the row terminal after losing the handle. It does **not** confirm process exit. |

`retired_runtime_launch_id` may be absent only for `handle_lost_unconfirmed`, on a legacy
row that never carried a reservation. It is stored NULL, never an empty UUID. An ID
without an observation, an unknown observation, or an absent ID under either process
observation refuses the transition before either ledger commits.

## Payload encoding

Every string is `decimal UTF-8 byte length`, `:`, the bytes. SHA-256 over the
concatenation.

**Without evidence** — unchanged, byte for byte: seven base strings (`run_ref`, decimal
`seq`, `event`, `from_state`, `to_state`, `detail`, timestamp), then the three
work-generation strings only when that group is present. Historical seven- and ten-string
digests are never rewritten.

**With evidence** — exactly fourteen strings, every slot always present:

1. `olv.sessions.run_event.terminal.v1`
2. `run_ref`
3. decimal `seq`
4. `event`
5. `from_state`
6. `to_state`
7. `detail`
8. timestamp
9. `1` when a work generation is present, else `0`
10. work item ID, else empty
11. work holder SID, else empty
12. decimal work fence, else empty
13. retired runtime launch ID, else empty for the permitted legacy recovery
14. terminal observation

The explicit version in slot 1 and the fixed work-presence flag in slot 9 are why a P1
event cannot be confused with a historical encoding whatever a caller puts in the other
fields.

## Reading it

Both columns surface as optional JSON fields on the existing authenticated run-event
read. Old records omit both.

- `process_wait_unverified` and `handle_lost_unconfirmed` **do not** satisfy confirmed
  process termination.
- No match is **unknown** — not a reason to relaunch or redispatch.
- A matching historical retirement can describe an older generation even if the run has
  since resumed; it never authorizes an action on the current one.
- `audit_seq = 0` means the projection has no accepted core-audit anchor under the
  existing degraded policy. It is not sealed proof, and a non-zero sequence alone is not
  cryptographic verification either.

This is a producer and a representation. It does not implement managed Stop, prevent
duplicate dispatch while a prior operation is uncertain, or establish exactly-once
execution.
