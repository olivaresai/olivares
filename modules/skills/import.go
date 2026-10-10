// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package skills validates immutable, portable instruction packs. Import never
// executes content or writes to a tool's native discovery home.
package skills

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

const (
	MaxSourceBytes     = 32 << 20
	MaxExpandedBytes   = 64 << 20
	MaxFileBytes       = 8 << 20
	MaxSkillBytes      = 128 << 10
	MaxFiles           = 4096
	MaxDepth           = 16
	MaxMembers         = 128
	MaxSelectedMembers = 64
	// MaxSelectionGroups bounds the agent groups one launch names: an agent
	// belongs to few groups, and a larger list is refused, never truncated.
	MaxSelectionGroups = 16
	ValidatorVersion   = "portable-v1"
)

// ImportError deliberately excludes raw source/parser/transport errors, which
// may contain secrets or terminal controls. Detail is a bounded safe path/field.
type ImportError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func (e *ImportError) Error() string { return e.Message }
func refuse(code, detail string) error {
	messages := map[string]string{
		"unsafe_pack":                  "This pack contains an unsafe path or file type. No skills were installed.",
		"import_limit":                 "This pack exceeds the import limit. Select a smaller skills folder or archive.",
		"source_changed":               "The source changed or did not match its expected digest. No revision was published.",
		"invalid_skill":                "This skill has an invalid SKILL.md. Correct the reported name, description, or format before installing it.",
		"unsupported_source":           "This source is not supported. Use a public HTTPS git URL, a local folder, or an uploaded archive.",
		"source_unavailable":           "The skills source could not be read. No revision was published.",
		"target_authority_unavailable": "Skills target authority is unavailable. No assignment was changed.",
		"skill_name_conflict":          "Two assigned skills use the same name with different content. Choose one revision before starting the session.",
	}
	if len(detail) > 160 {
		detail = detail[:160]
	}
	detail = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, detail)
	return &ImportError{Code: code, Message: messages[code], Detail: detail}
}

// FileEntry binds every original byte and normalized execution mode. Hashes
// establish integrity, not publisher authenticity or harmless instructions.
type FileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	Digest string `json:"sha256"`
}
type Member struct {
	Name          string            `json:"name"`
	Directory     string            `json:"directory"`
	Description   string            `json:"description"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	SkillDigest   string            `json:"skill_digest"`
	ContentDigest string            `json:"content_digest"`
	Extensions    []string          `json:"unsupported_extensions"`
	Scripts       []string          `json:"scripts"`
}

// ValidatedPack owns a bounded snapshot, independent of the source afterwards.
// Metadata is a preview; publication must revalidate bytes, never trust client
// digests or compatibility claims. File returns a copy, not a writable artifact.
type ValidatedPack struct {
	SourceDigest   string      `json:"source_digest"`
	ManifestDigest string      `json:"manifest_digest"`
	Manifest       []FileEntry `json:"manifest"`
	Members        []Member    `json:"members"`
	files          map[string][]byte
}

func (p *ValidatedPack) File(name string) ([]byte, bool) {
	b, ok := p.files[name]
	return bytes.Clone(b), ok
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// ImportArchive accepts ZIP and tar.gz bytes. It validates all entries before
// returning an assignable snapshot; there is no extraction directory to escape.
func ImportArchive(ctx context.Context, source io.Reader, format, expectedDigest string) (*ValidatedPack, error) {
	raw, err := boundedRead(ctx, source, MaxSourceBytes)
	if err != nil {
		return nil, err
	}
	if expectedDigest != "" && digest(raw) != expectedDigest {
		return nil, refuse("source_changed", "archive digest")
	}
	t := newTree()
	switch format {
	case "zip":
		z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			return nil, refuse("unsafe_pack", "archive")
		}
		if len(z.File) > MaxFiles {
			return nil, refuse("import_limit", "entry count")
		}
		for _, f := range z.File {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			dir := f.FileInfo().IsDir()
			if (!dir && !f.Mode().IsRegular()) || f.Flags&1 != 0 {
				return nil, refuse("unsafe_pack", f.Name)
			}
			if err := t.entry(f.Name, dir); err != nil {
				return nil, err
			}
			if dir {
				continue
			}
			if f.UncompressedSize64 > MaxFileBytes || f.UncompressedSize64 > uint64(MaxExpandedBytes-t.size) {
				return nil, refuse("import_limit", "expanded bytes")
			}
			r, err := f.Open()
			if err != nil {
				return nil, refuse("unsafe_pack", f.Name)
			}
			b, err := boundedRead(ctx, r, t.fileLimit(f.Name))
			closeErr := r.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, refuse("unsafe_pack", f.Name)
			}
			if err := t.file(f.Name, b, f.Mode()); err != nil {
				return nil, err
			}
		}
	case "tar.gz":
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, refuse("unsafe_pack", "archive")
		}
		defer func() { _ = gz.Close() }()
		// Bound the whole decoded stream, including padding and extended headers, not
		// only selected regular bytes. tar.Reader may consume PAX headers internally.
		limit := &io.LimitedReader{R: gz, N: MaxExpandedBytes + 1}
		tr := tar.NewReader(limit)
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, refuse("unsafe_pack", "archive")
			}
			if limit.N <= 0 {
				return nil, refuse("import_limit", "decoded stream")
			}
			dir := h.Typeflag == tar.TypeDir
			if !dir && h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
				return nil, refuse("unsafe_pack", h.Name)
			}
			if h.Mode&07000 != 0 || len(h.PAXRecords) > 0 {
				return nil, refuse("unsafe_pack", h.Name)
			}
			if err := t.entry(h.Name, dir); err != nil {
				return nil, err
			}
			if dir {
				if h.Size != 0 {
					return nil, refuse("unsafe_pack", h.Name)
				}
				continue
			}
			if h.Size < 0 || h.Size > int64(t.fileLimit(h.Name)) {
				return nil, refuse("import_limit", "file bytes")
			}
			b, err := boundedRead(ctx, tr, t.fileLimit(h.Name))
			if err != nil {
				return nil, err
			}
			if err := t.file(h.Name, b, fs.FileMode(h.Mode)); err != nil {
				return nil, err
			}
		}
		// Consume through checksum/trailing members with the same expansion bound.
		if _, err := io.Copy(io.Discard, limit); err != nil {
			return nil, refuse("unsafe_pack", "archive")
		}
		if limit.N <= 0 {
			return nil, refuse("import_limit", "decoded stream")
		}
	default:
		return nil, refuse("unsupported_source", "archive format")
	}
	return t.validate(digest(raw))
}

func boundedRead(ctx context.Context, r io.Reader, max int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, r: r}, int64(max)+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, refuse("unsafe_pack", "source read")
	}
	if len(b) > max {
		return nil, refuse("import_limit", "bytes")
	}
	return b, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}

type tree struct {
	files    map[string][]byte
	modes    map[string]fs.FileMode
	paths    map[string]bool
	explicit map[string]bool
	folded   map[string]string
	size     int
	count    int
}

func newTree() *tree {
	return &tree{files: map[string][]byte{}, modes: map[string]fs.FileMode{}, paths: map[string]bool{}, explicit: map[string]bool{}, folded: map[string]string{}}
}
func safePath(p string) bool {
	if p == "" || !utf8.ValidString(p) || !norm.NFC.IsNormalString(p) || len(p) > 1024 || strings.ContainsAny(p, "\\:\x00") || path.Clean(p) != p || strings.HasPrefix(p, "/") || len(strings.Split(p, "/")) > MaxDepth {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, s := range strings.Split(p, "/") {
		if s == "." || s == ".." || strings.HasSuffix(s, ".") || strings.HasSuffix(s, " ") {
			return false
		}
		switch strings.ToLower(s) {
		case ".git", ".ssh", ".aws", ".codex", ".claude", ".grok", ".config", "auth.json", "credentials.json", ".credentials.json", ".env":
			return false
		}
	}
	return true
}
func (t *tree) entry(raw string, dir bool) error {
	p := raw
	if dir {
		p = strings.TrimSuffix(p, "/")
	}
	if !safePath(p) {
		return refuse("unsafe_pack", raw)
	}
	t.count++
	if t.count > MaxFiles {
		return refuse("import_limit", "entry count")
	}
	if t.explicit[p] {
		return refuse("unsafe_pack", p)
	}
	t.explicit[p] = true
	parts := strings.Split(p, "/")
	for i := range parts {
		name := strings.Join(parts[:i+1], "/")
		isDir := i < len(parts)-1 || dir
		fold := cases.Fold().String(name)
		if prev, ok := t.folded[fold]; ok && prev != name {
			return refuse("unsafe_pack", name)
		}
		t.folded[fold] = name
		if prev, ok := t.paths[name]; ok && prev != isDir {
			return refuse("unsafe_pack", name)
		}
		t.paths[name] = isDir
		if len(t.paths) > MaxFiles {
			return refuse("import_limit", "path count")
		}
	}
	return nil
}
func (t *tree) fileLimit(p string) int {
	n := MaxFileBytes
	if path.Base(p) == "SKILL.md" {
		n = MaxSkillBytes
	}
	if MaxExpandedBytes-t.size < n {
		n = MaxExpandedBytes - t.size
	}
	return n
}
func (t *tree) file(p string, b []byte, mode fs.FileMode) error {
	if len(b) > t.fileLimit(p) {
		return refuse("import_limit", p)
	}
	if mode&(fs.ModeType|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
		return refuse("unsafe_pack", p)
	}
	t.size += len(b)
	t.files[p] = b
	t.modes[p] = mode
	return nil
}
func (t *tree) validate(sourceDigest string) (*ValidatedPack, error) {
	// Normalize a single selected pack wrapper without changing source bodies.
	// All members are siblings; selecting a repository parent is not an implicit
	// import of its configuration, authentication or arbitrary other directories.
	root := ""
	for name := range t.files {
		if path.Base(name) != "SKILL.md" {
			continue
		}
		dir := path.Dir(name)
		if dir == "." {
			return nil, refuse("invalid_skill", "skill directory")
		}
		parent := path.Dir(dir)
		if root != "" && root != parent {
			return nil, refuse("invalid_skill", "members must share a pack root")
		}
		root = parent
	}
	if root == "" {
		return nil, refuse("invalid_skill", "SKILL.md missing")
	}
	if root != "." {
		normalized := map[string][]byte{}
		modes := map[string]fs.FileMode{}
		for name, body := range t.files {
			if !strings.HasPrefix(name, root+"/") {
				return nil, refuse("unsafe_pack", name)
			}
			rel := strings.TrimPrefix(name, root+"/")
			normalized[rel] = body
			modes[rel] = t.modes[name]
		}
		t.files = normalized
		t.modes = modes
	}

	p := &ValidatedPack{SourceDigest: sourceDigest, files: t.files, Manifest: []FileEntry{}, Members: []Member{}}
	names := make([]string, 0, len(t.files))
	for name := range t.files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		mode := uint32(0644)
		if t.modes[name].Perm()&0111 != 0 {
			mode = 0755
		}
		p.Manifest = append(p.Manifest, FileEntry{Path: name, Size: int64(len(t.files[name])), Mode: mode, Digest: digest(t.files[name])})
		if path.Base(name) != "SKILL.md" {
			continue
		}
		dir := path.Dir(name)
		m, err := parseSkill(dir, t.files[name])
		if err != nil {
			return nil, err
		}
		p.Members = append(p.Members, m)
		if len(p.Members) > MaxMembers {
			return nil, refuse("import_limit", "member count")
		}
	}
	seen := map[string]bool{}
	for i := range p.Members {
		m := &p.Members[i]
		if seen[m.Name] {
			return nil, refuse("invalid_skill", "duplicate skill name")
		}
		seen[m.Name] = true
		for _, name := range names {
			if !strings.HasPrefix(name, m.Directory+"/") {
				continue
			}
			rel := strings.TrimPrefix(name, m.Directory+"/")
			if strings.HasPrefix(rel, "scripts/") || t.modes[name].Perm()&0111 != 0 {
				m.Scripts = append(m.Scripts, rel)
			}
			if rel == "agents/openai.yaml" {
				if !displayOnlyOpenAI(t.files[name]) {
					m.Extensions = append(m.Extensions, rel)
				}
			}
			if strings.HasPrefix(rel, ".claude-plugin/") || strings.HasPrefix(rel, ".grok-plugin/") || rel == "settings.json" || rel == "hooks.json" {
				m.Extensions = append(m.Extensions, rel)
			}
		}
		// Bind the entire member, including support bytes and executable modes.
		// A shared SKILL.md alone does not make two native skills identical.
		var manifest []FileEntry
		for _, entry := range p.Manifest {
			if strings.HasPrefix(entry.Path, m.Directory+"/") {
				entry.Path = strings.TrimPrefix(entry.Path, m.Directory+"/")
				manifest = append(manifest, entry)
			}
		}
		encoded, _ := json.Marshal(manifest)
		m.ContentDigest = digest(encoded)
		sort.Strings(m.Extensions)
	}
	for _, name := range names {
		owner := false
		for _, m := range p.Members {
			if strings.HasPrefix(name, m.Directory+"/") {
				owner = true
				break
			}
		}
		if !owner && (strings.Contains(name, "/") || !packMaterial(name)) {
			return nil, refuse("unsafe_pack", name)
		}
	}
	manifest, _ := json.Marshal(p.Manifest)
	p.ManifestDigest = digest(manifest)
	return p, nil
}

var portableName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func parseSkill(dir string, b []byte) (Member, error) {
	m := Member{Directory: dir, SkillDigest: digest(b), Extensions: []string{}, Scripts: []string{}}
	if len(b) > MaxSkillBytes || !utf8.Valid(b) || bytes.ContainsRune(b, 0) {
		return m, refuse("invalid_skill", "encoding/size")
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) < 3 || strings.TrimSuffix(lines[0], "\r") != "---" {
		return m, refuse("invalid_skill", "frontmatter")
	}
	end := 0
	for i := 1; i < len(lines); i++ {
		if strings.TrimSuffix(lines[i], "\r") == "---" {
			end = i
			break
		}
	}
	if end == 0 {
		return m, refuse("invalid_skill", "frontmatter")
	}
	root, err := parseYAML([]byte(strings.Join(lines[1:end], "\n")))
	if err != nil {
		return m, err
	}
	for i := 0; i < len(root.Content); i += 2 {
		k, v := root.Content[i].Value, root.Content[i+1]
		switch k {
		case "name", "description", "license", "compatibility":
			if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
				return m, refuse("invalid_skill", k)
			}
			switch k {
			case "name":
				m.Name = v.Value
			case "description":
				m.Description = v.Value
			case "license":
				m.License = v.Value
			case "compatibility":
				m.Compatibility = v.Value
			}
		case "metadata":
			if v.Kind != yaml.MappingNode {
				return m, refuse("invalid_skill", k)
			}
			m.Metadata = map[string]string{}
			for j := 0; j < len(v.Content); j += 2 {
				if v.Content[j+1].Kind != yaml.ScalarNode || v.Content[j+1].Tag != "!!str" {
					return m, refuse("invalid_skill", k)
				}
				m.Metadata[v.Content[j].Value] = v.Content[j+1].Value
			}
		default:
			m.Extensions = append(m.Extensions, k)
		}
	}
	if !portableName.MatchString(m.Name) || len(m.Name) > 64 || m.Name != path.Base(dir) || strings.TrimSpace(m.Description) == "" || utf8.RuneCountInString(m.Description) > 1024 || utf8.RuneCountInString(m.Compatibility) > 500 {
		return m, refuse("invalid_skill", "name/description/compatibility")
	}
	if strings.Contains(strings.Join(lines[end+1:], "\n"), "!`") {
		m.Extensions = append(m.Extensions, "dynamic-command-interpolation")
	}
	return m, nil
}
func parseYAML(b []byte) (*yaml.Node, error) {
	if len(b) > MaxSkillBytes || !utf8.Valid(b) || bytes.ContainsRune(b, 0) {
		return nil, refuse("invalid_skill", "YAML")
	}
	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&doc); err != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, refuse("invalid_skill", "YAML")
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, refuse("invalid_skill", "multiple YAML documents")
	}
	count := 0
	var check func(*yaml.Node, int) bool
	check = func(n *yaml.Node, depth int) bool {
		count++
		if depth > MaxDepth || count > MaxFiles || n.Kind == yaml.AliasNode || n.Anchor != "" {
			return false
		}
		switch n.Tag {
		case "!!str", "!!map", "!!seq", "!!bool", "!!int", "!!float", "!!null":
		default:
			return false
		}
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				key := n.Content[i]
				if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" || seen[key.Value] {
					return false
				}
				seen[key.Value] = true
			}
		}
		for _, child := range n.Content {
			if !check(child, depth+1) {
				return false
			}
		}
		return true
	}
	root := doc.Content[0]
	if !check(root, 0) {
		return nil, refuse("invalid_skill", "YAML tags, aliases, duplicate keys or nesting")
	}
	return root, nil
}
func displayOnlyOpenAI(b []byte) bool {
	root, err := parseYAML(b)
	if err != nil {
		return false
	}
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value != "interface" || root.Content[i+1].Kind != yaml.MappingNode {
			return false
		}
		n := root.Content[i+1]
		for j := 0; j < len(n.Content); j += 2 {
			switch n.Content[j].Value {
			case "display_name", "short_description", "icon_small", "icon_large", "brand_color":
			default:
				return false
			}
			if n.Content[j+1].Kind != yaml.ScalarNode || n.Content[j+1].Tag != "!!str" {
				return false
			}
		}
	}
	return true
}

func packMaterial(name string) bool {
	upper := strings.ToUpper(name)
	for _, prefix := range []string{"LICENSE", "COPYING", "NOTICE", "ATTRIBUTION"} {
		if upper == prefix || strings.HasPrefix(upper, prefix+".") || strings.HasPrefix(upper, prefix+"-") {
			return true
		}
	}
	switch name {
	case "README.md", "PROVENANCE.json", "OLIVARES-USE.md", "GUARD.md":
		return true
	}
	return false
}
