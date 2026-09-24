# cedar-measure

A bounded measurement of the cedar-go policy library (v1.8.0) at fixed, submaximal input sizes. Its purpose is runtime evidence for choosing admission limits. It does not change the product, and it chooses no limit.

## What runs
- **`cedar-measure`** (Go, public cedar-go and `x/exp/ast` APIs only). Each process takes one measurement and prints one JSON record. It never prints policy text; inputs are identified by their SHA-256.
  - **`child`**: one of the 72 children in `MATRIX.json`. The child:
    1. generates its input and checks its length and digest;
    2. runs setup in a goroutine that exits;
    3. collects garbage twice;
    4. runs one operation in a fresh goroutine, between ordered readings;
    5. checks the operation's result afterwards.

    It writes fixed phase codes (`setup`, `measure`, `check`, `emit`) to standard error, only outside the measured interval, so a child that stops without a record still shows where it stopped.
  - **`validate`**: every input of the matrix, plus the hand-written low points.
  - **`oframe`**: framer agreement with the library over the cedar-go corpus (8 batches) and an edge set. This is a separate packet.
- **`supervisor.py`** (Python 3, standard library only). It runs, as root, one child at a time. Each child runs as the invoking user in a fresh cgroup v2 group.
  - **Limits:** a kill time that covers setup, `memory.max`, `memory.swap.max`, `pids.max`, and a fixed runtime environment (`GOMAXPROCS`, and `GODEBUG` with the collector's stack shrinking disabled). The values live only in `supervisor.py`.
  - It checks every record independently: token, identity, input digest, pins, the order of the readings, and the semantic result.
  - It stops every larger point of a cell after a failure.
  - It runs deliberate defects first. Each must be refused before any measurement counts.
- **`.github/workflows/cedar-measure.yml`**: the job for a hosted `ubuntu-latest` runner. It starts on manual dispatch with a packet choice, or on a push to one of two fixed measurement branches, each of which selects one packet. A push to any other branch starts nothing; a manual dispatch runs its chosen packet. Unsupported events or packet values are refused before anything is built. Module and build caches stay outside the uploaded evidence.

## Readings (per measured child)
| reading | meaning |
|---|---|
| operation ns | monotonic time between the marks around the one operation |
| allocated bytes and objects | `runtime.MemStats` `TotalAlloc` and `Mallocs`, after minus baseline |
| stack bytes | `/memory/classes/heap/stacks:bytes` right after the operation, inside its goroutine, with stack shrinking disabled. An aggregate retained-stack observation for the whole process, not a per-function peak |
| live heap bytes (compile only) | `/gc/heap/live:bytes` after a collection, with the compiled policy still reachable |
| memory peak | the child's own cgroup `memory.peak`: a whole-process high-water mark |

A compile reading covers fold and conversion together, because they share one call.

## Exit status
One outcome policy covers every child, every packet and the supervisor itself.
- **0 coherent:** every child was observed and every deliberate defect was refused.
- **1 measured mismatch:** a readable record differs (result, identity, digest, pin, runtime environment or reading order); a deliberate defect was accepted; or a control shows a readable mismatch instead of its named refusal.
- **2 inability:** no isolation, a setup failure, a timeout, out of memory, an abnormal exit, a missing or unreadable record, a record whose outcome disagrees with its exit code, a control that could not be observed, no time left, or a supervisor fault (recorded by its type only).

A child that stops before its measurement and says so keeps its own class and code (`child:<code>`); no measurement is invented for it. When several results mix, any measured mismatch gives 1, and every accompanying inability is still recorded.

A failure is kept as evidence and never counted as a pass. Nothing retries.

## Files
`go.mod` and `go.sum` hold the only Go and cedar-go pins. Setup builds from a working copy of the build inputs, because `go mod download` may drop the `toolchain` line that repeats the `go` line; after the build, setup checks every copied input and the copy's file list against the committed bytes, records both go.mod texts, and accepts no other change; a refusal after these steps keeps their log and names its reasons. An inability inside a setup step records its reason in inability.json, but earlier step logs may be absent. The pins are always read from the committed files, and the run checks the copy again and refuses a binary that setup did not build from those exact bytes. `supervisor.py` holds the only limits. `MATRIX.json` holds the only inputs and expectations, and `schema/` describes both JSON formats.
