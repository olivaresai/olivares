<!--
Thanks for contributing to Olivares AI. This checklist mirrors CONTRIBUTING.md —
it does not restate the rules, it points to them. Tick what applies; explain
anything you can't tick. Keep the PR focused on one unit of work.
-->

## What and why

<!-- What does this change do, and why? Link the issue it addresses (e.g. "Closes #123").
     For anything non-trivial, link the issue/discussion where the approach was agreed. -->

Closes #

## Type of change

<!-- Pick the Conventional Commit type that matches (feat/fix/refactor/docs/test/chore/...). -->

- [ ] `feat` — new feature
- [ ] `fix` — bug fix
- [ ] `docs` / `chore` / `refactor` / `test` — non-functional change
- [ ] Breaking change (explain the migration below)

## Checklist

<!-- See CONTRIBUTING.md for the details behind each item. -->

- [ ] **DCO sign-off** on every commit (`git commit -s`) — required by the project (enforced at review).
- [ ] **CLA** signed, if I am an external contributor (one-time, before first merge) — see `CLA.md`.
- [ ] **Conventional Commits** in English, validated by the `commit-msg` hook.
- [ ] **`task lint:spdx lint:boundary`**, **`task build:go`** and **`task test`** pass locally (CI runs the same subset plus `govulncheck` + gitleaks; the full `task lint` is not part of the gate — see CONTRIBUTING.md).
- [ ] **SPDX header** correct for the directory each new source file lives in (AGPL / Apache / commercial — see `CONTRIBUTING.md` and `LICENSING.md`). Non-code files are annotated centrally in `REUSE.toml`, not inline.
- [ ] **License boundary respected:** a connector/SDK change imports **only** from `sdk/`, never from `core/` (`scripts/check-boundary.sh`).
- [ ] **`CHANGELOG.md`** `[Unreleased]` updated under the right heading (Added/Changed/Deprecated/Removed/Fixed/**Security**) if this is a user-visible `feat:`/`fix:`/breaking change.
- [ ] **No secrets, credentials, or personal data** added to the repo, tests, or fixtures.
- [ ] This PR is **not** a security fix being disclosed early — security issues follow `SECURITY.md` (private), and the public advisory/CHANGELOG entry comes after a coordinated fix.

## Notes for the reviewer

<!-- Anything that helps review: design decisions, trade-offs, what you tested, follow-ups. -->
