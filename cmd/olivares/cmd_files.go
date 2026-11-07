// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/runtime/documents"
	"github.com/spf13/cobra"
)

func newFilesCmd() *cobra.Command {
	root := &cobra.Command{Use: "files", Short: "Convert local documents to Markdown", Long: "Convert a local document (PDF, DOCX, PPTX, XLSX, HTML, CSV, JSON, XML, EPUB or ZIP) to Markdown beside the original.\nThe original file is never changed.", Example: "  olivares files convert report.pdf", Annotations: map[string]string{"changes_data": "write"}}
	var asJSON bool
	cmd := &cobra.Command{Annotations: map[string]string{"changes_data": "write"}, Example: "  olivares files convert report.pdf\n  olivares files convert report.xlsx --json", Use: "convert <path>", Short: "Create Markdown beside a document, preserving the original", Long: "Create <path>.md beside the document and print its path. The original document is left unchanged, and an existing <path>.md is never overwritten.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		path, err := filepath.Abs(args[0])
		if err != nil {
			return exitcode.New(exitcode.Usage, err)
		}
		folder, err := os.OpenRoot(filepath.Dir(path))
		if err != nil {
			return err
		}
		defer folder.Close()
		name, err := documents.Convert(cmd.Context(), folder, filepath.Base(path))
		if err != nil {
			return err
		}
		output := filepath.Join(filepath.Dir(path), name)
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"path": output})
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), output)
		return err
	}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the Markdown path as JSON")
	root.AddCommand(cmd)
	return root
}
