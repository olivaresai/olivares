// Standalone module, like scripts/check-error-mappers: this gate must build and run
// with GOWORK=off so it never drags the workspace's build graph into a fast lint, and
// so a broken module elsewhere cannot stop it from looking.
module github.com/olivaresai/olivares/scripts/check-list-page-discarded

go 1.26.6
