// Standalone module, like scripts/check-error-mappers and scripts/overlay-ast: this
// reader must build and run with GOWORK=off so it never drags the workspace's build
// graph into a fast lint, and so a broken module elsewhere cannot stop it looking.
module github.com/olivaresai/olivares/scripts/cli-render-tabwriter

go 1.26.6
