// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/olivaresai/olivares/sdk"
)

// pluginOpenTimeout bounds the health check and Open of a restarted output plugin. They
// run on a context detached from the delivery that found the plugin dead, so a
// delivery that already timed out neither hides a wedged plugin nor makes Open fail.
const pluginOpenTimeout = 30 * time.Second

// supervisedOutput is the connector DispenseOutputPlugin and
// DispenseOutputPluginVerified return: it delivers through the plugin's connector
// and, when a delivery fails because the plugin process died, starts the same
// binary again so the next delivery reaches a live process. It is the destination
// twin of restartDeadPlugin: a source is supervised by its gather loop, a
// destination has no loop, so the failed Notify is the signal.
//
// The failed delivery still returns its error: the caller (the notify outbox)
// retries it, and the retry runs on the new process. A plugin that is alive and
// whose Notify fails is the connector's own failure and is left alone.
type supervisedOutput struct {
	rt     *Runtime
	launch pluginLaunch
	first  *goplugin.Client // the client the dispenser returned, whose owner reaps it

	mu     sync.RWMutex
	conn   sdk.OutputConnector
	client *goplugin.Client
	cfg    sdk.Config // the resolved settings Open ran with; a restart opens with them
	closed bool

	// restartMu serializes restarts; the fields below are guarded by it. Attempts are
	// spaced by wait (the first is immediate), which doubles per attempt up to
	// maxRepollBackoff and starts over once the last attempt is older than that, so a
	// plugin that dies right after each start is not relaunched on every delivery.
	restartMu   sync.Mutex
	wait        time.Duration
	notBefore   time.Time
	attemptedAt time.Time
}

func (o *supervisedOutput) current() (sdk.OutputConnector, *goplugin.Client) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.conn, o.client
}

func (o *supervisedOutput) Descriptor() sdk.Descriptor {
	conn, _ := o.current()
	return conn.Descriptor()
}

func (o *supervisedOutput) Open(ctx context.Context, cfg sdk.Config) error {
	conn, _ := o.current()
	if err := conn.Open(ctx, cfg); err != nil {
		return err
	}
	o.mu.Lock()
	o.cfg = cfg
	o.mu.Unlock()
	return nil
}

func (o *supervisedOutput) Notify(ctx context.Context, n sdk.Notification) error {
	conn, client := o.current()
	err := conn.Notify(ctx, n)
	if err != nil {
		// A refused or failed restart rides along with the delivery error, so the
		// outbox's record says why the destination keeps failing.
		if rerr := o.restartIfDead(ctx, conn, client); rerr != nil {
			err = errors.Join(err, rerr)
		}
	}
	return err
}

// Close closes the connector and, when the plugin was restarted, reaps the process
// that replaced the one the dispenser's caller holds: that caller knows only the
// first client, and a live reload or remove would otherwise leave the new process
// running until Stop.
func (o *supervisedOutput) Close(ctx context.Context) error {
	o.mu.Lock()
	o.closed = true
	conn, client := o.conn, o.client
	o.mu.Unlock()
	err := conn.Close(ctx)
	if client != o.first {
		o.rt.reapPlugin(client)
	}
	return err
}

// restartIfDead replaces the plugin process behind failed when its Notify failed
// and the process is gone or wedged (pluginDead). It returns the reason a restart it
// attempted failed, nil when it restarted the plugin or had nothing to do. A failed
// attempt leaves the connector as it was, so the next failed Notify tries again once
// the wait is over. Only one delivery restarts at a time: the others return at once
// and fail, and the outbox retries them on the new process.
func (o *supervisedOutput) restartIfDead(ctx context.Context, failed sdk.OutputConnector, client *goplugin.Client) error {
	if !o.restartMu.TryLock() {
		return nil
	}
	defer o.restartMu.Unlock()
	if o.stoppedOrClosed() { // torn down by a reload or Stop
		return nil
	}
	if conn, _ := o.current(); conn != failed {
		return nil // another delivery already restarted it
	}
	now := time.Now()
	if now.Before(o.notBefore) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pluginOpenTimeout)
	defer cancel()
	if !pluginDead(ctx, client) {
		return nil
	}
	if o.wait == 0 || now.Sub(o.attemptedAt) > maxRepollBackoff {
		o.wait = pluginRestartBackoff
	}
	o.attemptedAt, o.notBefore = now, now.Add(o.wait)
	o.wait = min(o.wait*2, maxRepollBackoff)

	plugin := filepath.Base(o.launch.path)
	o.rt.log.Warn("runtime: output plugin process exited; restarting it", "plugin", plugin)
	o.rt.reapPlugin(client)
	err := o.relaunch(ctx)
	switch {
	case err == nil:
		o.rt.log.Info("runtime: output plugin restarted", "plugin", plugin)
		return nil
	case errors.Is(err, ErrStopped):
		return nil // closed or stopped while it ran: nothing to retry
	}
	o.rt.log.Warn("runtime: output plugin restart failed; will retry", "plugin", plugin, "error", err, "retry_after", o.notBefore.Sub(now))
	return fmt.Errorf("runtime: output plugin restart: %w", err)
}

// stoppedOrClosed reports whether the runtime stopped or this connector was closed.
func (o *supervisedOutput) stoppedOrClosed() bool {
	o.rt.mu.Lock()
	defer o.rt.mu.Unlock()
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.rt.stopped || o.closed
}

// relaunch starts the plugin again, opens it with the settings of the first Open and
// swaps it in. It refuses before Open, which sends the resolved settings to the new
// process, and again before the swap, when the connector was closed or the runtime
// stopped while it ran, so a restart never leaves a process nobody tears down.
func (o *supervisedOutput) relaunch(ctx context.Context) error {
	conn, client, err := o.rt.launchOutput(o.launch)
	if err != nil {
		return err
	}
	if o.stoppedOrClosed() {
		o.rt.reapPlugin(client)
		return ErrStopped
	}
	o.mu.RLock()
	cfg := o.cfg
	o.mu.RUnlock()
	if oerr := safe(func() error { return conn.Open(ctx, cfg) }); oerr != nil {
		o.rt.reapPlugin(client)
		// Open ran on the RESOLVED settings, so its text may carry a secret: only the
		// debug log sees it, as for a source (cmd/olivares reconcile.go).
		o.rt.log.Debug("runtime: restarted output plugin refused its settings", "plugin", filepath.Base(o.launch.path), "error", oerr)
		return errors.New("runtime: restarted output plugin refused its settings")
	}
	o.rt.mu.Lock()
	o.mu.Lock()
	if o.rt.stopped || o.closed {
		o.mu.Unlock()
		o.rt.mu.Unlock()
		o.rt.reapPlugin(client)
		return ErrStopped
	}
	o.conn, o.client = conn, client
	o.rt.clients = append(o.rt.clients, client)
	o.mu.Unlock()
	o.rt.mu.Unlock()
	return nil
}
