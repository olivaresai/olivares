// Standalone module, like scripts/config-env-docs and scripts/check-error-mappers: this
// generator/gate must build and run with GOWORK=off so it never drags the workspace's
// build graph into a fast lint, and so a broken module elsewhere cannot stop it from
// rendering. Stdlib only — no dependency to resolve in an air-gapped gate.
module github.com/olivaresai/olivares/scripts/cli-ref-docs

go 1.26.6
