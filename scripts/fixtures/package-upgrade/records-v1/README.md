# records-v1: the PKG-L1 package-record format fixture (m-5 schema v1)

The grammar is the agreed m-5 schema, pkg-l1-m5-schema/RESPONSE-D-M5-CONTRAST.md (f5774e95).
check-records-v1.py parses by it. The shipped maintainer scripts write exactly these forms:
- `/run/olivares.pkg-phases`: one v1 line per script run, in the fixed key order.
- `/run/olivares.pkg-{upgrade-state,deconfigured,pending}`: key=value records.
- `/var/lib/olivares-package/install-state`: a key=value record.

Each record begins with its `schema=` line and the common fields.

- `valid/`: one example of each line and record. `valid/pkg-phases` holds the longest line the grammar allows (506 bytes
  with its newline, under 512). `valid/pending.max.record` holds the longest pending record (614 bytes, under 1024). Also
  valid: the short forms a writer uses past a bound, `v1 <UTC> script=<name> result=record-too-large` and
  `schema=...` followed by `result=record-too-large`.
- `negative/`: an unknown schema or line version, an oversized record or line, `boot=unknown`, a missing field, and a key out
  of order. `check-records-v1.py` exits 0 on `valid/*` and 1 on each negative.
- `boot=unknown` is well-formed, and the scripts write it when /proc cannot be read, but it is never evidence. The checker
  refuses it unless `--grammar` is given, which the script-level tests use for what the scripts write.
- v1 result values: `ok`, `refused:<code>`, `record-too-large`, `record-not-written:<name>`, and `old-manifest-unread` (a
  bounded read of an older package's manifest did not finish, or found no regular file).

9b's HM-23-A consumer tests read this directory.
