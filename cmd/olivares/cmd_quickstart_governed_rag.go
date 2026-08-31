// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type quickstartGovernedRAGOptions struct {
	dataDir            string
	outDir             string
	listen             string
	grpcListen         string
	agentGatewayListen string
	start              bool

	source        string
	sourceName    string
	credentialRef string
	bucket        string
	prefix        string
	region        string
	endpoint      string
	pathStyle     bool
	driveID       string
	driveAPIBase  string
	tenantID      string
	kbName        string
	agentRef      string
	agentName     string
	identityRef   string
	clearance     string
	groupRef      string
	mcpIssuer     string
	mcpJWKSURL    string
	mcpJWKSFile   string
	mcpResource   string
	mcpAuthServer string
}

// newQuickstartGovernedRAGCmd writes the operator config for the governed RAG path:
// one live content source, an MCP retrieval resource-server config and an audited
// post-login bootstrap script that creates the KB, ingests the source, binds the
// agent identity and confines the KB to that agent. It does not fake identity
// attributes: clearance/groups must come from the customer's roster/SCIM source, and
// the script says so before it binds the agent.
func newQuickstartGovernedRAGCmd() *cobra.Command {
	opts := quickstartGovernedRAGOptions{
		listen:             "127.0.0.1:8443",
		grpcListen:         "127.0.0.1:8444",
		agentGatewayListen: "127.0.0.1:8446",
		source:             "s3",
		sourceName:         "governed-rag-live",
		region:             "us-east-1",
		kbName:             "governed-data",
		agentRef:           "claude-code-governed",
		agentName:          "Claude Code governed RAG",
		identityRef:        "agent:claude-code-governed",
		clearance:          "confidential",
		groupRef:           "group:engineering",
	}
	cmd := &cobra.Command{
		Use:   "governed-rag",
		Short: "Prepare live governed data for Claude Code (S3/Drive -> semantic KB -> MCP retrieval)",
		Long: "Prepare the governed RAG path for Claude Code. The command writes a live\n" +
			"content-source config (S3 or Google Drive), an MCP retrieval resource-server\n" +
			"config, and a post-login bootstrap script that creates the KB, ingests from the\n" +
			"live source, binds the Claude Code agent identity and scopes the KB to that\n" +
			"agent. It is honest about prerequisites: a semantic embedder must be configured\n" +
			"for model_backed KBs, and identity clearance/groups must come from your roster\n" +
			"or SCIM source.",
		Example: `  # S3 live source, config only
  olivares quickstart governed-rag \
    --tenant-id ten_... \
    --source s3 --bucket prod-runbooks --prefix claude/ \
    --credential-ref store:s3/prod-runbooks-read \
    --mcp-issuer https://idp.example.com/ \
    --mcp-jwks-url https://idp.example.com/.well-known/jwks.json

  # Write config and start the engine with those files
  olivares quickstart governed-rag --start --tenant-id ten_... --bucket prod-runbooks \
    --credential-ref store:s3/prod-runbooks-read --mcp-issuer https://idp.example.com/ \
    --mcp-jwks-url https://idp.example.com/.well-known/jwks.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runQuickstartGovernedRAG(cmd.Context(), cmd.OutOrStdout(), opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.listen, "listen", opts.listen, "HTTP (REST + web console) listen address when --start is used")
	f.StringVar(&opts.grpcListen, "grpc-listen", opts.grpcListen, "gRPC listen address when --start is used")
	f.StringVar(&opts.dataDir, "data-dir", "", "data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	f.StringVar(&opts.outDir, "out-dir", "", "directory for generated governed-RAG config (default <data-dir>/quickstart/governed-rag)")
	f.BoolVar(&opts.start, "start", false, "start the engine after writing config")
	f.StringVar(&opts.agentGatewayListen, "agent-gateway-listen", opts.agentGatewayListen, "MCP gateway listen address")
	f.StringVar(&opts.source, "source", opts.source, "content source kind: s3 or gdrive")
	f.StringVar(&opts.sourceName, "source-name", opts.sourceName, "registered knowledge content-source name")
	f.StringVar(&opts.credentialRef, "credential-ref", "", "secret-store reference for the source credential, e.g. store:s3/prod-read")
	f.StringVar(&opts.bucket, "bucket", "", "S3 bucket for --source s3")
	f.StringVar(&opts.prefix, "prefix", "", "S3 key prefix for --source s3")
	f.StringVar(&opts.region, "region", opts.region, "S3 signing region")
	f.StringVar(&opts.endpoint, "endpoint", "", "optional S3-compatible endpoint (R2/MinIO/GCS interop)")
	f.BoolVar(&opts.pathStyle, "path-style", false, "force S3 path-style bucket addressing")
	f.StringVar(&opts.driveID, "drive-id", "", "shared Drive ID for --source gdrive (optional)")
	f.StringVar(&opts.driveAPIBase, "drive-api-base", "", "Google Drive API base override")
	f.StringVar(&opts.tenantID, "tenant-id", "", "tenant id for the MCP retrieval surface and bootstrap script")
	f.StringVar(&opts.kbName, "kb-name", opts.kbName, "knowledge base name to create in the bootstrap script")
	f.StringVar(&opts.agentRef, "agent-ref", opts.agentRef, "Claude Code agent external_id / MCP token subject")
	f.StringVar(&opts.agentName, "agent-name", opts.agentName, "human label for the agent created by the bootstrap script")
	f.StringVar(&opts.identityRef, "identity-ref", opts.identityRef, "NHI identity external_id to bind to the agent")
	f.StringVar(&opts.clearance, "clearance", opts.clearance, "expected roster clearance on the identity (documented and checked by the guard)")
	f.StringVar(&opts.groupRef, "group-ref", opts.groupRef, "expected roster group/ACL ref on the identity")
	f.StringVar(&opts.mcpIssuer, "mcp-issuer", "", "trusted OAuth issuer for MCP access tokens")
	f.StringVar(&opts.mcpJWKSURL, "mcp-jwks-url", "", "JWKS URL for the MCP issuer")
	f.StringVar(&opts.mcpJWKSFile, "mcp-jwks-file", "", "inline JWKS JSON file for the MCP issuer")
	f.StringVar(&opts.mcpResource, "mcp-resource", "", "MCP protected resource URI (default http://<agent-gateway-listen>/mcp)")
	f.StringVar(&opts.mcpAuthServer, "mcp-authorization-server", "", "authorization server metadata URL (default --mcp-issuer)")
	return cmd
}

func runQuickstartGovernedRAG(ctx context.Context, out io.Writer, opts quickstartGovernedRAGOptions) error {
	if err := opts.validate(); err != nil {
		return err
	}
	// Resolve the data dir ONCE, here at the edge, so the derived out-dir, the
	// generated config and the printed next steps all name the SAME directory —
	// and so the resolution's error surfaces as this command's error.
	resolvedDataDir, err := resolveDataDir(opts.dataDir)
	if err != nil {
		return err
	}
	opts.dataDir = resolvedDataDir
	dir := opts.effectiveOutDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	paths, err := writeGovernedRAGQuickstartFiles(dir, opts)
	if err != nil {
		return err
	}
	printGovernedRAGSummary(out, opts, paths)
	if !opts.start {
		return nil
	}
	if err := opts.validateStartReady(); err != nil {
		return err
	}
	_ = os.Setenv("OLIVARES_SOURCES_CONFIG", paths.sources)
	_ = os.Setenv("OLIVARES_AGENT_GATEWAY_CONFIG", paths.gateway)
	serve := serveOptions{
		listen: opts.listen, grpcListen: opts.grpcListen, dataDir: opts.dataDir,
		engine: "sqlite", checkpointInterval: time.Hour,
	}
	announce := func(ctx context.Context, out io.Writer, eng *engine) error {
		if err := announceQuickstart(ctx, out, eng, consoleURL(opts.listen, false)); err != nil {
			return err
		}
		printGovernedRAGNextSteps(out, opts, paths)
		return nil
	}
	return runEngine(ctx, out, serve, announce)
}

func (o quickstartGovernedRAGOptions) validate() error {
	o.source = strings.ToLower(strings.TrimSpace(o.source))
	if o.source != "s3" && o.source != "gdrive" {
		return fmt.Errorf("--source must be s3 or gdrive")
	}
	if strings.TrimSpace(o.sourceName) == "" {
		return fmt.Errorf("--source-name is required")
	}
	if !strings.HasPrefix(strings.TrimSpace(o.credentialRef), "store:") && !strings.HasPrefix(strings.TrimSpace(o.credentialRef), "db:") {
		return fmt.Errorf("--credential-ref must be a secret-store reference (store:<name> or db:<name>), never an inline credential")
	}
	if o.source == "s3" && strings.TrimSpace(o.bucket) == "" {
		return fmt.Errorf("--bucket is required for --source s3")
	}
	if strings.TrimSpace(o.kbName) == "" || strings.TrimSpace(o.agentRef) == "" || strings.TrimSpace(o.identityRef) == "" {
		return fmt.Errorf("--kb-name, --agent-ref and --identity-ref are required")
	}
	return nil
}

func (o quickstartGovernedRAGOptions) validateStartReady() error {
	if strings.TrimSpace(o.tenantID) == "" {
		return fmt.Errorf("--start needs --tenant-id so the MCP retrieval upstream mounts for the correct tenant")
	}
	if strings.TrimSpace(o.mcpIssuer) == "" {
		return fmt.Errorf("--start needs --mcp-issuer; MCP token issuer validation cannot be skipped")
	}
	if strings.TrimSpace(o.mcpJWKSURL) == "" && strings.TrimSpace(o.mcpJWKSFile) == "" {
		return fmt.Errorf("--start needs --mcp-jwks-url or --mcp-jwks-file; MCP token trust must be anchored")
	}
	return nil
}

func (o quickstartGovernedRAGOptions) effectiveOutDir() string {
	if strings.TrimSpace(o.outDir) != "" {
		return o.outDir
	}
	// o.dataDir is already resolved by runQuickstartGovernedRAG.
	return filepath.Join(o.dataDir, "quickstart", "governed-rag")
}

type governedRAGQuickstartPaths struct {
	sources   string
	gateway   string
	bootstrap string
}

func writeGovernedRAGQuickstartFiles(dir string, opts quickstartGovernedRAGOptions) (governedRAGQuickstartPaths, error) {
	paths := governedRAGQuickstartPaths{
		sources:   filepath.Join(dir, "sources.json"),
		gateway:   filepath.Join(dir, "agent-gateway.json"),
		bootstrap: filepath.Join(dir, "bootstrap-after-login.sh"),
	}
	gatewayCfg, err := governedRAGMCPConfig(opts)
	if err != nil {
		return paths, err
	}
	for _, f := range []struct {
		path  string
		value any
	}{
		{path: paths.sources, value: governedRAGSourcesConfig(opts)},
		{path: paths.gateway, value: gatewayCfg},
	} {
		// render-exempt: this writes CONFIGURATION FILES to disk for the guided
		// walkthrough; the engine reads them back, so their format is a contract
		// with the engine, not a presentation choice for the operator.
		b, err := json.MarshalIndent(f.value, "", "  ")
		if err != nil {
			return paths, err
		}
		if err := os.WriteFile(f.path, append(b, '\n'), 0o600); err != nil {
			return paths, err
		}
	}
	if err := os.WriteFile(paths.bootstrap, []byte(governedRAGBootstrapScript(opts)), 0o700); err != nil {
		return paths, err
	}
	return paths, nil
}

func governedRAGSourcesConfig(opts quickstartGovernedRAGOptions) map[string]any {
	cfg := map[string]any{
		"mode":           "live",
		"credential_ref": opts.credentialRef,
	}
	kind := "s3content"
	if opts.source == "gdrive" {
		kind = "gdrive"
		if opts.driveID != "" {
			cfg["drive_id"] = opts.driveID
		}
		if opts.driveAPIBase != "" {
			cfg["api_base"] = opts.driveAPIBase
		}
	} else {
		cfg["bucket"] = opts.bucket
		if opts.prefix != "" {
			cfg["prefix"] = opts.prefix
		}
		if opts.region != "" {
			cfg["region"] = opts.region
		}
		if opts.endpoint != "" {
			cfg["endpoint"] = opts.endpoint
		}
		if opts.pathStyle {
			cfg["path_style"] = true
		}
	}
	return map[string]any{
		"documents": []map[string]any{{
			"name":   opts.sourceName,
			"kind":   kind,
			"config": cfg,
		}},
	}
}

func governedRAGMCPConfig(opts quickstartGovernedRAGOptions) (map[string]any, error) {
	resource := strings.TrimSpace(opts.mcpResource)
	if resource == "" {
		resource = defaultMCPResource(opts.agentGatewayListen)
	}
	authServer := strings.TrimSpace(opts.mcpAuthServer)
	if authServer == "" {
		authServer = strings.TrimSpace(opts.mcpIssuer)
	}
	if authServer == "" {
		authServer = "<authorization-server-url>"
	}
	issuer := strings.TrimSpace(opts.mcpIssuer)
	if issuer == "" {
		issuer = "<issuer>"
	}
	trust := map[string]any{"issuer": issuer}
	if opts.mcpJWKSURL != "" {
		trust["jwks_url"] = opts.mcpJWKSURL
	}
	if opts.mcpJWKSFile != "" {
		jwks, err := readJSONFileRaw(opts.mcpJWKSFile)
		if err != nil {
			return nil, fmt.Errorf("--mcp-jwks-file: %w", err)
		}
		trust["issuer_jwks"] = jwks
	}
	return map[string]any{
		"listen": opts.agentGatewayListen,
		"mcp": map[string]any{
			"resource":              resource,
			"authorization_servers": []string{authServer},
			"scopes_supported":      []string{"knowledge:retrieval:read"},
			"tenant":                emptyAsPlaceholder(opts.tenantID, "<tenant-id>"),
			"issuers":               []map[string]any{trust},
			"retrieval": map[string]any{
				"enabled": true,
				"scope":   "knowledge:retrieval:read",
			},
			"next_revision_headers": true,
		},
	}, nil
}

func readJSONFileRaw(path string) (json.RawMessage, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("invalid JSON in %s", path)
	}
	return json.RawMessage(b), nil
}

func defaultMCPResource(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://" + listen + "/mcp"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/mcp"
}

func emptyAsPlaceholder(v, placeholder string) string {
	if strings.TrimSpace(v) == "" {
		return placeholder
	}
	return v
}

func printGovernedRAGSummary(out io.Writer, opts quickstartGovernedRAGOptions, paths governedRAGQuickstartPaths) {
	_, semantic, reason := resolveEmbeddingsProvider(os.Getenv)
	fmt.Fprintf(out, "\nGoverned RAG quickstart files written:\n"+
		"  sources:        %s\n"+
		"  agent gateway:  %s\n"+
		"  bootstrap:      %s\n\n"+
		"Configured content source %q as mode=live (%s) with credential_ref=%s.\n",
		paths.sources, paths.gateway, paths.bootstrap, opts.sourceName, opts.source, opts.credentialRef)
	if semantic {
		fmt.Fprintln(out, "Semantic retrieval: configured (retrieval_semantic=true).")
	} else {
		fmt.Fprintf(out, "WARNING: semantic retrieval is not configured (retrieval_semantic=false, reason=%s).\n", reason)
		fmt.Fprintln(out, "Set OLIVARES_EMBEDDINGS_* before creating the model_backed KB, or the KB create/ingest will refuse.")
	}
	printGovernedRAGNextSteps(out, opts, paths)
}

func printGovernedRAGNextSteps(out io.Writer, opts quickstartGovernedRAGOptions, paths governedRAGQuickstartPaths) {
	fmt.Fprintf(out, "\nNext steps:\n"+
		"  1. Store the live source credential, for example:\n"+
		"       olivares secrets put --data-dir %s --name %s --value-file /run/secrets/%s\n"+
		"  2. Start with the generated config:\n"+
		"       OLIVARES_SOURCES_CONFIG=%s OLIVARES_AGENT_GATEWAY_CONFIG=%s olivares quickstart --data-dir %s\n"+
		"  3. After setup/login, run:\n"+
		"       OLIVARES_TOKEN=<admin-token> OLIVARES_TENANT=%s %s\n"+
		"  4. Point Claude Code at the MCP endpoint:\n"+
		"       %s\n\n",
		opts.dataDir, secretName(opts.credentialRef), secretName(opts.credentialRef),
		paths.sources, paths.gateway, opts.dataDir,
		emptyAsPlaceholder(opts.tenantID, "<tenant-id>"), paths.bootstrap, defaultMCPResource(opts.agentGatewayListen))
	fmt.Fprintf(out, "Identity prerequisite: %s must be rostered with attr_clearance=%s and membership %s.\n"+
		"Until that is true, governed retrieval stays deny-closed/public-only.\n\n", opts.identityRef, opts.clearance, opts.groupRef)
}

func secretName(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "store:")
	ref = strings.TrimPrefix(ref, "db:")
	if ref == "" {
		return "source/read"
	}
	return ref
}

func governedRAGBootstrapScript(opts quickstartGovernedRAGOptions) string {
	tenant := emptyAsPlaceholder(opts.tenantID, "<tenant-id>")
	base := strings.TrimRight(consoleURL(opts.listen, false), "/")
	return fmt.Sprintf(`#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
set -euo pipefail

: "${OLIVARES_BASE_URL:=%s}"
: "${OLIVARES_TOKEN:?set OLIVARES_TOKEN to an admin/editor token}"
: "${OLIVARES_TENANT:=%s}"

hdr=(-H "Authorization: Bearer ${OLIVARES_TOKEN}" -H "X-Olivares-Tenant: ${OLIVARES_TENANT}" -H "Content-Type: application/json")
curl_json() { curl -skS "$@" ; }

echo "Checking semantic retrieval status..."
status="$(curl_json "${OLIVARES_BASE_URL}/status")"
semantic="$(printf '%%s' "$status" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("retrieval_semantic"))')"
if [ "$semantic" != "True" ] && [ "$semantic" != "true" ]; then
  echo "FAIL: retrieval_semantic is not true. Configure OLIVARES_EMBEDDINGS_* before creating the model_backed KB." >&2
  exit 1
fi

echo "Creating semantic KB %q..."
kb="$(curl_json -X POST "${OLIVARES_BASE_URL}/v1/m/knowledge/kbs" "${hdr[@]}" \
  -d %q)"
KB_ID="$(printf '%%s' "$kb" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
export KB_ID

echo "Ingesting live source %q into ${KB_ID}..."
curl_json -X POST "${OLIVARES_BASE_URL}/v1/m/knowledge/kbs/${KB_ID}/ingest" "${hdr[@]}" \
  -d %q >/dev/null

echo "Creating/updating agent %q..."
agent_list="$(curl_json "${OLIVARES_BASE_URL}/v1/agents?limit=200" "${hdr[@]}")"
AGENT_ID="$(printf '%%s' "$agent_list" | python3 -c 'import json,sys; ext=%q; data=json.load(sys.stdin); print(next((a["id"] for a in data.get("items",[]) if a.get("external_id")==ext), ""))')"
if [ -z "$AGENT_ID" ]; then
  agent="$(curl_json -X POST "${OLIVARES_BASE_URL}/v1/agents" "${hdr[@]}" \
    -d %q)"
  AGENT_ID="$(printf '%%s' "$agent" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
fi

echo "Binding agent to identity %q..."
curl_json -X POST "${OLIVARES_BASE_URL}/v1/m/governance/agents/${AGENT_ID}/identity" "${hdr[@]}" \
  -d %q >/dev/null

echo "Constraining KB to agent scope..."
curl_json -X POST "${OLIVARES_BASE_URL}/v1/m/sourcescope/bindings" "${hdr[@]}" \
  -d "$(python3 -c 'import json,os; print(json.dumps({"source_type":"knowledge","source_ref":os.environ["KB_ID"],"scope_tree":"agent","scope_ref":%q,"enabled":True,"note":"quickstart governed RAG"}))')" >/dev/null

cat <<EOF
Governed RAG bootstrap complete.
  KB_ID=${KB_ID}
  source_mode=live
  agent_ref=%s
  identity_ref=%s

The identity must be present in your roster/SCIM source with:
  attr_clearance=%s
  membership=%s

Claude Code MCP endpoint: %s
EOF
`, base, tenant,
		opts.kbName,
		jsonLiteral(map[string]any{"name": opts.kbName, "classification": "public", "embed_policy": "model_backed"}),
		opts.sourceName,
		jsonLiteral(map[string]any{"source": opts.sourceName}),
		opts.agentRef,
		opts.agentRef,
		jsonLiteral(map[string]any{"name": opts.agentName, "kind": "claude_code", "external_id": opts.agentRef, "status": "active"}),
		opts.identityRef,
		jsonLiteral(map[string]any{"identity_ref": opts.identityRef, "allow_unknown": true}),
		opts.agentRef,
		opts.agentRef, opts.identityRef, opts.clearance, opts.groupRef, defaultMCPResource(opts.agentGatewayListen))
}

func jsonLiteral(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
