// schemator is a helper library that turns Go types into JSON Schema
// definitions. It wraps github.com/invopop/jsonschema and focuses on build-time
// schema generation so that applications can ship pre-generated schemas
// alongside the binaries that use them.
package schemator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/invopop/jsonschema"
	"pkt.systems/logport"
)

// If no ImportPaths are given, the generator will attempt to find go.mod, use
// that module name and add package found in the first source file found in
// ImportPath.SourceDirectory, defaults to `./`. This works most of the time,
// but not for additional external modules you want to generate schemas for.
func New(ctx context.Context, filesThatMustExist []string, ImportPaths ...ImportPath) Generator {
	return &generator{
		ctx:                ctx,
		filesThatMustExist: filesThatMustExist,
		importPaths:        ImportPaths,
	}
}

// If you provide your own ImportPaths and not letting them be automatically
// inferred, you can use InferImportPath to add the local module import path
// automatically, e.g.:
//
//	ips := schemator.ImportPaths{schemator.InferImportPath(nil), schemator.ImportPath{ModuleImportPath: "github.com/google/uuid"} }
func InferImportPath(ctx context.Context, sourceDirectory ...string) ImportPath {
	if ctx == nil {
		ctx = context.Background()
	}
	sourceDir := ""
	if len(sourceDirectory) > 0 {
		sourceDir = sourceDirectory[0]
	}
	l := logport.LoggerFromContext(ctx).With("function", "InferImportPath")
	ip, err := inferLocalImportPath(ctx, sourceDir)
	if err != nil {
		l.Error("Unable to infer local import path", "error", err)
		return ImportPath{}
	}
	return ip
}

type ImportPath struct {
	// Full go module import path for the types you want to generate schemas for
	ModuleImportPath string
	// Directory from current path where source code files for ModuleImportPath
	// can be found (defaults to `./`).
	SourceDirectory string
}

// ImportPaths returns an ImportPath slice from all import path strings
// specified in importPaths.
func ImportPaths(importPaths ...string) []ImportPath {
	if len(importPaths) == 0 {
		return nil
	}
	ips := []ImportPath{}
	for _, ip := range importPaths {
		ips = append(ips, ImportPath{
			ModuleImportPath: ip,
		})
	}
	return ips
}

// ImportPathsWithLocal prepends the inferred local import path to those
// specified with importPaths and returns a slice of ImportPath.
func ImportPathsWithLocal(ctx context.Context, importPaths ...string) []ImportPath {
	ips := ImportPaths(importPaths...)
	localip := InferImportPath(ctx)
	newips := []ImportPath{localip}
	return append(newips, ips...)
}

type Generator interface {
	// Generate generates a JSON schema from concrete type model and returns a
	// renedered json schema in SchemaBytes or error if something failed.
	Generate(model any) (SchemaBytes, error)
	// WriteSchema generates a JSON schema from concrete type model and writes a
	// rendered json schema to filenamePath.
	WriteSchema(model any, filenamePath string) error
	// WriteSchemas writes every model mentioned into auto-generated filenames
	// inside outputDir.
	WriteSchemas(outputDir string, models ...any) error
}

type SchemaBytes []byte

func (b SchemaBytes) String() string {
	return string(b)
}

// Implements Generator
type generator struct {
	ctx                context.Context
	filesThatMustExist []string
	importPaths        []ImportPath
}

func (g *generator) Generate(model any) (SchemaBytes, error) {
	ctx := g.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	importPaths, err := g.resolveImportPaths(ctx, model)
	if err != nil {
		return nil, err
	}
	l := logport.LoggerFromContext(ctx).With(
		"importPaths", importPaths,
		"filesThatMustExist", g.filesThatMustExist,
		"model", model,
	)
	l.Debug("Generating JSON schema")
	if len(g.filesThatMustExist) > 0 {
		for _, p := range g.filesThatMustExist {
			if _, err := os.Stat(p); err != nil {
				return nil, err
			}
		}
	}
	r := &jsonschema.Reflector{
		ExpandedStruct:            true,
		AllowAdditionalProperties: false,
	}
	for _, ip := range importPaths {
		if err := addGoCommentsForImportPath(r, ip); err != nil {
			return nil, err
		}
	}
	s := r.Reflect(model)
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (g *generator) WriteSchema(model any, filenamePath string) error {
	out, err := g.Generate(model)
	if err != nil {
		return err
	}
	ctx := g.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	l := logport.LoggerFromContext(ctx).With(
		"importPaths", g.importPaths,
		"filesThatMustExist", g.filesThatMustExist,
		"model", model,
	)
	fpath := filepath.Dir(filenamePath)
	l.Debug("os.MkdirAll", "path", fpath)
	if err := os.MkdirAll(fpath, 0o0755); err != nil {
		return err
	}
	l.Debug("os.Create", "name", filenamePath)
	f, err := os.Create(filenamePath)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := fmt.Fprintln(f, out)
	l.Debug("Wrote JSON schema", "name", filenamePath, "bytesWritten", n, "error", err)
	return err
}

func (g *generator) WriteSchemas(outputDir string, models ...any) error {
	l := logport.LoggerFromContext(g.ctx).With("outputDir", outputDir, "models", models)
	if len(models) == 0 {
		l.Debug("WriteSchemas: no models provided")
		return nil
	}
	for _, model := range models {
		filename := toString(model)
		if filename == "" {
			l.Debug("Unable to reflect filename (string) from model (any), skipping", "model", model)
			continue
		}
		if err := g.WriteSchema(model, filepath.Join(outputDir, filename+".schema.json")); err != nil {
			return err
		}
	}
	return nil
}

// Helper functions...

func toString(x any) string {
	if x == nil {
		return ""
	}
	t := reflect.TypeOf(x)
	// unwrap top-level pointers
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	// 1) direct named, exported type
	if n := exportedName(t); n != "" {
		return n
	}
	// 2) slice/array of named, exported element type
	if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		elem := t.Elem()
		for elem.Kind() == reflect.Ptr {
			elem = elem.Elem()
		}
		if n := exportedName(elem); n != "" {
			return n + "Slice"
		}
	}
	return ""
}
func exportedName(t reflect.Type) string {
	// only defined types have a name
	if t.Name() == "" {
		return ""
	}
	// only exported identifiers
	if !token.IsExported(t.Name()) {
		return ""
	}
	return t.Name()
}

func collectDependentPackages(models ...any) []string {
	packages := make(map[string]struct{})
	visited := make(map[reflect.Type]struct{})
	for _, model := range models {
		if model == nil {
			continue
		}
		t := reflect.TypeOf(model)
		visitTypeForPackages(t, packages, visited)
	}
	out := make([]string, 0, len(packages))
	for pkg := range packages {
		out = append(out, pkg)
	}
	sort.Strings(out)
	return out
}

func visitTypeForPackages(t reflect.Type, packages map[string]struct{}, visited map[reflect.Type]struct{}) {
	if t == nil {
		return
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil {
		return
	}
	if _, seen := visited[t]; seen {
		return
	}
	visited[t] = struct{}{}

	if pkg := t.PkgPath(); pkg != "" {
		packages[pkg] = struct{}{}
	}

	switch t.Kind() {
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			visitTypeForPackages(t.Field(i).Type, packages, visited)
		}
	case reflect.Slice, reflect.Array:
		visitTypeForPackages(t.Elem(), packages, visited)
	case reflect.Map:
		visitTypeForPackages(t.Key(), packages, visited)
		visitTypeForPackages(t.Elem(), packages, visited)
	case reflect.Pointer:
		visitTypeForPackages(t.Elem(), packages, visited)
	}
}

func (g *generator) resolveImportPaths(ctx context.Context, models ...any) ([]ImportPath, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	existing := make(map[string]int, len(g.importPaths))
	for i, ip := range g.importPaths {
		if ip.ModuleImportPath != "" {
			existing[ip.ModuleImportPath] = i
		}
	}

	if len(g.importPaths) == 0 {
		ip, err := inferLocalImportPath(ctx, "./")
		if err != nil {
			return nil, err
		}
		g.importPaths = append(g.importPaths, ip)
		existing[ip.ModuleImportPath] = len(g.importPaths) - 1
	}

	inferredPkgs := collectDependentPackages(models...)
	for _, pkg := range inferredPkgs {
		if pkg == "" {
			continue
		}
		if _, found := existing[pkg]; found {
			continue
		}
		g.importPaths = append(g.importPaths, ImportPath{ModuleImportPath: pkg})
		existing[pkg] = len(g.importPaths) - 1
	}

	resolved := make([]ImportPath, 0, len(g.importPaths))
	for i, ip := range g.importPaths {
		if ip.ModuleImportPath == "" {
			return nil, fmt.Errorf("import path %d missing ModuleImportPath", i)
		}
		resolvedIP, err := ensureSourceDirectory(ctx, ip)
		if err != nil {
			return nil, err
		}
		g.importPaths[i] = resolvedIP
		resolved = append(resolved, resolvedIP)
	}
	return resolved, nil
}

var chdirMu sync.Mutex

func addGoCommentsForImportPath(r *jsonschema.Reflector, ip ImportPath) error {
	if ip.ModuleImportPath == "" {
		return fmt.Errorf("missing module import path")
	}
	if ip.SourceDirectory == "" {
		return fmt.Errorf("source directory is empty for %s", ip.ModuleImportPath)
	}
	dir := ip.SourceDirectory
	if !filepath.IsAbs(dir) {
		relPath := filepath.Clean(dir)
		if err := r.AddGoComments(ip.ModuleImportPath, relPath); err != nil {
			return err
		}
		sanitizeCommentMap(r.CommentMap)
		return nil
	}
	absDir := filepath.Clean(dir)
	return withWorkingDir(absDir, func() error {
		if err := r.AddGoComments(ip.ModuleImportPath, "."); err != nil {
			return err
		}
		sanitizeCommentMap(r.CommentMap)
		return nil
	})
}

func sanitizeCommentMap(m map[string]string) {
	if m == nil {
		return
	}
	for k, v := range m {
		if sanitized := sanitizeCommentText(v); sanitized != v {
			m[k] = sanitized
		}
	}
}

func sanitizeCommentText(text string) string {
	if text == "" {
		return text
	}
	return strings.Join(strings.Fields(text), " ")
}

func withWorkingDir(dir string, fn func() error) error {
	chdirMu.Lock()
	defer chdirMu.Unlock()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Chdir(cwd)
	}()
	if err := os.Chdir(dir); err != nil {
		return err
	}
	return fn()
}

func ensureSourceDirectory(ctx context.Context, ip ImportPath) (ImportPath, error) {
	if ip.SourceDirectory != "" {
		return ip, nil
	}
	dir, _, err := lookupPackageDir(ctx, ip.ModuleImportPath, "")
	if err != nil {
		return ip, fmt.Errorf("resolve source directory for %s: %w", ip.ModuleImportPath, err)
	}
	if dir == "" {
		return ip, fmt.Errorf("go list returned empty directory for %s", ip.ModuleImportPath)
	}
	ip.SourceDirectory = dir
	return ip, nil
}

func inferLocalImportPath(ctx context.Context, sourceDir string) (ImportPath, error) {
	if sourceDir == "" {
		sourceDir = "./"
	}
	absSourceDir, err := filepath.Abs(sourceDir)
	if err != nil {
		return ImportPath{}, err
	}
	moduleDir, modulePath, err := findModulePath(absSourceDir)
	if err != nil {
		return ImportPath{}, err
	}
	pkgName, err := detectPackageName(absSourceDir)
	if err != nil {
		return ImportPath{}, err
	}
	relPath, err := filepath.Rel(moduleDir, absSourceDir)
	if err != nil {
		return ImportPath{}, err
	}
	importPath := modulePath
	if relPath != "." {
		importPath = path.Join(importPath, filepath.ToSlash(relPath))
	}
	if pkgName != "" && pkgName != "main" {
		if path.Base(importPath) != pkgName {
			importPath = path.Join(importPath, pkgName)
		}
	}
	dir, _, err := lookupPackageDir(ctx, importPath, moduleDir)
	if err != nil {
		baseImportPath := modulePath
		if relPath != "." {
			baseImportPath = path.Join(baseImportPath, filepath.ToSlash(relPath))
		}
		if altDir, _, altErr := lookupPackageDir(ctx, baseImportPath, moduleDir); altErr == nil {
			dir = altDir
			importPath = baseImportPath
		} else {
			dir = absSourceDir
		}
	}
	if dir == "" {
		dir = absSourceDir
	}
	return ImportPath{
		ModuleImportPath: importPath,
		SourceDirectory:  dir,
	}, nil
}

func lookupPackageDir(ctx context.Context, importPath, workDir string) (string, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	format := "{{.Dir}}\t{{.Standard}}"
	cmd := exec.CommandContext(ctx, "go", "list", "-f", format, importPath)
	cmd.Env = os.Environ()
	if workDir != "" {
		cmd.Dir = workDir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false, fmt.Errorf("go list %s failed: %w (output: %s)", importPath, err, bytes.TrimSpace(out))
	}
	line := strings.TrimSpace(string(out))
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	if line == "" {
		return "", false, fmt.Errorf("go list %s returned empty output", importPath)
	}
	parts := strings.Split(line, "\t")
	dir := strings.TrimSpace(parts[0])
	isStd := len(parts) > 1 && parts[1] == "true"
	return dir, isStd, nil
}

func findModulePath(startDir string) (string, string, error) {
	dir := startDir
	for {
		modFile := filepath.Join(dir, "go.mod")
		contents, err := os.ReadFile(modFile)
		if err == nil {
			modulePath, err := parseModuleDirective(contents)
			if err != nil {
				return "", "", err
			}
			return dir, modulePath, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", fmt.Errorf("go.mod not found starting from %s", startDir)
		}
		dir = parent
	}
}

func parseModuleDirective(contents []byte) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return "", fmt.Errorf("invalid module directive: %q", line)
			}
			return fields[1], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("module directive not found in go.mod")
}

func detectPackageName(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		filePath := filepath.Join(dir, name)
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, filePath, nil, parser.PackageClauseOnly)
		if err != nil {
			return "", err
		}
		return parsed.Name.Name, nil
	}
	return "", fmt.Errorf("no go source files found in %s", dir)
}
