<!--
Thanks for contributing to Olivares AI. This checklist mirrors CONTRIBUTING.md —
it does not restate the rules, it points to them. Tick what applies; explain
anything you can't tick. Keep the PR focused on one unit of work.
-->

## What and why

<!-- Describe the problem and observable before/after behavior; link the requirement or issue.
     For a non-trivial change, link the decision/discussion that agreed the approach. -->

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
- [ ] **`task lint:spdx lint:boundary`**, **`task build:go`** and **`task test`** pass locally (web tests use `task test:web`; CI has its own functional/race split and additional checks; full `task lint` remains outside the gate — see CONTRIBUTING.md).
- [ ] **SPDX header** correct for the directory each new source file lives in (AGPL / Apache / commercial — see `CONTRIBUTING.md` and `LICENSING.md`). Non-code files are annotated centrally in `REUSE.toml`, not inline.
- [ ] **License boundary respected:** a connector/SDK change imports **only** from `sdk/`, never from `core/` (`scripts/check-boundary.sh`).
- [ ] **`CHANGELOG.md`** `[Unreleased]` updated under the right heading (Added/Changed/Deprecated/Removed/Fixed/**Security**) if this is a user-visible `feat:`/`fix:`/breaking change.
- [ ] **No secrets, credentials, or personal data** added to the repo, tests, or fixtures.
- [ ] This PR is **not** a security fix being disclosed early — security issues follow `SECURITY.md` (private), and the public advisory/CHANGELOG entry comes after a coordinated fix.

## Notes for the reviewer

<!-- Keep this proportional to the change. Connect the decision to the relevant code and
     positive/negative checks. Record the tested SHA, command/job, environment, result and
     evidence link; name skips, scope limits and follow-ups. For prose-only changes, record
     the source comparison and document checks instead of implying a product test run. -->
