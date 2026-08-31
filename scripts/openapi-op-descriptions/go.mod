// Standalone module, like scripts/config-env-docs and scripts/check-error-mappers: this
// generator/gate must build and run with GOWORK=off so it never drags the workspace's
// build graph into a fast lint, and so a broken module elsewhere cannot stop it from
// enumerating the routes the modules register.
module github.com/olivaresai/olivares/scripts/openapi-op-descriptions

go 1.26.6
