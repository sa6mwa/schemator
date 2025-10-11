package schemator

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/invopop/jsonschema"
	"pkt.systems/logport"
	"pkt.systems/logport/adapters/zerologger"
	"pkt.systems/schemator/example"
)

func TestInferLocalImportPath_AppendsPackageName(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/project\n\n")

	pkgDir := filepath.Join(root, "foo")
	writeFile(t, filepath.Join(pkgDir, "foo.go"), "package foo\n")

	ip, err := inferLocalImportPath(context.Background(), pkgDir)
	if err != nil {
		t.Fatalf("inferLocalImportPath() error = %v", err)
	}

	if got, want := ip.ModuleImportPath, "example.com/project/foo"; got != want {
		t.Fatalf("ModuleImportPath = %q, want %q", got, want)
	}
	if !samePath(ip.SourceDirectory, pkgDir) {
		t.Fatalf("SourceDirectory = %q, want %q", ip.SourceDirectory, pkgDir)
	}
}

func TestInferLocalImportPath_PackageMain(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/project\n\n")

	pkgDir := filepath.Join(root, "cmd", "tool")
	writeFile(t, filepath.Join(pkgDir, "main.go"), "package main\n")

	ip, err := inferLocalImportPath(context.Background(), pkgDir)
	if err != nil {
		t.Fatalf("inferLocalImportPath() error = %v", err)
	}

	if got, want := ip.ModuleImportPath, "example.com/project/cmd/tool"; got != want {
		t.Fatalf("ModuleImportPath = %q, want %q", got, want)
	}
	if !samePath(ip.SourceDirectory, pkgDir) {
		t.Fatalf("SourceDirectory = %q, want %q", ip.SourceDirectory, pkgDir)
	}
}

func TestInferLocalImportPath_NoGoMod(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "foo.go"), "package foo\n")

	if _, err := inferLocalImportPath(context.Background(), dir); err == nil {
		t.Fatalf("inferLocalImportPath() error = nil, want error")
	}
}

func TestEnsureSourceDirectory_StandardLibrary(t *testing.T) {
	ip := ImportPath{ModuleImportPath: "time"}
	resolved, err := ensureSourceDirectory(context.Background(), ip)
	if err != nil {
		t.Fatalf("ensureSourceDirectory() error = %v", err)
	}
	if resolved.SourceDirectory == "" {
		t.Fatalf("SourceDirectory is empty for standard library package")
	}
	info, statErr := os.Stat(resolved.SourceDirectory)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("resolved path %q is not a directory: %v", resolved.SourceDirectory, statErr)
	}
}

func TestEnsureSourceDirectory_InvalidPackage(t *testing.T) {
	ip := ImportPath{ModuleImportPath: "invalid!pkg"}
	if _, err := ensureSourceDirectory(context.Background(), ip); err == nil {
		t.Fatalf("ensureSourceDirectory() error = nil, want error")
	}
}

func TestGenerateAddsGoComments(t *testing.T) {
	ctx := context.Background()
	g := New(ctx, nil)
	schemaBytes, err := g.Generate(example.Subject{})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(schemaBytes, &doc); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	props, ok := doc["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing in schema: %v", doc)
	}
	field, ok := props["id"].(map[string]any)
	if !ok {
		t.Fatalf("id property missing: %v", props)
	}
	desc, _ := field["description"].(string)
	if !strings.Contains(desc, "ID is the ID of the subject") {
		t.Fatalf("expected description to mention field doc, got %q", desc)
	}
}

func TestCollectDependentPackages(t *testing.T) {
	type nested struct {
		When *time.Time
		IDs  []uuid.UUID
		Meta map[string]*uuid.UUID
	}

	pkgs := collectDependentPackages(nested{})
	if len(pkgs) == 0 {
		t.Fatalf("expected packages to be collected")
	}
	set := make(map[string]struct{}, len(pkgs))
	for _, p := range pkgs {
		set[p] = struct{}{}
	}
	for _, want := range []string{"time", "github.com/google/uuid"} {
		if _, ok := set[want]; !ok {
			t.Fatalf("package %q not discovered, got %v", want, pkgs)
		}
	}
}

func TestResolveImportPathsAutoAddsDependencies(t *testing.T) {
	ctx := context.Background()
	genIface := New(ctx, nil)
	gen, ok := genIface.(*generator)
	if !ok {
		t.Fatalf("New did not return *generator")
	}
	if _, err := gen.Generate(example.Example{}); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	paths := make(map[string]ImportPath, len(gen.importPaths))
	for _, ip := range gen.importPaths {
		paths[ip.ModuleImportPath] = ip
		if ip.SourceDirectory == "" {
			t.Fatalf("resolved import path %q missing SourceDirectory", ip.ModuleImportPath)
		}
	}
	for _, want := range []string{
		"pkt.systems/schemator",
		"pkt.systems/schemator/example",
		"github.com/google/uuid",
		"time",
	} {
		if _, ok := paths[want]; !ok {
			t.Fatalf("expected import path %q to be resolved, got %v", want, gen.importPaths)
		}
	}
}

func TestAddGoCommentsForImportPathWithAbsoluteDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "types.go"), `package foo

// DemoType is an example type.
type DemoType struct {
	// Info summary line.
	// Wrapped onto a new line.
	Info string
}
`)

	r := &jsonschema.Reflector{}
	ip := ImportPath{
		ModuleImportPath: "example.com/temp/foo",
		SourceDirectory:  dir,
	}
	if err := addGoCommentsForImportPath(r, ip); err != nil {
		t.Fatalf("addGoCommentsForImportPath() error = %v", err)
	}
	comment := r.CommentMap["example.com/temp/foo.DemoType.Info"]
	if comment != "Info summary line. Wrapped onto a new line." {
		t.Fatalf("expected sanitized comment, got %q", comment)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func samePath(a, b string) bool {
	ai, err := filepath.EvalSymlinks(a)
	if err != nil {
		ai = filepath.Clean(a)
	}
	bi, err := filepath.EvalSymlinks(b)
	if err != nil {
		bi = filepath.Clean(b)
	}
	return ai == bi
}

func TestInferImportPathHelper(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/project\n\n")
	fooDir := filepath.Join(root, "foo")
	writeFile(t, filepath.Join(fooDir, "foo.go"), "package foo\n")
	ctx := context.Background()
	ip := InferImportPath(ctx, fooDir)
	if ip.ModuleImportPath != "example.com/project/foo" {
		t.Fatalf("ModuleImportPath = %q, want %q", ip.ModuleImportPath, "example.com/project/foo")
	}
	if !samePath(ip.SourceDirectory, fooDir) {
		t.Fatalf("SourceDirectory = %q, want %q", ip.SourceDirectory, fooDir)
	}

	ip = InferImportPath(ctx, filepath.Join(root, "missing"))
	if ip.ModuleImportPath != "" || ip.SourceDirectory != "" {
		t.Fatalf("expected empty import path on failure, got %+v", ip)
	}
}

func TestImportPathHelpers(t *testing.T) {
	ctx := context.Background()
	ips := ImportPaths("a/b", "c/d")
	if len(ips) != 2 || ips[0].ModuleImportPath != "a/b" || ips[1].ModuleImportPath != "c/d" {
		t.Fatalf("ImportPaths returned unexpected result: %+v", ips)
	}

	withLocal := ImportPathsWithLocal(ctx, "x/y")
	if len(withLocal) != 2 {
		t.Fatalf("ImportPathsWithLocal length mismatch: %+v", withLocal)
	}
	if withLocal[0].ModuleImportPath == "" {
		t.Fatalf("local import path missing: %+v", withLocal)
	}
	if withLocal[1].ModuleImportPath != "x/y" {
		t.Fatalf("expected appended import path 'x/y', got %+v", withLocal[1])
	}
}

func TestSchemaBytesString(t *testing.T) {
	b := SchemaBytes(`{"name":"value"}`)
	if b.String() != "{\"name\":\"value\"}" {
		t.Fatalf("String() mismatch: %s", b.String())
	}
}

func TestToStringAndExportedName(t *testing.T) {
	if got := toString(example.Subject{}); got != "Subject" {
		t.Fatalf("toString example.Subject = %q", got)
	}
	if got := toString(&example.Subject{}); got != "Subject" {
		t.Fatalf("toString pointer = %q", got)
	}
	type wrapper struct {
		Items []example.Subject
	}
	if got := toString(wrapper{}); got != "" {
		t.Fatalf("toString unexported struct should be empty, got %q", got)
	}
	if got := toString([]*example.Subject{}); got != "SubjectSlice" {
		t.Fatalf("toString slice = %q", got)
	}

	if name := exportedName(reflect.TypeOf(example.Subject{})); name != "Subject" {
		t.Fatalf("exportedName = %q", name)
	}
	if name := exportedName(reflect.TypeOf(wrapper{})); name != "" {
		t.Fatalf("exportedName for unexported type should be empty, got %q", name)
	}
}

func TestWriteSchemaCreatesFile(t *testing.T) {
	ctx := context.Background()
	gen := New(ctx, nil)
	outDir := t.TempDir()
	outFile := filepath.Join(outDir, "subject.schema.json")
	if err := gen.WriteSchema(example.Subject{}, outFile); err != nil {
		t.Fatalf("WriteSchema() error = %v", err)
	}
	info, err := os.Stat(outFile)
	if err != nil || info.Size() == 0 {
		t.Fatalf("expected schema file to be created, err=%v info=%+v", err, info)
	}
}

func TestWriteSchemaFailsWhenRequiredMissing(t *testing.T) {
	ctx := context.Background()
	gen := New(ctx, []string{"does-not-exist"})
	if _, err := gen.Generate(example.Subject{}); err == nil {
		t.Fatalf("Generate expected error when required file is missing")
	}
}

func TestWriteSchemasGeneratesMultiple(t *testing.T) {
	logger := zerologger.New(io.Discard)
	ctx := logport.ContextWithLogger(context.Background(), logger)
	gen := New(ctx, nil)
	outDir := t.TempDir()
	if err := gen.WriteSchemas(outDir, example.Subject{}, example.Example{}); err != nil {
		t.Fatalf("WriteSchemas() error = %v", err)
	}
	files, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("ReadDir error: %v", err)
	}
	if len(files) < 2 {
		t.Fatalf("expected at least two schema files, got %d", len(files))
	}
}
