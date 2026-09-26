// Standalone module, like scripts/check-error-mappers: this helper must build and run
// with GOWORK=off so it never drags the workspace's build graph into a fast lint, and so
// a broken module elsewhere cannot stop it from looking. Standard library only.
module github.com/olivaresai/olivares/scripts/suspend-fence-guard

go 1.26.8
