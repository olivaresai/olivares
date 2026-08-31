// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/sdk"
	sdkplugin "github.com/olivaresai/olivares/sdk/plugin"
	olv1 "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

// newCollectorCmd runs the binary as an edge COLLECTOR (CB-1 option C, ARCHITECTURE.md):
// it loads the configured source connectors LOCALLY (OLIVARES_SOURCES_CONFIG) and
// PUSHES their observations to a remote core over gRPC+mTLS. It has NO inbound
// listener — the secure-default data plane of the distributed topology. The exact
// same source wiring (wireSources) the engine uses runs here; only the runtime's
// SinkFactory differs (it pushes instead of lifting onto a local bus), so a
// connector behaves identically single-node and distributed. The distributed
// PACKAGING (a DaemonSet of collectors + Helm/OCI) is ; this is the mechanism.
func newCollectorCmd() *cobra.Command {
	var (
		coreAddr   string
		tokenFile  string
		caFile     string
		clientCert string
		clientKey  string
		serverName string
		insecureTL bool
	)
	cmd := &cobra.Command{
		Use:   "collector",
		Short: "Run as an edge collector: push local source observations to a remote core over gRPC+mTLS",
		Long: "collector loads the source connectors named in OLIVARES_SOURCES_CONFIG and pushes their\n" +
			"observations to a remote core's ingest endpoint. It opens no inbound listener.\n" +
			"It presents a collector mTLS client certificate (when the core enforces --grpc-client-ca)\n" +
			"and a bearer token (--token-file) holding an ingest:write principal.",
		Example: "  olivares collector --core-addr core.internal:9443 --token-file /run/secrets/ingest.token \\\n" +
			"    --ca /etc/olivares/core-ca.pem --client-cert /etc/olivares/collector.crt --client-key /etc/olivares/collector.key",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			log := slog.Default()

			token, err := readToken(tokenFile)
			if err != nil {
				return err
			}
			if insecureTL {
				log.Warn("collector: INSECURE transport — pushing over plaintext; never use off localhost")
			}
			conn, err := dialCore(coreAddr, caFile, clientCert, clientKey, serverName, token, insecureTL)
			if err != nil {
				return fmt.Errorf("dial core %q: %w", coreAddr, err)
			}
			defer func() { _ = conn.Close() }()
			client := olv1.NewIngestServiceClient(conn)

			rt := runtime.New(runtime.Options{
				Logger: log,
				SinkFactory: func(tenant, source string) sdk.Sink {
					return sdkplugin.NewIngestSink(client, tenant, source)
				},
			})

			connectorDir, derr := os.MkdirTemp("", "olivares-collector-")
			if derr != nil {
				return fmt.Errorf("connector scratch dir: %w", derr)
			}
			defer func() { _ = os.RemoveAll(connectorDir) }()

			resetUnsealedSecretConfigs()
			srcCfg, err := loadSourcesConfig(log)
			if err != nil {
				return fmt.Errorf("load sources operator config: %w", err)
			}
			// a sealed sources config that could not be opened is a custody
			// failure — refuse to run an empty collector instead of degrading.
			if scErr := sealedConfigFailure(); scErr != nil {
				return fmt.Errorf("key custody: %w", scErr)
			}
			// advisory — warn once if the sources config carried cleartext
			// secrets unsealed (never fatal; see warnUnsealedSecretConfigs).
			warnUnsealedSecretConfigs(log)
			// a storeless resolver — env/file and the external secret backends
			// (vault + cloud secret managers) resolve; a `store:` reference fails closed
			// (the collector holds no local secret store, which lives in the core).
			collectorResolver := newSecretResolver(nil, os.Getenv, log)
			wireSources(ctx, rt, srcCfg, connectorDir, collectorResolver, log) // warns honestly if no sources are configured

			if err := rt.Start(ctx); err != nil {
				return fmt.Errorf("start collector runtime: %w", err)
			}
			log.Info("collector: started; pushing observations to core", "core", coreAddr)

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			defer signal.Stop(sigCh)
			select {
			case <-sigCh:
				log.Info("collector: shutdown signal received; draining")
			case <-ctx.Done():
				log.Info("collector: context canceled; draining")
			}

			stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			return rt.Stop(stopCtx)
		},
	}
	cmd.Flags().StringVar(&coreAddr, "core-addr", "", "host:port of the remote core's gRPC ingest endpoint (required)")
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "file holding the bearer token of an ingest:write principal (or set OLIVARES_INGEST_TOKEN)")
	cmd.Flags().StringVar(&caFile, "ca", "", "PEM of the CA that signed the core's server certificate (pins a self-signed core cert; empty uses system roots)")
	cmd.Flags().StringVar(&clientCert, "client-cert", "", "collector client certificate PEM (required when the core enforces mutual TLS)")
	cmd.Flags().StringVar(&clientKey, "client-key", "", "collector client private key PEM")
	cmd.Flags().StringVar(&serverName, "server-name", "", "override the core's TLS verification name (when dialing by IP)")
	cmd.Flags().BoolVar(&insecureTL, "insecure", false, "push over plaintext (DANGEROUS; localhost dev only)")
	_ = cmd.MarkFlagRequired("core-addr")
	return cmd
}

// readToken reads the bearer token from path, or from OLIVARES_INGEST_TOKEN when
// path is empty. The token is the collector's authorization to push; an empty
// token leaves the push anonymous and the core's ingest authorize will deny it.
func readToken(path string) (string, error) {
	if path == "" {
		return strings.TrimSpace(os.Getenv("OLIVARES_INGEST_TOKEN")), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// dialCore builds the gRPC client connection to the core's ingest endpoint: TLS
// (with the collector's client cert for mTLS) plus a per-RPC bearer credential, so
// every push carries both the mTLS collector identity and the ingest:write token.
func dialCore(addr, caFile, certFile, keyFile, serverName, token string, insecureTL bool) (*grpc.ClientConn, error) {
	var opts []grpc.DialOption
	if insecureTL {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		tlsCfg, err := secure.ClientTLSConfig(caFile, certFile, keyFile, serverName)
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	}
	if token != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(bearerToken{token: token, requireTLS: !insecureTL}))
	}
	return grpc.NewClient(addr, opts...)
}

// bearerToken attaches "authorization: Bearer <token>" to every RPC, the same
// credential the core's gRPC auth interceptor reads. requireTLS refuses to send the
// token over an insecure transport unless the operator explicitly opted into
// --insecure (dev only).
type bearerToken struct {
	token      string
	requireTLS bool
}

func (b bearerToken) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + b.token}, nil
}

func (b bearerToken) RequireTransportSecurity() bool { return b.requireTLS }
