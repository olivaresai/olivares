// Standalone module, like scripts/check-error-mappers and scripts/export-scrub: this
// generator/gate must build and run with GOWORK=off so it never drags the workspace's
// build graph into a fast lint, and so a broken module elsewhere cannot stop it from
// enumerating.
module github.com/olivaresai/olivares/scripts/config-env-docs

go 1.26.6
