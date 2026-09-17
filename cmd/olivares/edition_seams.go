// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
	"io"
	"log/slog"
)

// Edition seams for the session cockpit (V269 / docs/contracts/COCKPIT-07-edition-cut.md §4).
//
// This file carries NO build tag on purpose: it holds the TYPES and the runner the
// three build variants share, so the tagged files
// (cmd/olivares/wire_noenterprise.go, and the overlay's
// wire_enterprise_{addon_ids,noaddon_ids}.go) declare only the constructors. A type
// declared inside a tagged file would have to be written three times and could drift
// three ways.

// editionAuxServer is one AUXILIARY network listener an edition serves alongside the
// HTTP and gRPC servers.
//
// ⛔ WHY IT IS A LISTENER OF ITS OWN AND NOT A SERVICE ON THE SHARED gRPC SERVER, which
// is the cheaper-looking option: the cockpit's agent link is mTLS with the cockpit's OWN
// CA and a SAN→node verifier at accept time. The shared server's TLS policy belongs to
// the OPERATOR and has two settings, both unusable here — without a client CA the peer
// presents no certificate at all, and with one EVERY peer must present one, so a REST
// client would need a certificate for the cockpit's sake. Registering the agent service
// there would make "authenticated with mTLS" depend on somebody else's flag.
//
// The lifecycle IS shared: Serve runs until the serve context is done or it fails, and
// Shutdown is called on the same path as the HTTP server's.
type editionAuxServer interface {
	// Name identifies the listener in logs. It is not a route or an address.
	Name() string
	// Serve blocks until ctx is done or the listener fails.
	Serve(ctx context.Context) error
	// Shutdown drains it. It must be safe to call after a failed Serve.
	Shutdown(ctx context.Context) error
}

// serveEditionAuxServers starts each auxiliary listener on its own goroutine and reports
// a failure on errCh, exactly as serveHTTP does for the HTTP listeners.
//
// A build with no auxiliary listener passes an empty slice and this is a no-op — the
// same shape as enterpriseRootCommands returning nil. In phase 0 that is EVERY build,
// including `addon_ids`: the agent listener is phase 1A work. The seam lands first so
// the wiring, the twins and the shutdown path are already in their final shape when the
// engine arrives.
func serveEditionAuxServers(ctx context.Context, servers []editionAuxServer, log *slog.Logger, errCh chan<- error) {
	for _, srv := range servers {
		if srv == nil {
			continue
		}
		s := srv
		log.Info("edition auxiliary listener starting", "name", s.Name())
		go func() {
			if err := s.Serve(ctx); err != nil && ctx.Err() == nil {
				// ctx.Err() != nil means we are shutting down, and a listener closing
				// then is the expected end of Serve, not a fault to report.
				errCh <- err
			}
		}()
	}
}

// shutdownEditionAuxServers drains every auxiliary listener. Errors are logged and not
// returned: shutdown runs after the process has already decided to stop, and a listener
// that will not drain must not mask the exit reason the caller is carrying.
func shutdownEditionAuxServers(ctx context.Context, servers []editionAuxServer, log *slog.Logger) {
	for _, srv := range servers {
		if srv == nil {
			continue
		}
		if err := srv.Shutdown(ctx); err != nil {
			log.Error("edition auxiliary listener did not drain cleanly", "name", srv.Name(), "err", err)
		}
	}
}

// ⛔ ESTE BLOQUE VIVE AQUÍ Y NO EN `wire_noenterprise.go`, Y LA DIFERENCIA ES QUE COMPILE. Lo
// escribí allí y ese fichero lleva `//go:build !enterprise`: el tipo que las DOS ediciones
// necesitan quedaba invisible para la que de verdad lo consume, y el ensamblado falló con
// `undefined: EditionConfig` en el gemelo del overlay. Este fichero no lleva etiqueta —es donde
// viven las costuras— y es el único sitio desde el que una costura puede ser la misma para ambas.
// EditionConfig is what the composition root hands the edition seams. It exists because a seam
// that needs the deployment's configuration cannot invent it.
//
// ⛔ Y NACE POR UNA AUSENCIA MEDIDA, no por simetría: el store privado del cockpit tiene runner de
// migraciones, dos migraciones fijadas y 33 tablas, y NADIE lo abre — `Migrate` tiene una sola
// aparición en el árbol comercial, su propia definición. La causa no era descuido: **ninguna
// costura `edition*` recibía configuración**, así que el overlay no podía saber dónde vive el
// directorio de datos, y un cliente que instala se queda sin base.
//
// ⛔ Y EL OVERLAY NO PUEDE RE-DERIVARLO, que es la salida cómoda y es un defecto. El directorio
// sale de `--data-dir` con su cadena de defectos (`$OLIVARES_DATA_DIR`, un `./olivares-data`
// existente, `$XDG_DATA_HOME/olivares`, `~/.local/share/olivares`) y la resuelve LA BASE. Un
// segundo productor de esa decisión pondría la base del cockpit **en otro sitio que los datos del
// motor** en cuanto el operador pasara el flag — verde en su propio SELECT y en el directorio
// equivocado. Se pasa, no se recalcula.
//
// Es una estructura y no un `string` para que el siguiente campo no vuelva a mover esta firma en
// dos árboles a la vez.
type EditionConfig struct {
	// DataDir is the resolved data directory: the value the flag chain produced, never a
	// re-derivation of it.
	DataDir string
}

// editionConfigFrom is the mapping the composition root applies, split out so it can be tested.
//
// ⛔ ES LO ÚNICO DE ESTA CADENA QUE PUEDE ESTAR MAL DE FORMA SILENCIOSA. Que la costura reciba un
// `EditionConfig` lo garantiza el compilador; que reciba EL CAMPO CORRECTO no lo garantiza nadie —
// pasar aquí otro directorio compilaría igual y pondría la base del cockpit donde no toca. Por eso
// el testigo del hub es sobre esta función y no sobre la firma.
func editionConfigFrom(cfg bootConfig) EditionConfig {
	return EditionConfig{DataDir: cfg.DataDir}
}

// EditionDependencies contains existing engine capabilities, never a private engine
// or a second configuration source. Boot binds these after store/auth composition.
type EditionDependencies struct {
	Store    store.Store
	Sessions sessions.SessionIdentityReader
	Rows     api.RowAuthorizationPort
}

func closeEditionResources(resources []io.Closer, log *slog.Logger) {
	for i := len(resources) - 1; i >= 0; i-- {
		if err := resources[i].Close(); err != nil {
			log.Error("edition resource close failed", "err", err)
		}
	}
}
