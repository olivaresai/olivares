// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const grpcRegionID = "olivares-grpc-reference"

// grpcMethod is one rpc, as the SERVER registers it.
type grpcMethod struct {
	Service  string // fully-qualified: olivares.api.v1.ControlPlane
	Name     string // GetServerInfo
	Kind     string // unary | server-streaming | client-streaming | bidirectional
	Request  string
	Response string
}

// FullMethod is the string that goes on the wire and into a proxy's access log.
func (m grpcMethod) FullMethod() string { return "/" + m.Service + "/" + m.Name }

type grpcService struct {
	Name    string
	Proto   string // the Metadata field: the .proto this descriptor was generated from
	Source  string // repo-relative path of the *_grpc.pb.go it was read from
	Methods []grpcMethod
}

type grpcRoster struct {
	services    []grpcService
	catalog     map[string]catalogRow
	preFindings []string
	notes       []string
}

func (g *grpcRoster) methodCount() int {
	n := 0
	for _, s := range g.services {
		n += len(s.Methods)
	}
	return n
}

// loadGRPC enumerates the gRPC surface from the GENERATED registration tables.
//
// WHY NOT THE .proto. A .proto is the intent; grpc.ServiceDesc is what the server hands
// grpc-go at RegisterService, so it is what a client can actually call. The two can
// disagree — that is exactly what "somebody edited the proto and did not run buf
// generate" looks like — and this gate reports the disagreement rather than choosing the
// prettier of the two sources. The count in each .proto is therefore read as a
// CROSS-CHECK below, never as the roster.
func loadGRPC(root string) (*grpcRoster, error) {
	g := &grpcRoster{}
	sources := []struct{ gen, proto string }{
		{coreGRPCRel, coreProtoRel},
		{sdkGRPCRel, sdkProtoRel},
	}
	// THE SET OF SOURCES IS ITSELF CHECKED. A named pair of files is a list somebody has
	// to remember to extend, which is the exact defect this whole gate exists to remove: a
	// third service in a third package would be served by the binary and invisible here,
	// and every count above would stay reassuringly correct. So the tree is walked for
	// generated registration tables and the two sets must be equal.
	declaredGen := map[string]bool{coreGRPCRel: true, sdkGRPCRel: true}
	found, err := findGRPCFiles(root)
	if err != nil {
		return nil, err
	}
	for _, rel := range found {
		if !declaredGen[rel] {
			g.preFindings = append(g.preFindings, fmt.Sprintf(
				"%s registers gRPC services and this gate does not read it, so its methods are served and undocumented — add it to the sources in scripts/guide-docs/main.go", rel))
		}
	}
	foundSet := map[string]bool{}
	for _, rel := range found {
		foundSet[rel] = true
	}
	for rel := range declaredGen {
		if !foundSet[rel] {
			return nil, cannot("%s is declared as a gRPC source and the walk did not find it; the surface was not enumerated", rel)
		}
	}

	for _, src := range sources {
		svcs, err := parseGRPCFile(root, src.gen)
		if err != nil {
			return nil, err
		}
		if len(svcs) == 0 {
			return nil, cannot("%s registers no gRPC service; a generated file with no ServiceDesc means the parse found nothing, not that the surface is empty", src.gen)
		}
		// The generated file NAMES the .proto it came from, in its Metadata field. Checking
		// it is what keeps the cross-check below from being meaningless: paired with the
		// wrong contract it would compare two unrelated numbers and, when they happened to
		// agree, report a regenerated tree as in sync.
		for _, s := range svcs {
			if s.Proto == "" {
				return nil, cannot("%s declares %s with no Metadata, so the contract it was generated from is unknown", src.gen, s.Name)
			}
			if !strings.HasSuffix(src.proto, s.Proto) {
				return nil, cannot("%s says it was generated from %q and this gate cross-checks it against %s; the pairing is wrong, so the rpc counts below would compare two unrelated contracts",
					src.gen, s.Proto, src.proto)
			}
		}
		g.services = append(g.services, svcs...)

		// The cross-check. Both numbers are printed whichever way it goes, because a
		// finding that states only the mismatch sends the reader to count by hand.
		declared, err := countProtoRPCs(root, src.proto)
		if err != nil {
			return nil, err
		}
		generated := 0
		for _, s := range svcs {
			generated += len(s.Methods)
		}
		if declared != generated {
			g.preFindings = append(g.preFindings, fmt.Sprintf(
				"%s declares %d rpc and %s registers %d; the generated code is out of date with the contract, so this reference would describe a service the binary does not serve — run buf generate",
				src.proto, declared, src.gen, generated))
		}
	}
	if g.methodCount() < grpcFloor {
		return nil, cannot("the generated descriptors yield %d rpc, below the floor of %d; that is a parse that matched almost nothing", g.methodCount(), grpcFloor)
	}

	sort.Slice(g.services, func(i, j int) bool { return g.services[i].Name < g.services[j].Name })
	for i := range g.services {
		sort.Slice(g.services[i].Methods, func(a, b int) bool {
			return g.services[i].Methods[a].Name < g.services[i].Methods[b].Name
		})
	}

	catalog, err := loadCatalog(root, grpcCatalogRel, 2)
	if err != nil {
		return nil, err
	}
	g.catalog = catalog

	used := map[string]bool{}
	for _, s := range g.services {
		for _, m := range s.Methods {
			row, ok := g.catalog[m.FullMethod()]
			if !ok {
				g.preFindings = append(g.preFindings, fmt.Sprintf(
					"%s is registered by %s and has no row in %s — add one sentence saying what it does",
					m.FullMethod(), s.Source, grpcCatalogRel))
				continue
			}
			used[m.FullMethod()] = true
			if isTODO(row.fields[0]) {
				g.preFindings = append(g.preFindings, fmt.Sprintf(
					"%s:%d leaves %s described as %q; regenerating would publish the placeholder", grpcCatalogRel, row.line, m.FullMethod(), row.fields[0]))
			} else if len(row.fields[0]) < minSummary {
				g.preFindings = append(g.preFindings, fmt.Sprintf(
					"%s:%d describes %s in %d characters, below the %d a reader can use", grpcCatalogRel, row.line, m.FullMethod(), len(row.fields[0]), minSummary))
			}
		}
	}
	// Sorted, because map iteration order is random and a gate whose output reorders on
	// every run trains its reader to stop comparing runs.
	var orphans []string
	for k := range g.catalog {
		if !used[k] {
			orphans = append(orphans, k)
		}
	}
	sort.Strings(orphans)
	for _, k := range orphans {
		g.preFindings = append(g.preFindings, fmt.Sprintf(
			"%s:%d describes %s and no generated descriptor registers it — the rpc was removed or renamed", grpcCatalogRel, g.catalog[k].line, k))
	}

	g.notes = append(g.notes, fmt.Sprintf("grpc: %d method(s) across %d service(s), read from the generated registration tables",
		g.methodCount(), len(g.services)))
	return g, nil
}

// findGRPCFiles walks the tree for protoc-gen-go-grpc output. Directories that are not
// ours are skipped by name (vendor, node_modules, .git, the docs site): a generated file
// inside a dependency is not our published surface, and walking them would make this cost
// what a full tree walk costs.
func findGRPCFiles(root string) ([]string, error) {
	var out []string
	// `.export-tmp` es la COPIA de usar y tirar que deja `lint:export --check`. Medido el
	// 2026-08-21: un residuo de 117 MB de una corrida anterior hizo que este gate reportara
	// «2 difference(s) between the tree and the published guides» nombrando ficheros
	// `.export-tmp/tmp.XXXX/public/...` — es decir, acusaba al arbol por copias de si mismo.
	// Saltarlo NO abre un punto ciego, y esa es la condicion que el comentario de abajo exige:
	// todo lo que hay ahi es una COPIA de algo que este mismo walk ya recorre, asi que un
	// servicio no puede esconderse ahi sin estar tambien en su ruta real.
	skip := map[string]bool{".git": true, "node_modules": true, "vendor": true, "docs-site": true, "dist": true, ".export-tmp": true}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory this gate could not read is a place a service could be hiding.
			// Reporting the tree clean over it is the failure mode that must not happen.
			return fmt.Errorf("could not read %s: %w", p, err)
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), "_grpc.pb.go") {
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return rerr
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, cannot("the walk for generated gRPC registration tables did not finish: %v", err)
	}
	sort.Strings(out)
	return out, nil
}

// parseGRPCFile reads the grpc.ServiceDesc literals and the client interfaces out of one
// generated file with go/parser. An AST and not a scan: `MethodName:` inside a comment,
// a string or an example is not a registration, and the difference is the whole reason
// this repo stopped enumerating public surfaces with grep.
func parseGRPCFile(root, rel string) ([]grpcService, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
	if err != nil {
		return nil, cannot("could not parse %s: %v", rel, err)
	}

	// Pass 1: the client interfaces give request/response types and the streaming shape.
	// Keyed by "<InterfaceName>.<Method>" — e.g. ControlPlaneClient.GetServerInfo.
	sigs := map[string]grpcMethod{}
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		iface, ok := ts.Type.(*ast.InterfaceType)
		if !ok || !strings.HasSuffix(ts.Name.Name, "Client") {
			return true
		}
		for _, m := range iface.Methods.List {
			fn, ok := m.Type.(*ast.FuncType)
			if !ok || len(m.Names) != 1 {
				continue
			}
			sig := methodShape(fn)
			sigs[ts.Name.Name+"."+m.Names[0].Name] = sig
		}
		return true
	})

	var out []grpcService
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			if !strings.HasSuffix(vs.Names[0].Name, "_ServiceDesc") {
				continue
			}
			lit, ok := vs.Values[0].(*ast.CompositeLit)
			if !ok {
				return nil, cannot("%s declares %s but not as a composite literal, so it could not be read", rel, vs.Names[0].Name)
			}
			svc := grpcService{Source: rel}
			ifaceName := strings.TrimSuffix(vs.Names[0].Name, "_ServiceDesc") + "Client"
			var names []string
			for _, e := range lit.Elts {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				switch key.Name {
				case "ServiceName":
					svc.Name = strLit(kv.Value)
				case "Metadata":
					svc.Proto = strLit(kv.Value)
				case "Methods":
					names = append(names, entryNames(kv.Value, "MethodName")...)
				case "Streams":
					names = append(names, entryNames(kv.Value, "StreamName")...)
				}
			}
			if svc.Name == "" {
				return nil, cannot("%s declares %s with no readable ServiceName", rel, vs.Names[0].Name)
			}
			for _, mn := range names {
				sig, ok := sigs[ifaceName+"."+mn]
				if !ok {
					// The registration table and the client interface are emitted by one
					// generator from one descriptor. If they disagree, this file was
					// hand-edited or half-regenerated, and neither half may be trusted.
					return nil, cannot("%s registers %s.%s but its %s interface declares no such method; the generated file is inconsistent with itself", rel, svc.Name, mn, ifaceName)
				}
				sig.Service = svc.Name
				sig.Name = mn
				svc.Methods = append(svc.Methods, sig)
			}
			out = append(out, svc)
		}
	}
	return out, nil
}

// methodShape reads the streaming kind and the message types out of one client method
// signature. protoc-gen-go emits exactly four shapes and they are distinguishable by the
// RESULT type, not by the name — which is why this reads the type and not the identifier.
func methodShape(fn *ast.FuncType) grpcMethod {
	m := grpcMethod{Kind: "unary"}
	if fn.Params != nil {
		for _, p := range fn.Params.List {
			for _, nm := range p.Names {
				if nm.Name == "in" {
					m.Request = typeName(p.Type)
				}
			}
		}
	}
	if fn.Results == nil || len(fn.Results.List) == 0 {
		return m
	}
	res := fn.Results.List[0].Type
	if idx, ok := res.(*ast.IndexExpr); ok { // grpc.ServerStreamingClient[T]
		switch selName(idx.X) {
		case "ServerStreamingClient":
			m.Kind = "server-streaming"
			m.Response = typeName(idx.Index)
		}
		return m
	}
	if idx, ok := res.(*ast.IndexListExpr); ok { // grpc.ClientStreamingClient[Req, Resp]
		switch selName(idx.X) {
		case "ClientStreamingClient":
			m.Kind = "client-streaming"
		case "BidiStreamingClient":
			m.Kind = "bidirectional"
		}
		if len(idx.Indices) == 2 {
			m.Request = typeName(idx.Indices[0])
			m.Response = typeName(idx.Indices[1])
		}
		return m
	}
	m.Response = typeName(res)
	return m
}

func selName(e ast.Expr) string {
	if s, ok := e.(*ast.SelectorExpr); ok {
		return s.Sel.Name
	}
	if i, ok := e.(*ast.Ident); ok {
		return i.Name
	}
	return ""
}

func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return typeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

func strLit(e ast.Expr) string {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(bl.Value)
	if err != nil {
		return ""
	}
	return s
}

// entryNames pulls the `<field>: "Name"` values out of a slice literal of structs.
func entryNames(e ast.Expr, field string) []string {
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	var out []string
	for _, el := range lit.Elts {
		inner, ok := el.(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, kv := range inner.Elts {
			pair, ok := kv.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if id, ok := pair.Key.(*ast.Ident); ok && id.Name == field {
				if v := strLit(pair.Value); v != "" {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

// countProtoRPCs counts `rpc <Name>` declarations in a .proto.
//
// This is a CROSS-CHECK against the generated descriptors, never the roster, so it does
// not need a proto grammar — but it does need to not be a grep. Comments and string
// literals are removed first, and `rpc` is only counted where it stands as a whole token
// followed by an identifier, so a comment that mentions "rpc Foo" and a service option
// whose string value contains the word are both invisible. Getting this wrong in the
// permissive direction would print a mismatch on a tree that is perfectly in sync, and a
// gate that cries wolf is a gate somebody switches off.
func countProtoRPCs(root, rel string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return 0, cannot("%s could not be read (%v), so the contract could not be cross-checked against the generated code", rel, err)
	}
	src := stripProtoNoise(string(raw))
	n := 0
	for i := 0; i+3 <= len(src); i++ {
		if src[i:i+3] != "rpc" {
			continue
		}
		if i > 0 && isIdentByte(src[i-1]) {
			continue
		}
		j := i + 3
		if j >= len(src) || !isSpaceByte(src[j]) {
			continue
		}
		for j < len(src) && isSpaceByte(src[j]) {
			j++
		}
		if j < len(src) && (isAlphaByte(src[j]) || src[j] == '_') {
			n++
		}
	}
	return n, nil
}

// stripProtoNoise blanks comments and string literals, keeping byte offsets irrelevant —
// only token adjacency matters to the caller.
func stripProtoNoise(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		switch {
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			if i+1 < len(s) {
				i += 2
			} else {
				i = len(s)
			}
			b.WriteByte(' ')
		case s[i] == '"' || s[i] == '\'':
			q := s[i]
			i++
			for i < len(s) && s[i] != q {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				i++
			}
			if i < len(s) {
				i++
			}
			b.WriteByte(' ')
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

func isIdentByte(c byte) bool { return isAlphaByte(c) || (c >= '0' && c <= '9') || c == '_' }
func isAlphaByte(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func (g *grpcRoster) print(w io.Writer) {
	fmt.Fprintf(w, "# grpc — %d methods across %d services\n", g.methodCount(), len(g.services))
	for _, s := range g.services {
		for _, m := range s.Methods {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.FullMethod(), m.Kind, m.Request, m.Response)
		}
	}
}

func (g *grpcRoster) region() string {
	var b strings.Builder
	fmt.Fprintf(&b, "The engine and the plugin host register **%d rpc** across **%d services**. The tables below\n", g.methodCount(), len(g.services))
	fmt.Fprintf(&b, "are read from the generated registration tables the servers hand to gRPC, so a method that\n")
	fmt.Fprintf(&b, "is listed here is a method a client can call.\n")

	for _, s := range g.services {
		fmt.Fprintf(&b, "\n### `%s`\n\n", s.Name)
		fmt.Fprintf(&b, "Defined in `%s`; %d rpc.\n\n", mdCell(s.Proto), len(s.Methods))
		b.WriteString("| Method | Full method | Kind | Request | Response | What it does |\n")
		b.WriteString("|---|---|---|---|---|---|\n")
		for _, m := range s.Methods {
			summary := ""
			if row, ok := g.catalog[m.FullMethod()]; ok {
				summary = row.fields[0]
			}
			req, resp := m.Request, m.Response
			if req == "" {
				req = "—"
			} else {
				req = "`" + req + "`"
			}
			if resp == "" {
				resp = "—"
			} else {
				resp = "`" + resp + "`"
			}
			if m.Kind == "server-streaming" {
				resp += " (stream)"
			}
			if m.Kind == "client-streaming" || m.Kind == "bidirectional" {
				req += " (stream)"
			}
			if m.Kind == "bidirectional" {
				resp += " (stream)"
			}
			fmt.Fprintf(&b, "| `%s` | `%s` | %s | %s | %s | %s |\n",
				m.Name, m.FullMethod(), m.Kind, req, resp, mdCell(summary))
		}
	}
	return b.String()
}

func applyGRPC(root string, g *grpcRoster, write bool) (*surface, error) {
	s := &surface{name: "grpc", findings: append([]string{}, g.preFindings...), notes: g.notes}
	if len(s.findings) > 0 && write {
		return s, nil
	}
	keys := []string{}
	for _, svc := range g.services {
		for _, m := range svc.Methods {
			keys = append(keys, "`"+m.FullMethod()+"`")
		}
	}
	f, err := applyRegion(root, grpcPageRel, grpcRegionID, g.region(), write, &keySpec{
		noun: "rpc", want: keys, col: 1,
		fix: "Regenerate with `bash scripts/check-guide-docs.sh --write`.",
	})
	if err != nil {
		return nil, err
	}
	s.findings = append(s.findings, f...)
	return s, nil
}
