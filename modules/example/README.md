# Reference module

This is an SDK authoring example, not a selectable product module. The shipped
engine does not register it, and it has no console, HTTP, or CLI surface. Load it
in a composition root with `Runtime.AddModule`; no catalog or module-spec entry
is required for this reference package.

The example subscribes to observed access edges and exposes their count through
`Count()`. The count belongs to the module instance and starts at zero in a new
instance. It also declares `example.observation` through `RegisterSchema`, but
does not write events to that table. A composition root can pass
`rt.RegisterSchema` to `engine.Open` to build the table with the engine's scoped
data guards. Schema registration precedes runtime startup.

From the repository root, save this program as `example-main.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	source "github.com/olivaresai/olivares/connectors/example"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/modules/example"
	"github.com/olivaresai/olivares/sdk"
)

func main() {
	if err := run(); err != nil {
		panic(err)
	}
}

func run() (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rt := runtime.New(runtime.Options{})
	mod := example.New()
	if err = rt.AddModule(mod, sdk.Config{}); err != nil {
		return err
	}
	if err = rt.AddSource(source.New(), sdk.Config{Settings: map[string]string{
		"count": "5", "resource": "public.orders",
	}}, "tenant-demo"); err != nil {
		return err
	}
	// Stop even when startup or delivery fails; report cleanup errors too.
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		err = errors.Join(err, rt.Stop(stopCtx))
	}()
	if err = rt.Start(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for mod.Count() < 5 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	if got := mod.Count(); got != 5 {
		return fmt.Errorf("observed %d edges, want 5", got)
	}
	fmt.Println("observed 5 edges")
	return nil
}
```

Build and run the consumer from that same repository root:

```sh
go build -o example-module example-main.go
./example-module
```

It prints `observed 5 edges`. Run it again to load fresh source and module
instances; it again counts five synthetic edges. This demonstrates the existing
in-process loading path, not collection from a real external data source.

The integration tests use that loading path and a real file-backed SQLite store:

```sh
go test ./modules/example -run '^TestExample' -count=1 -v
```

`TestExampleSchemaRetainsScopedDataAcrossRestart` writes a fixture through the
composition root, closes and reopens the store with active/dormant/active schema
registration, and checks retention, read-only write refusal and cross-tenant
read refusal. This does not add persistence to `Count()` or a product off/on
switch. PostgreSQL and out-of-process module loading are not qualified by these
checks; the reference module runs in process.
