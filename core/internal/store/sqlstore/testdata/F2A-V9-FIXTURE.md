<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# F2-A historical v9 fixture

Source base: `ae19349aea39e27c51e0fc368382204848603592`, tree
`7c3c84cee4104aef585eb64aad6190abaebc0570`. The two `.go.txt` files in this directory
are test adapters; they are not replacement migration, writer or activation code.
Archive that exact commit into an isolated directory and copy the adapters to
`core/internal/store/sqlstore/f2a_fixture_test.go` and `f2a_history_test.go` there.
With the repository's Go toolchain, build:

```sh
go test -c ./core/internal/store/sqlstore -o /absolute/path/v9-store.test
```

Set `OLIVARES_F2A_V9_BINARY` to the absolute result when running current
`TestUserAuthority` tests. The existing pgtest split-role fixture owns each
PostgreSQL database; SQLite uses t.TempDir. Tests explicitly skip this historical
leg when the binary is absent. Acceptance evidence must record a run with it
present and the PostgreSQL leg required; a skip is not historical evidence.

The producer creates SYSTEM, two business organizations, two Users with direct
memberships and sessions, using the real archived v9 APIs. Each G starts at 2.
`enforced` invokes v9's actual activation and records generation 2. `writer`
leaves v9's process open, accepts a staged write instruction and then a target
write instruction through stdin. PostgreSQL's old staged writer can still write;
SQLite's new marker rejects it. Both old processes reject after target cutover.
The JSON manifest contains only disposable fixture data.

`TestF2AV9MigrationRender` measures both engines' ordered v1–v9 migration statement
lists/metadata, frozen v7 control rendering and lineage guard DDL. The current
historical-render test pins its measured digests. Exec-only migration bodies also
retain the existing v9 seal goldens; an empty statement list is not a claim that
an Exec body was serialized. Do not regenerate goldens from the current source.
