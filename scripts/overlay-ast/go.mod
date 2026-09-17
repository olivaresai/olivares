// Standalone module, like scripts/check-list-page-discarded and scripts/check-error-mappers:
// this reader must build and run with GOWORK=off so it never drags the workspace's build
// graph into a gate, and so a broken module elsewhere cannot stop it from looking.
module github.com/olivaresai/olivares/scripts/overlay-ast

go 1.26.6
