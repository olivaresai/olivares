# Examples

Copy-paste paths by scenario, each **run against the current `olivares`
binary** and validated in CI — no speculative snippets. Start with the
[5-minute quickstart](../docs-site/src/content/docs/start/quickstart.md) (install →
R/RW access map → permitted-vs-observed drift); the examples here pick up where it
ends, one real scenario at a time.

## Runnable examples (with CI smoke tests)

| Example | What it shows | Surface |
|---|---|---|
| [**govern-claude-code/**](./govern-claude-code/) | Govern an agent's tool-calls: **allow / deny / rewrite** a Claude Code `PreToolUse` call before it runs, deny-closed and audited. | hooks PEP |
| [**otel-genai-ingest/**](./otel-genai-ingest/) | Send vendor-neutral OpenTelemetry **GenAI** (`gen_ai.*`) telemetry from any framework and watch it become attributed **cost** in FinOps. | OTLP GenAI |
| [**build-a-connector/**](./build-a-connector/) | Scaffold, build, test and boundary-check **your first connector** — offline, never importing `/core`. | connector SDK |
| [**bring-your-own-protocol/**](./bring-your-own-protocol/) | Turn a **proprietary in-house protocol** (a fictional FabWorks ERP) into a governed content source: scaffold, fill, sign, and prove deny-closed admission — fully offline. | content-source SDK |

Each directory has a `README.md` and a `smoke.sh` that runs the documented steps and
asserts the outcome. Run one:

```sh
task build                              # produces ./bin/olivares
examples/govern-claude-code/smoke.sh
```

…or all of them at once:

```sh
task smoke:examples
```

> **Heads-up on `/tmp`:** the OTEL and connector smokes need an exec-capable temp
> dir (the engine fork/execs the embedded source plugin from `$TMPDIR`; `go test`
> execs its test binary there). On a normal host `/tmp` works; in a hardened
> container with `noexec` on `/tmp`, set `OLIVARES_SMOKE_TMPDIR` (or `TMPDIR`) to an
> exec-capable path. The smokes default to a repo-local scratch dir for this reason.

## Already covered elsewhere (don't duplicate)

These scenarios ship as first-class assets — the examples above deliberately don't
re-implement them:

- **Run it** — single-node and real-Postgres Docker Compose stacks in
  [`deploy/compose/`](../deploy/compose/); Helm chart in
  [`deploy/helm/olivares/`](../deploy/helm/olivares/);
  flat `kubectl apply` manifest at
  [`deploy/manifests/install.yaml`](../deploy/manifests/install.yaml).
- **Manage as code** — the Terraform/OpenTofu provider, with its own standard
  `examples/{provider,resources,data-sources}` layout, lives in
  [`terraform-provider-olivares/`](../terraform-provider-olivares/); GitOps
  (ArgoCD/Flux) reconciliation in [`deploy/gitops/`](../deploy/gitops/).
- **Author policy** — managed-settings, hooks and Cedar/ABAC policy authoring are
  documented under the [docs site how-to guides](../docs-site/src/content/docs/how-to/).

## Conventions

- Every command shown in a README is exercised by that example's `smoke.sh`. If a
  step can't be tested against the binary, it isn't in the example.
- The smokes are self-contained: they build the binary if it is absent, run on
  loopback, use a throwaway data dir, and clean up after themselves.
