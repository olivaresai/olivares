// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"flag"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/cedar-policy/cedar-go"
)

// maxCorpusEntryBytes bounds one corpus file read into memory.
const maxCorpusEntryBytes = 1 << 20

var errCorpusEntryTooLarge = errors.New("corpus entry too large")

// agreement counts, for one batch, where the framer and the library agree.
// Lists hold corpus file names or edge-case names only, never text.
type agreement struct {
	Documents          int      `json:"documents"`
	LibraryAccepted    int      `json:"library_accepted"`
	LibraryRejected    int      `json:"library_rejected"`
	FramedAdmitted     int      `json:"framed_admitted"`
	Agreements         int      `json:"agreements"`
	SafetyViolations   []string `json:"safety_violations"`
	CompletenessMisses []string `json:"completeness_misses"`
	CountMismatches    []string `json:"count_mismatches"`
	TextMismatches     []string `json:"text_mismatches"`
	OffsetMismatches   []string `json:"offset_mismatches"`
}

func newAgreement() agreement {
	return agreement{
		SafetyViolations:   []string{},
		CompletenessMisses: []string{},
		CountMismatches:    []string{},
		TextMismatches:     []string{},
		OffsetMismatches:   []string{},
	}
}

func (a *agreement) disagreements() int {
	return len(a.SafetyViolations) + len(a.CompletenessMisses) + len(a.CountMismatches) +
		len(a.TextMismatches) + len(a.OffsetMismatches)
}

// compareOne applies the original O-frame controls to one document:
// (i) accepted by the library: the framer admits it, with the same policy
// count and the same text for each policy; (ii) rejected by the library: the
// framer refuses it; (iii) the last policy is present (implied by (i) in
// order); (iv) each policy's byte offset equals the library's.
func compareOne(name, doc string, a *agreement) {
	a.Documents++
	lib, libErr := cedar.NewPolicyListFromBytes("", []byte(doc))
	fr := admitFramed(doc)
	if fr.admitted {
		a.FramedAdmitted++
	}
	if libErr != nil {
		a.LibraryRejected++
		if fr.admitted {
			a.SafetyViolations = append(a.SafetyViolations, name)
			return
		}
		a.Agreements++
		return
	}
	a.LibraryAccepted++
	if !fr.admitted {
		a.CompletenessMisses = append(a.CompletenessMisses, name)
		return
	}
	if len(lib) != len(fr.policies) {
		a.CountMismatches = append(a.CountMismatches, name)
		return
	}
	for i := range lib {
		got := cedar.NewPolicyFromAST(&fr.policies[i]).MarshalCedar()
		if string(got) != string(lib[i].MarshalCedar()) {
			a.TextMismatches = append(a.TextMismatches, name)
			return
		}
		if fr.offsets[i] != lib[i].Position().Offset {
			a.OffsetMismatches = append(a.OffsetMismatches, name)
			return
		}
	}
	a.Agreements++
}

type corpusDoc struct {
	name string
	text string
}

// readCorpus reads every regular .cedar entry of the gzip tar archive, sorted
// by name, so every batch sees the same order.
func readCorpus(archivePath string) ([]corpusDoc, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var docs []corpusDoc
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if !h.FileInfo().Mode().IsRegular() || !strings.HasSuffix(h.Name, ".cedar") {
			continue
		}
		if h.Size > maxCorpusEntryBytes {
			return nil, errCorpusEntryTooLarge
		}
		b, err := io.ReadAll(io.LimitReader(tr, h.Size))
		if err != nil {
			return nil, err
		}
		docs = append(docs, corpusDoc{name: path.Base(h.Name), text: string(b)})
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].name < docs[j].name })
	return docs, nil
}

// oframeDetail is the detail of an oframe record.
type oframeDetail struct {
	Batch     int       `json:"batch"`
	Batches   int       `json:"batches"`
	Edges     bool      `json:"edges"`
	CorpusAll int       `json:"corpus_documents"`
	Result    agreement `json:"agreement"`
}

func runOFrame(args []string) int {
	fs := flag.NewFlagSet("oframe", flag.ContinueOnError)
	corpusPath := fs.String("corpus", "", "path of corpus-tests.tar.gz")
	batch := fs.Int("batch", 0, "batch index, from 0")
	batches := fs.Int("batches", 1, "number of batches")
	edges := fs.Bool("edges", false, "compare the edge set instead of a corpus batch")
	token := fs.String("token", "", "token chosen by the supervisor")
	rec := &record{Schema: recordSchema, Mode: "oframe", Control: controlNone, Env: currentEnv()}
	if err := fs.Parse(args); err != nil {
		return emit(rec, exitInability, "flags")
	}
	rec.Token = *token
	phase("oframe")
	d := oframeDetail{Batch: *batch, Batches: *batches, Edges: *edges, Result: newAgreement()}
	if *edges {
		for _, e := range edgeCases() {
			compareOne(e.name, e.text, &d.Result)
		}
	} else {
		if *batches < 1 || *batch < 0 || *batch >= *batches {
			return emit(rec, exitInability, "batch_range")
		}
		docs, err := readCorpus(*corpusPath)
		if err != nil {
			return emit(rec, exitInability, "corpus_unreadable")
		}
		d.CorpusAll = len(docs)
		size := (len(docs) + *batches - 1) / *batches
		lo := min(*batch*size, len(docs))
		hi := min(lo+size, len(docs))
		for _, doc := range docs[lo:hi] {
			compareOne(doc.name, doc.text, &d.Result)
		}
	}
	rec.Detail = d
	if d.Result.disagreements() > 0 {
		return emit(rec, exitMismatch, "framer_disagrees")
	}
	return emit(rec, exitObserved, "")
}
