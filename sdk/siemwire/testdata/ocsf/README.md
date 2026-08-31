<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Vendored OCSF class schemas

These are the **official** OCSF JSON Schema exports the encoder is validated
against, vendored verbatim so the conformance check needs no network and no
third-party toolkit.

| File | Class | Source |
|---|---|---|
| `api_activity.schema.json` | API Activity (6003) | `schema.ocsf.io/schema/1.8.0/classes/api_activity?profiles=ai_operation` |
| `process_activity.schema.json` | Process Activity (1007) | `schema.ocsf.io/schema/1.8.0/classes/process_activity?profiles=ai_operation` |
| `datastore_activity.schema.json` | Datastore Activity (6005) | `schema.ocsf.io/schema/1.8.0/classes/datastore_activity?profiles=ai_operation` |

`VERSION` records the schema version these files were exported for. It is what
makes the pin honest: the schemas themselves type `metadata.version` as a plain
string, so nothing inside them would object if the encoder started claiming a
version it was never checked against. `TestOCSFVersionPinMatchesVendoredSchemas`
compares `VERSION` with the `OCSFVersion` constant, so bumping the constant
without re-vendoring fails the build instead of shipping an unverified claim.

## Re-vendoring

1. Fetch each class above with `?profiles=ai_operation` for the new version.
2. Write the new version into `VERSION` and update `OCSFVersion`.
3. Run the suite: the class schemas set `additionalProperties: false`, so a field
   the new version removed, renamed or re-scoped fails loudly rather than
   silently dropping out of the emitted event.
