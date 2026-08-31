// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// ---------------------------------------------------------------------------
// Request bodies. Each mirrors the module's own decoder EXACTLY, because every
// module decodes with DisallowUnknownFields (modules/knowledge/helpers.go): a
// field this CLI invents is answered 400, not ignored.
// ---------------------------------------------------------------------------

type knowledgeKBBody struct {
	Name            string   `json:"name"`
	Classification  string   `json:"classification,omitempty"`
	ResidencyRegion string   `json:"residency_region,omitempty"`
	EmbedPolicy     string   `json:"embed_policy,omitempty"`
	DefaultACL      []string `json:"default_acl,omitempty"`
	Status          string   `json:"status,omitempty"`
}

type knowledgeIngestBody struct {
	Source    string          `json:"source,omitempty"`
	Documents json.RawMessage `json:"documents,omitempty"`
}

type knowledgeSyncBody struct {
	Source string `json:"source"`
}

type knowledgeQueryBody struct {
	Query      string `json:"query"`
	TopK       int    `json:"top_k,omitempty"`
	SessionRef string `json:"session_ref,omitempty"`
}

type knowledgePromptBody struct {
	Name     string `json:"name"`
	Template string `json:"template"`
	Label    string `json:"label,omitempty"`
	Note     string `json:"note,omitempty"`
}

type knowledgeRevisionBody struct {
	Template string `json:"template"`
	Label    string `json:"label,omitempty"`
	Note     string `json:"note,omitempty"`
}

type knowledgeRollbackBody struct {
	Rev int64 `json:"rev"`
}

type knowledgeMemoryBody struct {
	AgentRef       string  `json:"agent_ref"`
	Key            string  `json:"key"`
	Content        string  `json:"content"`
	Classification string  `json:"classification,omitempty"`
	Residency      string  `json:"residency_region,omitempty"`
	TTLSeconds     int64   `json:"ttl_seconds,omitempty"`
	UserRef        *string `json:"user_ref,omitempty"`
	SessionRef     *string `json:"session_ref,omitempty"`
}

type knowledgeContextPolicyBody struct {
	ScopeKind         string          `json:"scope_kind"`
	ScopeRef          string          `json:"scope_ref"`
	MaxTokens         int64           `json:"max_tokens,omitempty"`
	Strategy          string          `json:"strategy,omitempty"`
	RedactionRequired bool            `json:"redaction_required,omitempty"`
	Spec              json.RawMessage `json:"spec,omitempty"`
	Effect            string          `json:"effect,omitempty"`
}

type knowledgeDLPBody struct {
	Class  string `json:"class"`
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
}

// knowledgeDataProductBody is the ONE body in this family that is a genuine
// PATCH: every field is a pointer with omitempty, and the module applies only
// what arrived (modules/knowledge/dataproduct.go:60-71). So `data-products set`
// carries no --replace guard — there is nothing to reset.
type knowledgeDataProductBody struct {
	Name                *string         `json:"name,omitempty"`
	Description         *string         `json:"description,omitempty"`
	OwnerRef            *string         `json:"owner_ref,omitempty"`
	KBRef               *string         `json:"kb_ref,omitempty"`
	KBID                *string         `json:"kb_id,omitempty"`
	Tags                json.RawMessage `json:"tags,omitempty"`
	FreshnessSLASeconds *int64          `json:"freshness_sla_seconds,omitempty"`
	AvailabilityTarget  *string         `json:"availability_target,omitempty"`
	EnforcementMode     *string         `json:"enforcement_mode,omitempty"`
	QualityScore        *int64          `json:"quality_score,omitempty"`
}

type knowledgeContractBody struct {
	SchemaDefinition         json.RawMessage `json:"schema_definition,omitempty"`
	ValidationMode           string          `json:"validation_mode,omitempty"`
	CompletenessThreshold    int64           `json:"completeness_threshold,omitempty"`
	FreshnessOverrideSeconds int64           `json:"freshness_override_seconds,omitempty"`
	Note                     string          `json:"note,omitempty"`
}

type knowledgeValidateBody struct {
	Payload  json.RawMessage `json:"payload,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// ---------------------------------------------------------------------------
// The command tree
// ---------------------------------------------------------------------------

func newKnowledgeCmd() *cobra.Command {
	flags := &authClientFlags{}
	cmd := &cobra.Command{
		Use:   "knowledge",
		Short: "Govern knowledge bases, data products, memory and DLP",
		Long: "Govern the tenant's knowledge plane: knowledge bases and their documents,\n" +
			"governed retrieval and its lineage, the prompt registry, agent memory, context\n" +
			"policies, PII discovery, the DLP egress rules and the data-product catalog.\n\n" +
			"Connection, credential and TLS values use the same resolution order and trust\n" +
			"controls as `auth`. These routes are served by the knowledge module: an engine\n" +
			"built without it answers 404 for the whole namespace, and this command says so.",
		Example: `  olivares knowledge kbs ls
  olivares knowledge --server https://plane.example.com --tenant tenant-a kbs ls`,
		Args: cobra.NoArgs,
	}
	flags.addPersistent(cmd)
	client := datalaneClient{flags: flags, base: knowledgeAPIBase, what: "knowledge"}
	cmd.AddCommand(
		newKnowledgeKBsCmd(client),
		newKnowledgeDocumentsCmd(client),
		newKnowledgeLineageCmd(client),
		newKnowledgePromptsCmd(client),
		newKnowledgeMemoryCmd(client),
		newKnowledgeContextPoliciesCmd(client),
		newKnowledgeLabelsCmd(client),
		newKnowledgeScansCmd(client),
		newKnowledgeDLPCmd(client),
		newKnowledgeSourcesCmd(client),
		newKnowledgeDataProductsCmd(client),
	)
	return cmd
}

// --- knowledge bases -------------------------------------------------------

func newKnowledgeKBsCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kbs",
		Short: "Declare, inspect and operate knowledge bases",
		Long: "A knowledge base is the governed collection: its classification, residency\n" +
			"region, embed policy and default ACL are the perimeter every ingest and every\n" +
			"retrieval is checked against.",
		Example: `  olivares knowledge kbs ls
  olivares knowledge kbs get kb_123`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		newKnowledgeKBsListCmd(client), newKnowledgeKBsGetCmd(client),
		newKnowledgeKBsCreateCmd(client), newKnowledgeKBsSetCmd(client),
		newKnowledgeKBsRemoveCmd(client), newKnowledgeKBsIngestCmd(client),
		newKnowledgeKBsReindexCmd(client), newKnowledgeKBsSyncCmd(client),
		newKnowledgeKBsDocumentsCmd(client), newKnowledgeKBsQueryCmd(client),
		newKnowledgeKBsScanCmd(client),
	)
	return cmd
}

func newKnowledgeKBsListCmd(client datalaneClient) *cobra.Command {
	var (
		page   datalanePageFlags
		status string
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the tenant's knowledge bases",
		Long: datalaneListLong("List every knowledge base visible to the caller, with its classification,\n" +
			"residency region, embed policy and document counts."),
		Example: `  olivares knowledge kbs ls
  olivares knowledge kbs ls --status active --limit 50 -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query, err := page.values(cmd, datalaneFilter(nil, "status", status))
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/kbs", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no knowledge bases visible to this caller",
				datalaneCols("ID", "id", "NAME", "name", "CLASSIFICATION", "classification",
					"RESIDENCY", "residency_region", "EMBED", "embed_policy", "STATUS", "status",
					"DOCS", "doc_count", "CHUNKS", "chunk_count"))
		},
	}
	page.add(cmd)
	cmd.Flags().StringVar(&status, "status", "", "only knowledge bases in this status")
	return cmd
}

func newKnowledgeKBsGetCmd(client datalaneClient) *cobra.Command {
	return &cobra.Command{
		Use:   "get <kb-id>",
		Short: "Show one knowledge base",
		Long: "Show one knowledge base, including the embed model actually wired — the field\n" +
			"that says whether its vectors are semantic or the local-hash fallback.",
		Example: "  olivares knowledge kbs get kb_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("kbs", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
}

// knowledgeKBFlags are the authored fields of a KB, shared by create and set.
type knowledgeKBFlags struct {
	name           string
	classification string
	residency      string
	embedPolicy    string
	acl            []string
	status         string
}

func (f *knowledgeKBFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.name, "name", "", "knowledge base name")
	cmd.Flags().StringVar(&f.classification, "classification", "",
		"public, internal, confidential or secret (server default: internal)")
	cmd.Flags().StringVar(&f.residency, "residency-region", "",
		"region the corpus is pinned to (server default: global)")
	cmd.Flags().StringVar(&f.embedPolicy, "embed-policy", "",
		"embedding egress policy, e.g. local_only or auto (server default: auto)")
	cmd.Flags().StringArrayVar(&f.acl, "acl", nil,
		"default ACL entry granted to every document, repeatable")
	cmd.Flags().StringVar(&f.status, "status", "", "knowledge base status (server default: active)")
}

func (f *knowledgeKBFlags) body() knowledgeKBBody {
	return knowledgeKBBody{
		Name: f.name, Classification: f.classification, ResidencyRegion: f.residency,
		EmbedPolicy: f.embedPolicy, DefaultACL: f.acl, Status: f.status,
	}
}

func newKnowledgeKBsCreateCmd(client datalaneClient) *cobra.Command {
	var fields knowledgeKBFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Declare a knowledge base",
		Long: "Declare a governed knowledge base. The control plane validates the\n" +
			"embed-policy/egress and residency/egress gates before it exists: a region-locked\n" +
			"corpus cannot be declared while an egressing embedder is wired.",
		Example: `  olivares knowledge kbs create --name handbook --classification internal
  olivares knowledge kbs create --name eu-support --residency-region eu --embed-policy local_only`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if fields.name == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--name is required"))
			}
			raw, code, err := client.do(cmd, http.MethodPost, "/kbs", nil, fields.body())
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	fields.add(cmd)
	return cmd
}

func newKnowledgeKBsSetCmd(client datalaneClient) *cobra.Command {
	var (
		fields  knowledgeKBFlags
		replace bool
	)
	cmd := &cobra.Command{
		Use:   "set <kb-id>",
		Short: "Replace a knowledge base's authored fields",
		Long: "Replace the authored fields of a knowledge base.\n\n" +
			"THIS REPLACES, IT DOES NOT PATCH. The control plane rewrites classification,\n" +
			"residency region, embed policy, default ACL and status from the request, so a\n" +
			"field left out is reset to its server default — an omitted classification\n" +
			"becomes internal, an omitted ACL becomes empty. The command therefore refuses a\n" +
			"partial invocation unless --replace states that the reset is intended. The name\n" +
			"is the exception: the engine keeps the stored name when none is sent.",
		Example: `  olivares knowledge kbs set kb_123 --classification confidential --residency-region eu --embed-policy local_only --status active --acl team:support
  olivares knowledge kbs set kb_123 --status archived --replace`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := datalaneRequireCompleteReplace(cmd, replace,
				[]string{"classification", "residency-region", "embed-policy", "acl", "status"}); err != nil {
				return err
			}
			path, err := datalanePath("kbs", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPut, path, nil, fields.body())
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	fields.add(cmd)
	addDatalaneReplaceFlag(cmd, &replace)
	return cmd
}

func newKnowledgeKBsRemoveCmd(client datalaneClient) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <kb-id>",
		Aliases: []string{"delete"},
		Short:   "Delete a knowledge base and cascade its documents",
		Long: "Delete a knowledge base. The control plane cascades its documents, chunks and\n" +
			"sensitivity labels in one transaction; the append-only lineage and PII scan\n" +
			"evidence are retained deliberately. An active legal hold vetoes the delete.\n\n" +
			"JSON output is the raw API response.",
		Example: "  olivares knowledge kbs rm kb_123 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes,
				fmt.Sprintf("delete knowledge base %s and cascade its documents and chunks",
					safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			path, err := datalanePath("kbs", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodDelete, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code,
				fmt.Sprintf("deleted knowledge base %s", safeCLIValue(args[0], "")))
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}

func newKnowledgeKBsIngestCmd(client datalaneClient) *cobra.Command {
	var (
		source    string
		documents string
		docFile   string
	)
	cmd := &cobra.Command{
		Use:   "ingest <kb-id>",
		Short: "Ingest documents into a knowledge base",
		Long: "Ingest into a knowledge base, either by pulling a registered content source or\n" +
			"by supplying documents inline. Content is chunked, REDACTED and only then\n" +
			"embedded and indexed. Supply exactly one of --source or --documents/--documents-file.",
		Example: `  olivares knowledge kbs ingest kb_123 --source confluence
  olivares knowledge kbs ingest kb_123 --documents-file ./docs.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			docs, err := datalaneJSONArg(cmd, "documents", documents, docFile)
			if err != nil {
				return err
			}
			if (source == "") == (docs == nil) {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("supply exactly one of --source or --documents/--documents-file"))
			}
			path, err := datalanePath("kbs", args[0], "ingest")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, path, nil,
				knowledgeIngestBody{Source: source, Documents: docs})
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "name of a registered content source to pull from")
	cmd.Flags().StringVar(&documents, "documents", "", "inline documents as a JSON array")
	cmd.Flags().StringVar(&docFile, "documents-file", "", "file holding the JSON document array (- for stdin)")
	return cmd
}

func newKnowledgeKBsReindexCmd(client datalaneClient) *cobra.Command {
	return &cobra.Command{
		Use:   "reindex <kb-id>",
		Short: "Embed and index the knowledge base's pending chunks",
		Long: "Embed and index every chunk of a knowledge base that is not indexed yet. It is\n" +
			"the repair path after an embedder outage, and it re-applies the current embed\n" +
			"policy rather than the one in force at ingest time.",
		Example: "  olivares knowledge kbs reindex kb_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("kbs", args[0], "reindex")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
}

func newKnowledgeKBsSyncCmd(client datalaneClient) *cobra.Command {
	var source string
	cmd := &cobra.Command{
		Use:   "sync <kb-id>",
		Short: "Delta-sync a knowledge base from its content source",
		Long: "Run a delta synchronization against a registered content source: new and changed\n" +
			"documents are ingested, removed ones are deleted and ACLs are refreshed. The\n" +
			"response reports what was synced, deleted, skipped and held.",
		Example: "  olivares knowledge kbs sync kb_123 --source confluence",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if source == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--source is required"))
			}
			path, err := datalanePath("kbs", args[0], "sync")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, path, nil, knowledgeSyncBody{Source: source})
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "name of the registered content source to sync from")
	return cmd
}

func newKnowledgeKBsDocumentsCmd(client datalaneClient) *cobra.Command {
	var (
		page   datalanePageFlags
		status string
	)
	cmd := &cobra.Command{
		Use:   "documents <kb-id>",
		Short: "List a knowledge base's documents",
		Long: datalaneListLong("List the documents of one knowledge base with their source, classification\n" +
			"and indexing status."),
		Example: `  olivares knowledge kbs documents kb_123
  olivares knowledge kbs documents kb_123 --status indexed --limit 100`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query, err := page.values(cmd, datalaneFilter(nil, "status", status))
			if err != nil {
				return err
			}
			path, err := datalanePath("kbs", args[0], "documents")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no documents in this knowledge base",
				datalaneCols("ID", "id", "TITLE", "title", "SOURCE", "source_kind",
					"SOURCE_DOC", "source_doc_id", "CLASSIFICATION", "classification", "STATUS", "status"))
		},
	}
	page.add(cmd)
	cmd.Flags().StringVar(&status, "status", "", "only documents in this status")
	return cmd
}

func newKnowledgeKBsQueryCmd(client datalaneClient) *cobra.Command {
	var (
		query      string
		queryFile  string
		topK       int
		sessionRef string
	)
	cmd := &cobra.Command{
		Use:   "query <kb-id>",
		Short: "Run a governed retrieval against a knowledge base",
		Long: "Run governed retrieval: identity, classification and residency are filtered\n" +
			"BEFORE ranking, and the call writes an append-only lineage record naming the\n" +
			"chunks it returned. The effective agent is the authenticated identity — a caller\n" +
			"cannot name a privileged agent and borrow its clearance.",
		Example: `  olivares knowledge kbs query kb_123 --query "expenses policy" --top-k 5
  olivares knowledge kbs query kb_123 --query-file ./question.txt -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, set, err := datalaneTextArg(cmd, "query", query, queryFile)
			if err != nil {
				return err
			}
			if !set || text == "" {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("supply the retrieval text with --query or --query-file"))
			}
			if cmd.Flags().Changed("top-k") && topK <= 0 {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--top-k must be positive, got %d", topK))
			}
			path, err := datalanePath("kbs", args[0], "query")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, path, nil,
				knowledgeQueryBody{Query: text, TopK: topK, SessionRef: sessionRef})
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "retrieval text")
	cmd.Flags().StringVar(&queryFile, "query-file", "", "file holding the retrieval text (- for stdin)")
	cmd.Flags().IntVar(&topK, "top-k", 0, "maximum chunks to return (server default when unset)")
	cmd.Flags().StringVar(&sessionRef, "session-ref", "", "session this retrieval belongs to, recorded in lineage")
	return cmd
}

func newKnowledgeKBsScanCmd(client datalaneClient) *cobra.Command {
	return &cobra.Command{
		Use:   "scan <kb-id>",
		Short: "Run PII discovery over a knowledge base",
		Long: "Scan a knowledge base at rest for personal data, writing sensitivity labels and\n" +
			"append-only scan evidence. With no classifier wired the control plane REFUSES\n" +
			"(409) rather than reporting a clean corpus it never inspected.",
		Example: "  olivares knowledge kbs scan kb_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("kbs", args[0], "scan")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
}

// --- documents -------------------------------------------------------------

func newKnowledgeDocumentsCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "documents",
		Short:   "Inspect an individual knowledge document",
		Long:    "Read one governed document by id, across knowledge bases.",
		Example: "  olivares knowledge documents get doc_123",
		Args:    cobra.NoArgs,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "get <document-id>",
		Short: "Show one knowledge document",
		Long: "Show one document: its source, classification, ACL and indexing state. The body\n" +
			"is the redacted form, the only one that ever existed in the store.",
		Example: "  olivares knowledge documents get doc_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("documents", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	})
	return cmd
}

// --- lineage ---------------------------------------------------------------

func newKnowledgeLineageCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lineage",
		Short: "Read the append-only retrieval lineage",
		Long: "Every governed retrieval writes a lineage record: who asked, which chunks were\n" +
			"returned and whether the request was allowed or denied. It is append-only\n" +
			"evidence and survives the deletion of the corpus it describes.",
		Example: "  olivares knowledge lineage ls --decision denied",
		Args:    cobra.NoArgs,
	}
	var (
		page     datalanePageFlags
		kbID     string
		agentRef string
		decision string
	)
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List retrieval lineage records",
		Long: datalaneListLong("List lineage records, optionally narrowed to one knowledge base, one agent or\n" +
			"one decision (allowed or denied)."),
		Example: `  olivares knowledge lineage ls
  olivares knowledge lineage ls --kb-id kb_123 --decision denied --limit 20`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := datalaneFilter(nil, "kb_id", kbID)
			query = datalaneFilter(query, "agent_ref", agentRef)
			query = datalaneFilter(query, "decision", decision)
			query, err := page.values(cmd, query)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/lineage", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no lineage records match",
				datalaneCols("ID", "id", "KB", "kb_ref", "AGENT", "agent_ref",
					"DECISION", "decision", "REASON", "reason", "CHUNKS", "chunk_count"))
		},
	}
	page.add(list)
	list.Flags().StringVar(&kbID, "kb-id", "", "only lineage for this knowledge base")
	list.Flags().StringVar(&agentRef, "agent-ref", "", "only lineage for this agent")
	list.Flags().StringVar(&decision, "decision", "", "allowed or denied")

	get := &cobra.Command{
		Use:   "get <lineage-id>",
		Short: "Show one lineage record",
		Long: "Show one lineage record in full, including the chunk references that let an\n" +
			"auditor reconstruct origin to answer in a single read.",
		Example: "  olivares knowledge lineage get ln_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("lineage", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	cmd.AddCommand(list, get)
	return cmd
}

// --- prompts ---------------------------------------------------------------

func newKnowledgePromptsCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prompts",
		Short: "Manage the versioned prompt registry",
		Long: "The prompt registry keeps every revision immutable: a change appends a revision\n" +
			"and a rollback moves the current pointer. Nothing is ever rewritten in place.",
		Example: "  olivares knowledge prompts ls",
		Args:    cobra.NoArgs,
	}
	var page datalanePageFlags
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List registered prompts",
		Long:    datalaneListLong("List prompts with their current revision and latest content hash."),
		Example: `  olivares knowledge prompts ls
  olivares knowledge prompts ls --limit 50 -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query, err := page.values(cmd, nil)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/prompts", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no prompts registered",
				datalaneCols("ID", "id", "NAME", "name", "CURRENT_REV", "current_rev",
					"HASH", "latest_hash", "STATUS", "status"))
		},
	}
	page.add(list)

	get := &cobra.Command{
		Use:     "get <prompt-id>",
		Short:   "Show one prompt",
		Long:    "Show one prompt: its name, current revision pointer and latest content hash.",
		Example: "  olivares knowledge prompts get pr_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("prompts", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}

	var (
		name         string
		template     string
		templateFile string
		label        string
		note         string
	)
	create := &cobra.Command{
		Use:   "create",
		Short: "Register a prompt and its first revision",
		Long: "Register a prompt. The template is its first immutable revision; supply it with\n" +
			"--template or, for anything multi-line, --template-file (- reads stdin).",
		Example: `  olivares knowledge prompts create --name triage --template "You are a triage agent"
  olivares knowledge prompts create --name triage --template-file ./triage.txt --label v1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			text, set, err := datalaneTextArg(cmd, "template", template, templateFile)
			if err != nil {
				return err
			}
			if name == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--name is required"))
			}
			if !set || text == "" {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("supply the prompt body with --template or --template-file"))
			}
			raw, code, err := client.do(cmd, http.MethodPost, "/prompts", nil,
				knowledgePromptBody{Name: name, Template: text, Label: label, Note: note})
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	create.Flags().StringVar(&name, "name", "", "prompt name")
	create.Flags().StringVar(&template, "template", "", "prompt template text")
	create.Flags().StringVar(&templateFile, "template-file", "", "file holding the template (- for stdin)")
	create.Flags().StringVar(&label, "label", "", "label for this revision")
	create.Flags().StringVar(&note, "note", "", "note recorded with this revision")

	var (
		rev         int64
		rollbackYes bool
	)
	rollback := &cobra.Command{
		Use:   "rollback <prompt-id>",
		Short: "Point a prompt at an earlier revision",
		Long: "Move the prompt's current revision pointer back to an existing revision. No\n" +
			"revision is rewritten or removed, but every agent resolving this prompt changes\n" +
			"behavior at once — which is why it asks for confirmation.",
		Example: "  olivares knowledge prompts rollback pr_123 --rev 4 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if rev <= 0 {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--rev must be a positive revision number"))
			}
			if err := confirmDestructive(cmd, rollbackYes, fmt.Sprintf(
				"roll prompt %s back to revision %d for every agent that resolves it",
				safeCLIValue(args[0], ""), rev)); err != nil {
				return err
			}
			path, err := datalanePath("prompts", args[0], "rollback")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, path, nil, knowledgeRollbackBody{Rev: rev})
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	rollback.Flags().Int64Var(&rev, "rev", 0, "revision number to roll back to")
	addYesFlag(rollback, &rollbackYes)

	cmd.AddCommand(list, get, create, rollback, newKnowledgePromptRevisionsCmd(client))
	return cmd
}

func newKnowledgePromptRevisionsCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "revisions",
		Short:   "List, read and append immutable prompt revisions",
		Long:    "Every prompt revision is immutable; a change appends a new one.",
		Example: "  olivares knowledge prompts revisions ls pr_123",
		Args:    cobra.NoArgs,
	}
	var page datalanePageFlags
	list := &cobra.Command{
		Use:     "ls <prompt-id>",
		Aliases: []string{"list"},
		Short:   "List a prompt's revisions",
		Long:    datalaneListLong("List every revision of one prompt with its label, hash and author."),
		Example: `  olivares knowledge prompts revisions ls pr_123
  olivares knowledge prompts revisions ls pr_123 --limit 20`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query, err := page.values(cmd, nil)
			if err != nil {
				return err
			}
			path, err := datalanePath("prompts", args[0], "revisions")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "this prompt has no revisions",
				datalaneCols("REV", "rev", "LABEL", "label", "HASH", "content_hash",
					"CREATED_BY", "created_by", "NOTE", "note"))
		},
	}
	page.add(list)

	get := &cobra.Command{
		Use:     "get <prompt-id> <rev>",
		Short:   "Show one prompt revision",
		Long:    "Show one immutable revision of a prompt, including its template text and hash.",
		Example: "  olivares knowledge prompts revisions get pr_123 4",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := strconv.ParseInt(args[1], 10, 64); err != nil {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("revision must be a number, got %q", safeCLIValue(args[1], "")))
			}
			path, err := datalanePath("prompts", args[0], "revisions", args[1])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}

	var (
		template     string
		templateFile string
		label        string
		note         string
	)
	add := &cobra.Command{
		Use:   "add <prompt-id>",
		Short: "Append an immutable revision to a prompt",
		Long: "Append a revision. The prompt's current pointer moves to it; the previous\n" +
			"revisions stay readable and verifiable by hash.",
		Example: `  olivares knowledge prompts revisions add pr_123 --template-file ./v2.txt --label v2
  olivares knowledge prompts revisions add pr_123 --template "Be concise" --note "shorter"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, set, err := datalaneTextArg(cmd, "template", template, templateFile)
			if err != nil {
				return err
			}
			if !set || text == "" {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("supply the revision body with --template or --template-file"))
			}
			path, err := datalanePath("prompts", args[0], "revisions")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, path, nil,
				knowledgeRevisionBody{Template: text, Label: label, Note: note})
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	add.Flags().StringVar(&template, "template", "", "revision template text")
	add.Flags().StringVar(&templateFile, "template-file", "", "file holding the template (- for stdin)")
	add.Flags().StringVar(&label, "label", "", "label for this revision")
	add.Flags().StringVar(&note, "note", "", "note recorded with this revision")

	cmd.AddCommand(list, get, add)
	return cmd
}

// --- memory ----------------------------------------------------------------

// knowledgeMemoryScopeFlags are the DECLARED namespace dimensions every memory
// read is filtered by. They are not a convenience: the control plane scopes a
// read to the declared user/session (deny-closed isolation), so passing them is
// how a caller narrows what it is allowed to see — never how it widens it.
type knowledgeMemoryScopeFlags struct {
	agentRef   string
	userRef    string
	sessionRef string
}

func (f *knowledgeMemoryScopeFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.agentRef, "agent-ref", "", "only entries of this agent")
	cmd.Flags().StringVar(&f.userRef, "user-ref", "", "only entries declared in this user scope")
	cmd.Flags().StringVar(&f.sessionRef, "session-ref", "", "only entries declared in this session scope")
}

func (f *knowledgeMemoryScopeFlags) query(q url.Values) url.Values {
	q = datalaneFilter(q, "agent_ref", f.agentRef)
	q = datalaneFilter(q, "user_ref", f.userRef)
	return datalaneFilter(q, "session_ref", f.sessionRef)
}

func newKnowledgeMemoryCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Govern agent memory: read, write, verify, export and purge",
		Long: "Governed agent memory with retention, quota and ledger-anchored integrity.\n" +
			"Reads are scoped to the DECLARED user/session context; the cross-scope view and\n" +
			"the integrity verification are admin-tier.",
		Example: "  olivares knowledge memory ls --agent-ref agent-1",
		Args:    cobra.NoArgs,
	}
	cmd.AddCommand(
		newKnowledgeMemoryListCmd(client, "/memory", "ls", "List memory entries visible in the declared scope",
			"List memory entries the caller may see: filtered by clearance and by the declared\nuser/session scope."),
		newKnowledgeMemoryListCmd(client, "/memory/all", "all", "List every memory entry (admin-tier cross-scope view)",
			"List memory entries across every scope. This is the admin-tier governance view and\nignores the per-caller scope filter that `ls` obeys."),
		newKnowledgeMemoryGetCmd(client),
		newKnowledgeMemoryPutCmd(client),
		newKnowledgeMemoryRemoveCmd(client),
		newKnowledgeMemoryPurgeCmd(client),
		newKnowledgeMemoryVerifyCmd(client),
		newKnowledgeMemoryExportCmd(client),
		newKnowledgeMemoryImportCmd(client),
	)
	return cmd
}

func newKnowledgeMemoryListCmd(client datalaneClient, path, use, short, long string) *cobra.Command {
	var (
		page  datalanePageFlags
		scope knowledgeMemoryScopeFlags
	)
	cmd := &cobra.Command{
		Use:     use,
		Aliases: []string{"list"},
		Short:   short,
		Long:    datalaneListLong(long),
		Example: fmt.Sprintf("  olivares knowledge memory %s\n  olivares knowledge memory %s --agent-ref agent-1 --limit 50",
			use, use),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query, err := page.values(cmd, scope.query(nil))
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no memory entries visible in this scope",
				datalaneCols("ID", "id", "AGENT", "agent_ref", "KEY", "key",
					"CLASSIFICATION", "classification", "USER", "user_ref", "SESSION", "session_ref",
					"EXPIRES", "expires_at"))
		},
	}
	page.add(cmd)
	scope.add(cmd)
	if use == "all" {
		cmd.Aliases = nil
	}
	return cmd
}

func newKnowledgeMemoryGetCmd(client datalaneClient) *cobra.Command {
	var scope knowledgeMemoryScopeFlags
	cmd := &cobra.Command{
		Use:   "get <entry-id>",
		Short: "Show one memory entry",
		Long: "Show one memory entry. The control plane answers 404 for an entry outside the\n" +
			"declared scope or above the caller's clearance: absence and refusal are the same\n" +
			"answer here on purpose, so a probe cannot enumerate what it may not read.",
		Example: `  olivares knowledge memory get mem_123
  olivares knowledge memory get mem_123 --agent-ref agent-1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("memory", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, scope.query(nil), nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	scope.add(cmd)
	return cmd
}

func newKnowledgeMemoryPutCmd(client datalaneClient) *cobra.Command {
	var (
		agentRef       string
		key            string
		content        string
		contentFile    string
		classification string
		residency      string
		ttl            int64
		userRef        string
		sessionRef     string
	)
	cmd := &cobra.Command{
		Use:   "put",
		Short: "Write one governed memory entry",
		Long: "Write a memory entry for an agent. The content goes through the same fail-closed\n" +
			"write path the module uses everywhere; use --content-file (- for stdin) to keep\n" +
			"the value out of the process table.\n\n" +
			"--user-ref and --session-ref DECLARE the entry's namespace. Declaring one blank\n" +
			"is rejected by the control plane: an undeclared scope and an empty one are\n" +
			"different facts.",
		Example: `  olivares knowledge memory put --agent-ref agent-1 --key preferences --content-file ./prefs.txt
  olivares knowledge memory put --agent-ref agent-1 --key note --content "call back" --ttl-seconds 3600`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			text, set, err := datalaneTextArg(cmd, "content", content, contentFile)
			if err != nil {
				return err
			}
			if agentRef == "" || key == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--agent-ref and --key are required"))
			}
			if !set {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("supply the entry body with --content or --content-file"))
			}
			if ttl < 0 {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--ttl-seconds cannot be negative"))
			}
			body := knowledgeMemoryBody{
				AgentRef: agentRef, Key: key, Content: text,
				Classification: classification, Residency: residency, TTLSeconds: ttl,
			}
			if cmd.Flags().Changed("user-ref") {
				body.UserRef = &userRef
			}
			if cmd.Flags().Changed("session-ref") {
				body.SessionRef = &sessionRef
			}
			raw, code, err := client.do(cmd, http.MethodPost, "/memory", nil, body)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	cmd.Flags().StringVar(&agentRef, "agent-ref", "", "agent the entry belongs to")
	cmd.Flags().StringVar(&key, "key", "", "entry key within the agent's namespace")
	cmd.Flags().StringVar(&content, "content", "", "entry content")
	cmd.Flags().StringVar(&contentFile, "content-file", "", "file holding the content (- for stdin)")
	cmd.Flags().StringVar(&classification, "classification", "", "entry classification")
	cmd.Flags().StringVar(&residency, "residency-region", "", "region the entry is pinned to")
	cmd.Flags().Int64Var(&ttl, "ttl-seconds", 0, "retention in seconds (0 leaves the module default)")
	cmd.Flags().StringVar(&userRef, "user-ref", "", "declare the entry's user scope")
	cmd.Flags().StringVar(&sessionRef, "session-ref", "", "declare the entry's session scope")
	return cmd
}

func newKnowledgeMemoryRemoveCmd(client datalaneClient) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <entry-id>",
		Aliases: []string{"delete"},
		Short:   "Delete one memory entry",
		Long: "Delete one memory entry. An active legal hold over the agent or the memory class\n" +
			"vetoes the delete.\n\nJSON output is the raw API response.",
		Example: "  olivares knowledge memory rm mem_123 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes,
				fmt.Sprintf("delete memory entry %s", safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			path, err := datalanePath("memory", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodDelete, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code,
				fmt.Sprintf("deleted memory entry %s", safeCLIValue(args[0], "")))
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}

func newKnowledgeMemoryPurgeCmd(client datalaneClient) *cobra.Command {
	var (
		agentRef string
		yes      bool
	)
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Purge expired memory entries",
		Long: "Purge expired memory entries, optionally for one agent. This deletes rows: it is\n" +
			"destructive even though the method is POST, so it asks for confirmation and\n" +
			"refuses an unattended session without --yes. Entries under an active legal hold\n" +
			"are excluded by the control plane, one subject at a time.",
		Example: `  olivares knowledge memory purge --yes
  olivares knowledge memory purge --agent-ref agent-1 --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target := "purge expired memory for EVERY agent in this tenant"
			if agentRef != "" {
				target = fmt.Sprintf("purge expired memory of agent %s", safeCLIValue(agentRef, ""))
			}
			if err := confirmDestructive(cmd, yes, target); err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, "/memory/purge",
				datalaneFilter(nil, "agent_ref", agentRef), nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "purge completed")
		},
	}
	cmd.Flags().StringVar(&agentRef, "agent-ref", "", "purge only this agent's expired entries")
	addYesFlag(cmd, &yes)
	return cmd
}

func newKnowledgeMemoryVerifyCmd(client datalaneClient) *cobra.Command {
	var scope knowledgeMemoryScopeFlags
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify memory integrity against the ledger anchor",
		Long: "Recompute each entry's canonical hash and check it against the ledger anchor.\n" +
			"This is the verify-before-trust contract: a tampered entry is reported, never\n" +
			"silently served.",
		Example: `  olivares knowledge memory verify
  olivares knowledge memory verify --agent-ref agent-1 -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, code, err := client.do(cmd, http.MethodPost, "/memory/verify", scope.query(nil), nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	scope.add(cmd)
	return cmd
}

func newKnowledgeMemoryExportCmd(client datalaneClient) *cobra.Command {
	var (
		scope   knowledgeMemoryScopeFlags
		outFile string
	)
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export a signed, portable memory bundle",
		Long: "Export the caller's memory as a signed portability bundle (the anti-lock-in\n" +
			"copy). It applies the SAME clearance-filtered predicate as `ls`, never the\n" +
			"admin cross-scope view.\n\n" +
			"THE BUNDLE IS FORWARDED BYTE FOR BYTE. It is newline-delimited JSON whose\n" +
			"manifest line signs the bytes that follow, so re-encoding it as one JSON\n" +
			"document would break the verification it exists for: -o json does not reshape\n" +
			"it. Use --out to write it to a file instead of stdout.",
		Example: `  olivares knowledge memory export --out ./memory.ndjson
  olivares knowledge memory export --agent-ref agent-1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, code, err := client.do(cmd, http.MethodGet, "/memory/export", scope.query(nil), nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRaw(cmd, raw, outFile)
		},
	}
	scope.add(cmd)
	cmd.Flags().StringVar(&outFile, "out", "", "write the bundle to this file instead of stdout")
	return cmd
}

func newKnowledgeMemoryImportCmd(client datalaneClient) *cobra.Command {
	var bundleFile string
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a signed portability bundle",
		Long: "Import a memory bundle produced by `memory export`. The control plane verifies\n" +
			"the manifest signature before writing anything and applies the same fail-closed\n" +
			"write path as `put`, entry by entry. Without a verify key wired it answers 501\n" +
			"rather than importing unverified content.",
		Example: `  olivares knowledge memory import --bundle-file ./memory.ndjson
  cat memory.ndjson | olivares knowledge memory import --bundle-file -`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if bundleFile == "" {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--bundle-file is required (- reads the bundle from stdin)"))
			}
			// datalaneRawArg, NOT datalaneTextArg: the manifest line signs the
			// payload, so the bundle is posted with the bytes it has on disk. The
			// text helper trims the trailing newline by contract, which every
			// export ends in.
			bundle, err := datalaneRawArg(cmd, "bundle", bundleFile)
			if err != nil {
				return err
			}
			raw, code, err := client.doRawBody(cmd, http.MethodPost, "/memory/import", bundle)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "import completed")
		},
	}
	cmd.Flags().StringVar(&bundleFile, "bundle-file", "", "file holding the exported bundle (- for stdin)")
	return cmd
}

// --- context policies ------------------------------------------------------

func newKnowledgeContextPoliciesCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "context-policies",
		Short:   "Read and set context/compaction policies",
		Long:    "Context policies bound the token budget and redaction posture of governed retrieval.",
		Example: "  olivares knowledge context-policies ls",
		Args:    cobra.NoArgs,
	}
	var (
		page      datalanePageFlags
		scopeKind string
		scopeRef  string
	)
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List context policies",
		Long: datalaneListLong("List context/compaction policies, optionally narrowed to one scope kind or\n" +
			"scope reference."),
		Example: `  olivares knowledge context-policies ls
  olivares knowledge context-policies ls --scope-kind agent --scope-ref agent-1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := datalaneFilter(nil, "scope_kind", scopeKind)
			query = datalaneFilter(query, "scope_ref", scopeRef)
			query, err := page.values(cmd, query)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/context-policies", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no context policies declared",
				datalaneCols("ID", "id", "SCOPE_KIND", "scope_kind", "SCOPE_REF", "scope_ref",
					"MAX_TOKENS", "max_tokens", "STRATEGY", "strategy",
					"REDACTION", "redaction_required", "EFFECT", "effect"))
		},
	}
	page.add(list)
	list.Flags().StringVar(&scopeKind, "scope-kind", "", "only policies of this scope kind")
	list.Flags().StringVar(&scopeRef, "scope-ref", "", "only policies of this scope reference")

	var (
		putScopeKind string
		putScopeRef  string
		maxTokens    int64
		strategy     string
		redaction    bool
		spec         string
		specFile     string
		effect       string
	)
	put := &cobra.Command{
		Use:   "put",
		Short: "Create or replace a context policy",
		Long: "Create or replace the context policy of one scope. The floors it declares —\n" +
			"token budget and redaction — are applied by governed retrieval, and the response\n" +
			"reports what they actually did rather than only that they were set.",
		Example: `  olivares knowledge context-policies put --scope-kind agent --scope-ref agent-1 --max-tokens 8000
  olivares knowledge context-policies put --scope-kind tenant --scope-ref t1 --strategy summarize --redaction-required`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			specJSON, err := datalaneJSONArg(cmd, "spec", spec, specFile)
			if err != nil {
				return err
			}
			if putScopeKind == "" || putScopeRef == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--scope-kind and --scope-ref are required"))
			}
			if maxTokens < 0 {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--max-tokens cannot be negative"))
			}
			raw, code, err := client.do(cmd, http.MethodPost, "/context-policies", nil,
				knowledgeContextPolicyBody{
					ScopeKind: putScopeKind, ScopeRef: putScopeRef, MaxTokens: maxTokens,
					Strategy: strategy, RedactionRequired: redaction, Spec: specJSON, Effect: effect,
				})
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	put.Flags().StringVar(&putScopeKind, "scope-kind", "", "scope kind the policy applies to")
	put.Flags().StringVar(&putScopeRef, "scope-ref", "", "scope reference the policy applies to")
	put.Flags().Int64Var(&maxTokens, "max-tokens", 0, "context token budget")
	put.Flags().StringVar(&strategy, "strategy", "", "compaction strategy")
	put.Flags().BoolVar(&redaction, "redaction-required", false, "require redaction for this scope")
	put.Flags().StringVar(&spec, "spec", "", "extra policy specification as JSON")
	put.Flags().StringVar(&specFile, "spec-file", "", "file holding the JSON specification (- for stdin)")
	put.Flags().StringVar(&effect, "effect", "", "policy effect")

	cmd.AddCommand(list, put)
	return cmd
}

// --- labels and scans ------------------------------------------------------

func newKnowledgeLabelsCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "labels",
		Short:   "Read the sensitivity labels PII discovery wrote",
		Long:    "Sensitivity labels are the current-state metadata a PII scan attaches to its subjects.",
		Example: "  olivares knowledge labels ls",
		Args:    cobra.NoArgs,
	}
	var (
		page        datalanePageFlags
		subjectKind string
		kbID        string
	)
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List sensitivity labels",
		Long:    datalaneListLong("List sensitivity labels, optionally narrowed to one subject kind or knowledge base."),
		Example: `  olivares knowledge labels ls
  olivares knowledge labels ls --kb-id kb_123 --limit 100`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := datalaneFilter(nil, "subject_kind", subjectKind)
			query = datalaneFilter(query, "kb_id", kbID)
			query, err := page.values(cmd, query)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/labels", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no sensitivity labels recorded",
				datalaneCols("ID", "id", "SUBJECT_KIND", "subject_kind", "SUBJECT", "subject_ref",
					"CLASSES", "classes", "CONFIDENCE", "confidence", "SCAN", "scan_ref"))
		},
	}
	page.add(list)
	list.Flags().StringVar(&subjectKind, "subject-kind", "", "only labels of this subject kind")
	list.Flags().StringVar(&kbID, "kb-id", "", "only labels for this knowledge base")
	cmd.AddCommand(list)
	return cmd
}

func newKnowledgeScansCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "scans",
		Short:   "Read the append-only PII scan evidence",
		Long:    "Every discovery run leaves an append-only evidence row; scans are never rewritten.",
		Example: "  olivares knowledge scans ls",
		Args:    cobra.NoArgs,
	}
	var (
		page      datalanePageFlags
		scopeKind string
		scopeRef  string
	)
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List PII scan runs",
		Long:    datalaneListLong("List discovery runs and what each found, optionally narrowed to one scope."),
		Example: `  olivares knowledge scans ls
  olivares knowledge scans ls --scope-kind kb --scope-ref kb_123`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := datalaneFilter(nil, "scope_kind", scopeKind)
			query = datalaneFilter(query, "scope_ref", scopeRef)
			query, err := page.values(cmd, query)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/scans", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no scans recorded",
				datalaneCols("ID", "id", "SCOPE_KIND", "scope_kind", "SCOPE_REF", "scope_ref",
					"DOCUMENTS", "documents_scanned", "FINDINGS", "findings", "STARTED", "started_at"))
		},
	}
	page.add(list)
	list.Flags().StringVar(&scopeKind, "scope-kind", "", "only scans of this scope kind")
	list.Flags().StringVar(&scopeRef, "scope-ref", "", "only scans of this scope reference")
	cmd.AddCommand(list)
	return cmd
}

// --- DLP -------------------------------------------------------------------

func newKnowledgeDLPCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dlp",
		Short: "Read and set the DLP egress rules",
		Long: "DLP rules decide, per sensitivity class, whether content may leave the perimeter.\n" +
			"The retrieval and ingest gates enforce them deny-closed.",
		Example: "  olivares knowledge dlp ls",
		Args:    cobra.NoArgs,
	}
	var page datalanePageFlags
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the DLP egress rules",
		Long:    datalaneListLong("List the tenant's DLP rules: one action (allow or deny) per sensitivity class."),
		Example: `  olivares knowledge dlp ls
  olivares knowledge dlp ls -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query, err := page.values(cmd, nil)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/dlp/rules", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no DLP rules declared (the gate stays deny-closed)",
				datalaneCols("ID", "id", "CLASS", "class", "ACTION", "action",
					"CREATED_BY", "created_by", "NOTE", "note"))
		},
	}
	page.add(list)

	var (
		class  string
		action string
		note   string
	)
	put := &cobra.Command{
		Use:   "put",
		Short: "Create or replace one DLP rule",
		Long: "Create or replace the rule for one sensitivity class. The action must be allow\n" +
			"or deny; anything else is refused by the control plane rather than defaulted.",
		Example: `  olivares knowledge dlp put --class pii.email --action deny
  olivares knowledge dlp put --class pii.name --action allow --note "approved by DPO"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if class == "" || action == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--class and --action are required"))
			}
			raw, code, err := client.do(cmd, http.MethodPut, "/dlp/rules", nil,
				knowledgeDLPBody{Class: class, Action: action, Note: note})
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	put.Flags().StringVar(&class, "class", "", "sensitivity class the rule governs")
	put.Flags().StringVar(&action, "action", "", "allow or deny")
	put.Flags().StringVar(&note, "note", "", "note recorded with the rule")

	var yes bool
	remove := &cobra.Command{
		Use:     "rm <rule-id>",
		Aliases: []string{"delete"},
		Short:   "Delete one DLP rule",
		Long: "Delete a DLP rule. Removing a DENY rule WIDENS what may leave the perimeter for\n" +
			"that class, so it asks for confirmation.\n\n" +
			"An endpoint answering 204 has no body: JSON output is then the CLI's own\n" +
			"{\"ok\":true,\"http_status\":204}.",
		Example: "  olivares knowledge dlp rm dlp_123 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"delete DLP rule %s (if it was a deny, that class may leave the perimeter afterwards)",
				safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			path, err := datalanePath("dlp", "rules", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodDelete, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code,
				fmt.Sprintf("deleted DLP rule %s", safeCLIValue(args[0], "")))
		},
	}
	addYesFlag(remove, &yes)

	cmd.AddCommand(list, put, remove)
	return cmd
}

// --- sources ---------------------------------------------------------------

func newKnowledgeSourcesCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sources",
		Short:   "Run discovery over a registered content source",
		Long:    "Scan a registered document source for personal data WITHOUT ingesting it.",
		Example: "  olivares knowledge sources scan confluence",
		Args:    cobra.NoArgs,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "scan <source-name>",
		Short: "Scan a content source for personal data without ingesting",
		Long: "Pull a registered DOCUMENT content source and classify it in place. Audit and\n" +
			"inventory feeds are refused: they are not knowledge, and labeling them as source\n" +
			"documents would be wrong. With no classifier wired the control plane refuses\n" +
			"(409) instead of reporting a clean source it never read.",
		Example: "  olivares knowledge sources scan confluence",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("sources", args[0], "scan")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	})
	return cmd
}

// --- data products ---------------------------------------------------------

func newKnowledgeDataProductsCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "data-products",
		Short: "Govern data products and their versioned contracts",
		Long: "A data product publishes a knowledge base under a contract: a schema, a freshness\n" +
			"SLA and an enforcement mode. Once published it governs the ingest and retrieval\n" +
			"of the corpus behind it.",
		Example: "  olivares knowledge data-products ls",
		Args:    cobra.NoArgs,
	}
	cmd.AddCommand(
		newKnowledgeDataProductsListCmd(client),
		newKnowledgeDataProductsGetCmd(client),
		newKnowledgeDataProductsCreateCmd(client),
		newKnowledgeDataProductsSetCmd(client),
		newKnowledgeDataProductsRemoveCmd(client),
		newKnowledgeDataProductLifecycleCmd(client, "publish",
			"Publish a data product so its contract governs the corpus",
			"Publish a data product. From then on its active contract governs ingest and\nretrieval of the knowledge base behind it.",
			false, ""),
		newKnowledgeDataProductLifecycleCmd(client, "deprecate",
			"Deprecate a data product",
			"Deprecate a published data product. Consumers are expected to migrate; the\nproduct stops being the recommended interface to its corpus.",
			true, "deprecate data product %s, withdrawing it as the supported interface to its corpus"),
		newKnowledgeDataProductLifecycleCmd(client, "archive",
			"Archive a data product",
			"Archive a data product. It leaves the catalog and stops governing its corpus;\nthis is the admin-tier end of its lifecycle.",
			true, "archive data product %s, removing it from the catalog and ending its governance of the corpus"),
		newKnowledgeDataProductsHealthCmd(client),
		newKnowledgeDataProductsValidateCmd(client),
		newKnowledgeDataProductsEventsCmd(client),
		newKnowledgeDataContractsCmd(client),
	)
	return cmd
}

func newKnowledgeDataProductsListCmd(client datalaneClient) *cobra.Command {
	var (
		page     datalanePageFlags
		status   string
		ownerRef string
		kbRef    string
	)
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List data products",
		Long: datalaneListLong("List data products with their status, owner, backing knowledge base and\n" +
			"enforcement mode."),
		Example: `  olivares knowledge data-products ls
  olivares knowledge data-products ls --status published --kb-ref kb_123`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query := datalaneFilter(nil, "status", status)
			query = datalaneFilter(query, "owner_ref", ownerRef)
			query = datalaneFilter(query, "kb_ref", kbRef)
			query, err := page.values(cmd, query)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, "/data-products", query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no data products declared",
				datalaneCols("ID", "id", "NAME", "name", "STATUS", "status", "OWNER", "owner_ref",
					"KB", "kb_ref", "ENFORCEMENT", "enforcement_mode"))
		},
	}
	page.add(cmd)
	cmd.Flags().StringVar(&status, "status", "", "only products in this status")
	cmd.Flags().StringVar(&ownerRef, "owner-ref", "", "only products with this owner")
	cmd.Flags().StringVar(&kbRef, "kb-ref", "", "only products backed by this knowledge base")
	return cmd
}

func newKnowledgeDataProductsGetCmd(client datalaneClient) *cobra.Command {
	return &cobra.Command{
		Use:     "get <product-id>",
		Short:   "Show one data product",
		Long:    "Show one data product: its status, owner, backing corpus, SLA and enforcement mode.",
		Example: "  olivares knowledge data-products get dp_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("data-products", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
}

// knowledgeDataProductFlags builds the PATCH body: only the flags the caller
// actually passed become fields, so an unset flag changes nothing.
type knowledgeDataProductFlags struct {
	name        string
	description string
	ownerRef    string
	kbRef       string
	kbID        string
	tags        string
	tagsFile    string
	freshness   int64
	availTarget string
	enforcement string
	quality     int64
}

func (f *knowledgeDataProductFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.name, "name", "", "data product name")
	cmd.Flags().StringVar(&f.description, "description", "", "human description")
	cmd.Flags().StringVar(&f.ownerRef, "owner-ref", "", "owning team or principal")
	cmd.Flags().StringVar(&f.kbRef, "kb-ref", "", "knowledge base reference the product publishes")
	cmd.Flags().StringVar(&f.kbID, "kb-id", "", "knowledge base id the product publishes")
	cmd.Flags().StringVar(&f.tags, "tags", "", "tags as a JSON object")
	cmd.Flags().StringVar(&f.tagsFile, "tags-file", "", "file holding the JSON tags object (- for stdin)")
	cmd.Flags().Int64Var(&f.freshness, "freshness-sla-seconds", 0, "freshness SLA in seconds")
	cmd.Flags().StringVar(&f.availTarget, "availability-target", "", "availability target")
	cmd.Flags().StringVar(&f.enforcement, "enforcement-mode", "", "contract enforcement mode")
	cmd.Flags().Int64Var(&f.quality, "quality-score", 0, "quality score override")
}

func (f *knowledgeDataProductFlags) body(cmd *cobra.Command) (knowledgeDataProductBody, error) {
	tags, err := datalaneJSONArg(cmd, "tags", f.tags, f.tagsFile)
	if err != nil {
		return knowledgeDataProductBody{}, err
	}
	body := knowledgeDataProductBody{Tags: tags}
	set := func(flag string, dst **string, src *string) {
		if cmd.Flags().Changed(flag) {
			*dst = src
		}
	}
	set("name", &body.Name, &f.name)
	set("description", &body.Description, &f.description)
	set("owner-ref", &body.OwnerRef, &f.ownerRef)
	set("kb-ref", &body.KBRef, &f.kbRef)
	set("kb-id", &body.KBID, &f.kbID)
	set("availability-target", &body.AvailabilityTarget, &f.availTarget)
	set("enforcement-mode", &body.EnforcementMode, &f.enforcement)
	if cmd.Flags().Changed("freshness-sla-seconds") {
		body.FreshnessSLASeconds = &f.freshness
	}
	if cmd.Flags().Changed("quality-score") {
		body.QualityScore = &f.quality
	}
	return body, nil
}

func newKnowledgeDataProductsCreateCmd(client datalaneClient) *cobra.Command {
	var fields knowledgeDataProductFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Declare a data product",
		Long: "Declare a data product over a knowledge base. It starts unpublished: nothing is\n" +
			"enforced until `publish` and an active contract exist.",
		Example: `  olivares knowledge data-products create --name support-kb --kb-ref kb_123 --owner-ref team-support
  olivares knowledge data-products create --name metrics --kb-ref kb_9 --freshness-sla-seconds 86400`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := fields.body(cmd)
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("name") || fields.name == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--name is required"))
			}
			raw, code, err := client.do(cmd, http.MethodPost, "/data-products", nil, body)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	fields.add(cmd)
	return cmd
}

func newKnowledgeDataProductsSetCmd(client datalaneClient) *cobra.Command {
	var fields knowledgeDataProductFlags
	cmd := &cobra.Command{
		Use:   "set <product-id>",
		Short: "Update a data product's authored fields",
		Long: "Update a data product. This endpoint is a genuine PATCH — the control plane\n" +
			"applies only the fields present in the request — so an unset flag leaves its\n" +
			"stored value alone. That is why this verb carries no --replace guard while\n" +
			"`kbs set` does.",
		Example: `  olivares knowledge data-products set dp_123 --enforcement-mode strict
  olivares knowledge data-products set dp_123 --owner-ref team-data --freshness-sla-seconds 3600`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := fields.body(cmd)
			if err != nil {
				return err
			}
			path, err := datalanePath("data-products", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPut, path, nil, body)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	fields.add(cmd)
	return cmd
}

func newKnowledgeDataProductsRemoveCmd(client datalaneClient) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <product-id>",
		Aliases: []string{"delete"},
		Short:   "Delete a data product",
		Long: "Delete a data product and the contracts attached to it. The knowledge base it\n" +
			"published is not deleted.\n\nJSON output is the raw API response.",
		Example: "  olivares knowledge data-products rm dp_123 --yes",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes,
				fmt.Sprintf("delete data product %s and its contracts", safeCLIValue(args[0], ""))); err != nil {
				return err
			}
			path, err := datalanePath("data-products", args[0])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodDelete, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code,
				fmt.Sprintf("deleted data product %s", safeCLIValue(args[0], "")))
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}

// newKnowledgeDataProductLifecycleCmd builds publish/deprecate/archive: the same
// bodyless POST, differing only in whether it is destructive.
func newKnowledgeDataProductLifecycleCmd(client datalaneClient, verb, short, long string, destructive bool, confirmFmt string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     verb + " <product-id>",
		Short:   short,
		Long:    long,
		Example: fmt.Sprintf("  olivares knowledge data-products %s dp_123", verb),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if destructive {
				if err := confirmDestructive(cmd, yes,
					fmt.Sprintf(confirmFmt, safeCLIValue(args[0], ""))); err != nil {
					return err
				}
			}
			path, err := datalanePath("data-products", args[0], verb)
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	if destructive {
		cmd.Example = fmt.Sprintf("  olivares knowledge data-products %s dp_123 --yes", verb)
		cmd.Long += "\n\nIt withdraws an interface consumers may be using, so it asks for confirmation\n" +
			"and refuses an unattended session without --yes."
		addYesFlag(cmd, &yes)
	}
	return cmd
}

func newKnowledgeDataProductsHealthCmd(client datalaneClient) *cobra.Command {
	return &cobra.Command{
		Use:   "health <product-id>",
		Short: "Report a data product's freshness and quality",
		Long: "Report the product's measured freshness against its SLA and its quality score.\n" +
			"It is a measurement of the corpus, not a restatement of the declared targets.",
		Example: "  olivares knowledge data-products health dp_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("data-products", args[0], "health")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
}

func newKnowledgeDataProductsValidateCmd(client datalaneClient) *cobra.Command {
	var (
		payload      string
		payloadFile  string
		metadata     string
		metadataFile string
	)
	cmd := &cobra.Command{
		Use:   "validate <product-id>",
		Short: "Validate a payload against the product's active contract",
		Long: "Validate a candidate payload against the data product's ACTIVE contract and\n" +
			"report whether it conforms, in which validation mode and against which contract\n" +
			"version. It writes no data.",
		Example: `  olivares knowledge data-products validate dp_123 --payload-file ./row.json
  olivares knowledge data-products validate dp_123 --payload '{"id":1}' -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payloadJSON, err := datalaneJSONArg(cmd, "payload", payload, payloadFile)
			if err != nil {
				return err
			}
			metadataJSON, err := datalaneJSONArg(cmd, "metadata", metadata, metadataFile)
			if err != nil {
				return err
			}
			path, err := datalanePath("data-products", args[0], "validate")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, path, nil,
				knowledgeValidateBody{Payload: payloadJSON, Metadata: metadataJSON})
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	cmd.Flags().StringVar(&payload, "payload", "", "candidate payload as JSON")
	cmd.Flags().StringVar(&payloadFile, "payload-file", "", "file holding the JSON payload (- for stdin)")
	cmd.Flags().StringVar(&metadata, "metadata", "", "validation metadata as a JSON object")
	cmd.Flags().StringVar(&metadataFile, "metadata-file", "", "file holding the JSON metadata (- for stdin)")
	return cmd
}

func newKnowledgeDataProductsEventsCmd(client datalaneClient) *cobra.Command {
	var (
		page      datalanePageFlags
		eventType string
	)
	cmd := &cobra.Command{
		Use:   "events <product-id>",
		Short: "List a data product's enforcement events",
		Long: datalaneListLong("List the enforcement events recorded for one data product: what the contract\n" +
			"admitted, what it rejected and why."),
		Example: `  olivares knowledge data-products events dp_123
  olivares knowledge data-products events dp_123 --event-type rejected --limit 50`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query, err := page.values(cmd, datalaneFilter(nil, "event_type", eventType))
			if err != nil {
				return err
			}
			path, err := datalanePath("data-products", args[0], "events")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "no enforcement events for this data product",
				datalaneCols("ID", "id", "TYPE", "event_type", "CONTRACT", "contract_version",
					"OUTCOME", "outcome", "DETAIL", "detail", "AT", "created_at"))
		},
	}
	page.add(cmd)
	cmd.Flags().StringVar(&eventType, "event-type", "", "only events of this type")
	return cmd
}

func newKnowledgeDataContractsCmd(client datalaneClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "contracts",
		Short: "Read and add a data product's versioned contracts",
		Long: "A data contract is versioned and immutable: adding one supersedes the previous\n" +
			"active version rather than editing it.",
		Example: "  olivares knowledge data-products contracts ls dp_123",
		Args:    cobra.NoArgs,
	}
	var page datalanePageFlags
	list := &cobra.Command{
		Use:     "ls <product-id>",
		Aliases: []string{"list"},
		Short:   "List a data product's contract versions",
		Long:    datalaneListLong("List every contract version of one data product with its validation mode and status."),
		Example: `  olivares knowledge data-products contracts ls dp_123
  olivares knowledge data-products contracts ls dp_123 --limit 20`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query, err := page.values(cmd, nil)
			if err != nil {
				return err
			}
			path, err := datalanePath("data-products", args[0], "contracts")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, query, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneRenderList(cmd, client.what, raw, "this data product has no contracts",
				datalaneCols("VERSION", "version", "STATUS", "status", "MODE", "validation_mode",
					"COMPLETENESS", "completeness_threshold", "CREATED_BY", "created_by"))
		},
	}
	page.add(list)

	active := &cobra.Command{
		Use:   "active <product-id>",
		Short: "Show the contract version currently in force",
		Long: "Show the data product's ACTIVE contract — the one enforcement actually applies,\n" +
			"which is not necessarily the highest version number.",
		Example: "  olivares knowledge data-products contracts active dp_123",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := datalanePath("data-products", args[0], "contracts", "active")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}

	get := &cobra.Command{
		Use:     "get <product-id> <version>",
		Short:   "Show one contract version",
		Long:    "Show one immutable contract version, including its schema definition.",
		Example: "  olivares knowledge data-products contracts get dp_123 2",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := strconv.ParseInt(args[1], 10, 64); err != nil {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("contract version must be a number, got %q", safeCLIValue(args[1], "")))
			}
			path, err := datalanePath("data-products", args[0], "contracts", args[1])
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodGet, path, nil, nil)
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}

	var (
		schema       string
		schemaFile   string
		mode         string
		completeness int64
		freshness    int64
		note         string
	)
	add := &cobra.Command{
		Use:   "add <product-id>",
		Short: "Add a new contract version to a data product",
		Long: "Add a contract version. It supersedes the previous active version; nothing is\n" +
			"rewritten, so every earlier version stays readable as the record of what was\n" +
			"enforced at the time.",
		Example: `  olivares knowledge data-products contracts add dp_123 --schema-file ./schema.json --validation-mode strict
  olivares knowledge data-products contracts add dp_123 --completeness-threshold 95 --note "Q3 tightening"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			schemaJSON, err := datalaneJSONArg(cmd, "schema", schema, schemaFile)
			if err != nil {
				return err
			}
			if completeness < 0 || freshness < 0 {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--completeness-threshold and --freshness-override-seconds cannot be negative"))
			}
			path, err := datalanePath("data-products", args[0], "contracts")
			if err != nil {
				return err
			}
			raw, code, err := client.do(cmd, http.MethodPost, path, nil, knowledgeContractBody{
				SchemaDefinition: schemaJSON, ValidationMode: mode,
				CompletenessThreshold: completeness, FreshnessOverrideSeconds: freshness, Note: note,
			})
			if err != nil {
				return err
			}
			if !datalaneOK(code) {
				return datalaneHTTPError(client.what, code, raw)
			}
			return datalaneResult(cmd, raw, code, "")
		},
	}
	add.Flags().StringVar(&schema, "schema", "", "contract schema as JSON")
	add.Flags().StringVar(&schemaFile, "schema-file", "", "file holding the JSON schema (- for stdin)")
	add.Flags().StringVar(&mode, "validation-mode", "", "validation mode the contract enforces")
	add.Flags().Int64Var(&completeness, "completeness-threshold", 0, "minimum completeness percentage")
	add.Flags().Int64Var(&freshness, "freshness-override-seconds", 0, "freshness override for this contract")
	add.Flags().StringVar(&note, "note", "", "note recorded with the contract version")

	cmd.AddCommand(list, active, get, add)
	return cmd
}
