// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/model"
)

const protocolBindingAPIBase = workAPIBase

type protocolBindingMutationFlags struct {
	mode           string
	file           string
	planHash       string
	idempotencyKey string
	version        uint64
}

type protocolBindingListFlags struct {
	workspaceID   string
	bindingKey    string
	generation    int64
	protocol      string
	direction     string
	localKind     string
	peerAuthority string
	state         string
	bindingSpecID string
	workItemID    string
	ownerKind     string
	ownerRef      string
	externalKind  string
	externalID    string
	verdict       string
	terminal      string
	limit         int
	cursor        string
}

func newProtocolBindingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "protocol-binding",
		Short: "Compose and reconcile durable A2A and MCP protocol bindings",
		Long: "protocol-binding manages immutable protocol mapping generations and their durable runtime " +
			"bindings. All mutations use the control-plane validate, plan, test, and apply phases.",
		Example: "  olivares work protocol-binding spec list --workspace-id 0195f1a7-8b6c-7d2e-9f10-112233445566\n" +
			"  olivares work protocol-binding binding list --workspace-id 0195f1a7-8b6c-7d2e-9f10-112233445566",
	}
	cmd.AddCommand(newProtocolBindingSpecCmd(), newProtocolBindingInstanceCmd())
	return cmd
}

func newProtocolBindingSpecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spec",
		Short: "Manage immutable protocol binding specifications",
		Long: "spec validates, plans, creates, activates, disables, and reads immutable protocol " +
			"binding specification generations.",
		Example: "  olivares work protocol-binding spec list --workspace-id 0195f1a7-8b6c-7d2e-9f10-112233445566\n" +
			"  olivares work protocol-binding spec get 0195f1a7-8b6c-7d2e-9f10-112233445577",
	}
	cmd.AddCommand(
		newProtocolBindingSpecCreateCmd(),
		newProtocolBindingSpecListCmd(),
		newProtocolBindingSpecGetCmd(),
		newProtocolBindingSpecStateCmd("activate"),
		newProtocolBindingSpecStateCmd("disable"),
	)
	return cmd
}

func newProtocolBindingInstanceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "binding",
		Short: "Inspect and reconcile durable protocol bindings",
		Long: "binding reads durable protocol binding generations and reconciles one exact remote " +
			"observation through the governed mutation phases.",
		Example: "  olivares work protocol-binding binding list --workspace-id 0195f1a7-8b6c-7d2e-9f10-112233445566\n" +
			"  olivares work protocol-binding binding get 0195f1a7-8b6c-7d2e-9f10-112233445588",
	}
	cmd.AddCommand(
		newProtocolBindingListCmd(),
		newProtocolBindingGetCmd(),
		newProtocolBindingReconcileCmd(),
	)
	return cmd
}

func newProtocolBindingSpecCreateCmd() *cobra.Command {
	var cfg agentClientConfig
	var flags protocolBindingMutationFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Validate, plan, or create one draft protocol binding spec",
		Long: "create validates or plans a specification document, then creates its immutable draft " +
			"generation when apply is requested with the pinned plan and idempotency key.",
		Example: "  olivares work protocol-binding spec create -f binding.yaml --mode plan\n" +
			"  olivares work protocol-binding spec create -f binding.yaml --mode apply --plan-hash <sha256> " +
			"--idempotency-key 0195f1a7-8b6c-7d2e-9f10-112233445599",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(flags.file) == "" {
				return workUsagef("spec create requires -f with one YAML or JSON specification")
			}
			body, err := readProtocolBindingDocument(cmd, flags.file)
			if err != nil {
				return err
			}
			return runProtocolBindingMutation(
				cmd, &cfg, http.MethodPost, protocolBindingAPIBase+"/protocol-binding-specs",
				flags, false, body,
			)
		},
	}
	cfg.addFlags(cmd)
	addProtocolBindingMutationFlags(cmd, &flags, false, true)
	return cmd
}

func newProtocolBindingSpecStateCmd(action string) *cobra.Command {
	var cfg agentClientConfig
	var flags protocolBindingMutationFlags
	cmd := &cobra.Command{
		Use:   action + " <spec-id>",
		Short: strings.ToUpper(action[:1]) + action[1:] + " one protocol binding spec generation",
		Long: "The " + action + " command validates or plans the requested state transition, then " +
			"applies it only with the expected version, pinned plan, and idempotency key.",
		Example: "  olivares work protocol-binding spec " + action +
			" 0195f1a7-8b6c-7d2e-9f10-112233445577 --mode plan --version 3",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := canonicalProtocolBindingID(args[0], "spec-id")
			if err != nil {
				return err
			}
			return runProtocolBindingMutation(
				cmd, &cfg, http.MethodPost,
				protocolBindingAPIBase+"/protocol-binding-specs/"+url.PathEscape(id)+"/"+action,
				flags, true, map[string]any{},
			)
		},
	}
	cfg.addFlags(cmd)
	addProtocolBindingMutationFlags(cmd, &flags, true, false)
	return cmd
}

func newProtocolBindingSpecGetCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:   "get <spec-id>",
		Short: "Get one immutable protocol binding spec generation",
		Long:  "get returns one tenant-confined immutable protocol binding specification generation.",
		Example: "  olivares work protocol-binding spec get " +
			"0195f1a7-8b6c-7d2e-9f10-112233445577 -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := canonicalProtocolBindingID(args[0], "spec-id")
			if err != nil {
				return err
			}
			return runProtocolBindingRead(
				cmd, &cfg, protocolBindingAPIBase+"/protocol-binding-specs/"+url.PathEscape(id),
			)
		},
	}
	cfg.addFlags(cmd)
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newProtocolBindingSpecListCmd() *cobra.Command {
	var cfg agentClientConfig
	var flags protocolBindingListFlags
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List protocol binding spec generations in one workspace",
		Long: "list returns a cursor-paginated, tenant-confined collection of protocol binding " +
			"specification generations filtered by the supplied exact selectors.",
		Example: "  olivares work protocol-binding spec list " +
			"--workspace-id 0195f1a7-8b6c-7d2e-9f10-112233445566 --state active -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query, err := protocolBindingSpecListQuery(flags)
			if err != nil {
				return err
			}
			return runProtocolBindingRead(
				cmd, &cfg, protocolBindingAPIBase+"/protocol-binding-specs?"+query.Encode(),
			)
		},
	}
	cfg.addFlags(cmd)
	addProtocolBindingSpecListFlags(cmd, &flags)
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newProtocolBindingGetCmd() *cobra.Command {
	var cfg agentClientConfig
	cmd := &cobra.Command{
		Use:   "get <binding-id>",
		Short: "Get one durable protocol binding generation",
		Long:  "get returns one tenant-confined durable protocol binding generation and its pinned lineage.",
		Example: "  olivares work protocol-binding binding get " +
			"0195f1a7-8b6c-7d2e-9f10-112233445588 -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := canonicalProtocolBindingID(args[0], "binding-id")
			if err != nil {
				return err
			}
			return runProtocolBindingRead(
				cmd, &cfg, protocolBindingAPIBase+"/protocol-bindings/"+url.PathEscape(id),
			)
		},
	}
	cfg.addFlags(cmd)
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newProtocolBindingListCmd() *cobra.Command {
	var cfg agentClientConfig
	var flags protocolBindingListFlags
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List durable protocol bindings in one workspace",
		Long: "list returns a cursor-paginated, tenant-confined collection of durable protocol " +
			"binding generations filtered by the supplied exact selectors.",
		Example: "  olivares work protocol-binding binding list " +
			"--workspace-id 0195f1a7-8b6c-7d2e-9f10-112233445566 --protocol a2a -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query, err := protocolBindingListQuery(flags)
			if err != nil {
				return err
			}
			return runProtocolBindingRead(
				cmd, &cfg, protocolBindingAPIBase+"/protocol-bindings?"+query.Encode(),
			)
		},
	}
	cfg.addFlags(cmd)
	addProtocolBindingListFlags(cmd, &flags)
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func newProtocolBindingReconcileCmd() *cobra.Command {
	var cfg agentClientConfig
	var flags protocolBindingMutationFlags
	cmd := &cobra.Command{
		Use:   "reconcile <binding-id>",
		Short: "Validate, plan, test, or apply one exact-generation remote observation",
		Long: "reconcile obtains a fresh remote observation for one exact binding generation and " +
			"runs it through validate, plan, test, or apply without accepting caller-supplied evidence.",
		Example: "  olivares work protocol-binding binding reconcile " +
			"0195f1a7-8b6c-7d2e-9f10-112233445588 --mode test --version 4",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := canonicalProtocolBindingID(args[0], "binding-id")
			if err != nil {
				return err
			}
			return runProtocolBindingMutation(
				cmd, &cfg, http.MethodPost,
				protocolBindingAPIBase+"/protocol-bindings/"+url.PathEscape(id)+"/reconcile",
				flags, true, map[string]any{},
			)
		},
	}
	cfg.addFlags(cmd)
	addProtocolBindingMutationFlags(cmd, &flags, true, false)
	cmd.Flags().Lookup("mode").DefValue = "test"
	flags.mode = "test"
	return cmd
}

func addProtocolBindingMutationFlags(
	cmd *cobra.Command,
	flags *protocolBindingMutationFlags,
	withVersion bool,
	withFile bool,
) {
	cmd.Flags().StringVar(&flags.mode, "mode", "plan", "operation phase: validate, plan, test, or apply")
	if withFile {
		cmd.Flags().StringVarP(&flags.file, "file", "f", "", "YAML or JSON protocol binding spec ('-' reads stdin)")
	}
	cmd.Flags().StringVar(&flags.planHash, "plan-hash", "", "SHA-256 plan hash required by apply")
	cmd.Flags().StringVar(&flags.idempotencyKey, "idempotency-key", "", "UUID reused for an exact apply retry")
	if withVersion {
		cmd.Flags().Uint64Var(&flags.version, "version", 0, "expected resource version N")
	}
	addDeprecatedJSONFlag(cmd)
}

func runProtocolBindingMutation(
	cmd *cobra.Command,
	cfg *agentClientConfig,
	method string,
	path string,
	flags protocolBindingMutationFlags,
	versioned bool,
	body map[string]any,
) error {
	mode := strings.ToLower(strings.TrimSpace(flags.mode))
	validMode := mode == "validate" || mode == "plan" || mode == "apply"
	if strings.Contains(path, "/reconcile") {
		validMode = validMode || mode == "test"
	}
	if !validMode {
		return workUsagef("--mode is not valid for this protocol binding operation")
	}
	headers := make(http.Header)
	if strings.TrimSpace(flags.planHash) != "" {
		planHash, err := canonicalProtocolBindingPlanHash(flags.planHash)
		if err != nil {
			return err
		}
		headers.Set("If-Plan-Hash", planHash)
	}
	if flags.version > 0 {
		headers.Set("If-Match", fmt.Sprintf("\"v%d\"", flags.version))
	}
	if mode == "apply" {
		if headers.Get("If-Plan-Hash") == "" {
			return workUsagef("protocol binding apply requires --plan-hash")
		}
		if versioned && flags.version == 0 {
			return workUsagef("protocol binding apply requires --version")
		}
		key := strings.TrimSpace(flags.idempotencyKey)
		if key == "" {
			key = model.NewID().String()
			fmt.Fprintf(cmd.ErrOrStderr(), "idempotency key: %s (reuse this key for an ambiguous retry)\n", key)
		}
		parsed, err := model.ParseID(key)
		if err != nil || parsed.IsZero() || parsed.String() != key {
			return workUsagef("--idempotency-key must be a canonical UUID")
		}
		headers.Set("Idempotency-Key", key)
	}
	if err := cfg.resolve(); err != nil {
		return err
	}
	query := url.Values{"mode": []string{mode}}
	resp, err := workDo(cmd.Context(), cfg, method, path+"?"+query.Encode(), body, headers, false)
	if err != nil {
		return err
	}
	if resp.status < http.StatusOK || resp.status >= http.StatusMultipleChoices {
		return workHTTPError(resp.status, resp.body)
	}
	return renderProtocolBindingResponse(cmd, resp.body)
}

func runProtocolBindingRead(cmd *cobra.Command, cfg *agentClientConfig, path string) error {
	if err := cfg.resolve(); err != nil {
		return err
	}
	resp, err := workDo(cmd.Context(), cfg, http.MethodGet, path, nil, nil, false)
	if err != nil {
		return err
	}
	if resp.status != http.StatusOK {
		return workHTTPError(resp.status, resp.body)
	}
	return renderProtocolBindingResponse(cmd, resp.body)
}

func renderProtocolBindingResponse(cmd *cobra.Command, body []byte) error {
	var value any
	if len(bytes.TrimSpace(body)) == 0 {
		value = map[string]any{}
	} else if err := decodeWorkJSON(body, &value); err != nil {
		return fmt.Errorf("decode protocol binding response: %w", err)
	}
	// -o text imprimia JSON. La rama de TEXTO de `renderOut` codificaba a JSON dentro de si
	// misma, asi que el operador que pedia texto recibia JSON igual. Es el mismo defecto que
	// 41382bdd0 arreglo para `keys status`, `license status` y `license verify`, y que
	// `render_coverage_test.go:23-30` declara «falso en las DOS direcciones».
	//
	// `renderReportOut` ya lo resuelve y conserva el comportamiento por defecto: sin flag, JSON
	// —que es lo que este comando imprime hoy y lo que sus tests esperan—; con `-o text`, las
	// mismas claves como lineas alineadas. Se reutiliza en vez de escribir una segunda forma a
	// mano, que es como las dos acaban divergiendo.
	//
	// NOTA SOBRE EL COMENTARIO, no sobre el codigo: la version anterior de estas lineas nombraba
	// la funcion de codificacion, y `render_coverage_test.go` la ACUSO — su heuristica es textual,
	// asi que un comentario que describe el defecto se lee como el defecto. Queda dicho porque el
	// que reescriba esto volvera a tropezar, y porque es un dato para quien reconstruya ese gate.
	//
	// ⛔ CONFLICTO CON `main` RESUELTO A FAVOR DE ESTE LADO, y no por llegar antes.
	//
	// `0d3ecaf9d` puso aqui una exencion con el argumento contrario: «the response is a
	// server-defined object decoded into `any` … There is no schema here to lay out in columns,
	// so indented JSON IS the text form — Deliberate, not the defect». Las dos afirmaciones
	// no pueden ser ciertas.
	//
	// Lo decide una medida, no la antiguedad: **`renderStatusOut` NO necesita esquema.**
	// `writeStatusLines` (`render.go:266-269`) aplana CUALQUIER JSON decodificado en lineas
	// `path\tvalue`, con los mapas en orden de clave. Asi que «no hay esquema que poner en
	// columnas» describe la herramienta como menos capaz de lo que es — y el testigo
	// `cmd_protocol_binding_text_test.go` lo comprueba justo ahi: con `-o text` el objeto
	// ANIDADO se aplana, que es lo que aquella exencion daba por imposible.
	//
	// Y el mutante cierra el argumento: restaurando la conducta de `main`, ese testigo se pone
	// rojo diciendo «-o text devolvio JSON». Una exencion habria dejado el defecto en pie con la
	// bendicion de un comentario, que es la unica forma en que un gate correcto certifica algo
	// que no cumple.
	if err := renderReportOut(cmd, value); err != nil {
		return err
	}
	object, _ := value.(map[string]any)
	verdict, _ := object["verdict"].(string)
	switch verdict {
	case "", "CLEAN", "LIMPIO":
		return nil
	case "BROKEN", "ROTO":
		return exitcode.New(exitcode.Degraded, nil)
	case "UNKNOWN", "NO_HE_PODIDO_MIRAR":
		return exitcode.New(exitcode.Indeterminate, nil)
	default:
		return fmt.Errorf("protocol binding response carries unknown verdict %q", verdict)
	}
}

func readProtocolBindingDocument(cmd *cobra.Command, path string) (map[string]any, error) {
	raw, err := readWorkInput(cmd, path)
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		return nil, workUsagef("parse protocol binding spec %s: %v", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return nil, workUsagef("parse protocol binding spec %s: multiple documents are not allowed", path)
	} else if !errors.Is(err, io.EOF) {
		return nil, workUsagef("parse protocol binding spec %s: %v", path, err)
	}
	if body == nil {
		return nil, workUsagef("protocol binding spec %s must contain one object", path)
	}
	return body, nil
}

func canonicalProtocolBindingID(raw, name string) (string, error) {
	raw = strings.TrimSpace(raw)
	id, err := model.ParseID(raw)
	if err != nil || id.IsZero() || id.String() != raw {
		return "", workUsagef("%s must be a canonical UUID", name)
	}
	return raw, nil
}

func canonicalProtocolBindingPlanHash(raw string) (string, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(raw), "sha256:"))
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != 32 || len(raw) != 64 {
		return "", workUsagef("--plan-hash must be a 64-character SHA-256 hex digest")
	}
	return raw, nil
}

func addProtocolBindingSpecListFlags(cmd *cobra.Command, flags *protocolBindingListFlags) {
	addProtocolBindingBaseListFlags(cmd, flags)
	cmd.Flags().StringVar(&flags.bindingKey, "binding-key", "", "stable binding specification key")
	cmd.Flags().Int64Var(&flags.generation, "generation", 0, "exact specification generation")
	cmd.Flags().StringVar(&flags.direction, "direction", "", "binding direction")
	cmd.Flags().StringVar(&flags.localKind, "local-kind", "", "local resource kind")
	cmd.Flags().StringVar(&flags.state, "state", "", "specification state")
}

func addProtocolBindingListFlags(cmd *cobra.Command, flags *protocolBindingListFlags) {
	addProtocolBindingBaseListFlags(cmd, flags)
	cmd.Flags().StringVar(&flags.bindingSpecID, "binding-spec-id", "", "exact binding specification UUID")
	cmd.Flags().StringVar(&flags.workItemID, "work-item-id", "", "exact work item UUID")
	cmd.Flags().StringVar(&flags.ownerKind, "owner-kind", "", "binding owner kind")
	cmd.Flags().StringVar(&flags.ownerRef, "owner-ref", "", "binding owner reference")
	cmd.Flags().StringVar(&flags.externalKind, "external-kind", "", "remote resource kind")
	cmd.Flags().StringVar(&flags.externalID, "external-id", "", "remote resource ID")
	cmd.Flags().StringVar(&flags.verdict, "verdict", "", "observation verdict")
	cmd.Flags().StringVar(&flags.terminal, "terminal", "", "terminal filter: true or false")
}

func addProtocolBindingBaseListFlags(cmd *cobra.Command, flags *protocolBindingListFlags) {
	cmd.Flags().StringVar(&flags.workspaceID, "workspace-id", "", "workspace UUID (optional for a confined principal)")
	cmd.Flags().StringVar(&flags.protocol, "protocol", "", "protocol: a2a or mcp")
	cmd.Flags().StringVar(&flags.peerAuthority, "peer-authority", "", "canonical peer authority")
	cmd.Flags().IntVar(&flags.limit, "limit", 0, "page size")
	cmd.Flags().StringVar(&flags.cursor, "cursor", "", "opaque keyset cursor")
}

func protocolBindingSpecListQuery(flags protocolBindingListFlags) (url.Values, error) {
	query, err := protocolBindingBaseListQuery(flags)
	if err != nil {
		return nil, err
	}
	addQueryValue(query, "binding_key", flags.bindingKey)
	if flags.generation < 0 {
		return nil, workUsagef("--generation must be greater than zero")
	}
	if flags.generation > 0 {
		query.Set("generation", strconv.FormatInt(flags.generation, 10))
	}
	addQueryValue(query, "direction", flags.direction)
	addQueryValue(query, "local_kind", flags.localKind)
	addQueryValue(query, "state", flags.state)
	return query, nil
}

func protocolBindingListQuery(flags protocolBindingListFlags) (url.Values, error) {
	query, err := protocolBindingBaseListQuery(flags)
	if err != nil {
		return nil, err
	}
	for name, raw := range map[string]string{
		"binding_spec_id": flags.bindingSpecID,
		"work_item_id":    flags.workItemID,
	} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		id, idErr := canonicalProtocolBindingID(raw, "--"+strings.ReplaceAll(name, "_", "-"))
		if idErr != nil {
			return nil, idErr
		}
		query.Set(name, id)
	}
	addQueryValue(query, "owner_kind", flags.ownerKind)
	addQueryValue(query, "owner_ref", flags.ownerRef)
	addQueryValue(query, "external_kind", flags.externalKind)
	addQueryValue(query, "external_id", flags.externalID)
	addQueryValue(query, "verdict", flags.verdict)
	if value := strings.TrimSpace(flags.terminal); value != "" {
		parsed, parseErr := strconv.ParseBool(value)
		if parseErr != nil {
			return nil, workUsagef("--terminal must be true or false")
		}
		query.Set("terminal", strconv.FormatBool(parsed))
	}
	return query, nil
}

func protocolBindingBaseListQuery(flags protocolBindingListFlags) (url.Values, error) {
	query := make(url.Values)
	if raw := strings.TrimSpace(flags.workspaceID); raw != "" {
		id, err := canonicalProtocolBindingID(raw, "--workspace-id")
		if err != nil {
			return nil, err
		}
		query.Set("workspace_id", id)
	}
	if flags.limit < 0 || flags.limit > 500 {
		return nil, workUsagef("--limit must be between 0 and 500")
	}
	if flags.limit > 0 {
		query.Set("limit", strconv.Itoa(flags.limit))
	}
	addQueryValue(query, "protocol", flags.protocol)
	addQueryValue(query, "peer_authority", flags.peerAuthority)
	addQueryValue(query, "cursor", flags.cursor)
	return query, nil
}

func addQueryValue(query url.Values, name, value string) {
	if value = strings.TrimSpace(value); value != "" {
		query.Set(name, value)
	}
}
