# schemator

`schemator` is a Go helper library that turns Go types into JSON Schema definitions. It wraps [github.com/invopop/jsonschema](https://github.com/invopop/jsonschema) and focuses on build-time schema generation so that applications can ship pre-generated schemas alongside the binaries that use them.

## Why schemator?

- **Build-time friendly** – Designed to be used from `go generate` so that schema files are produced as part of your build pipeline.
- **Comment aware** – Adds Go doc comments as JSON Schema `description` fields by calling `Reflector.AddGoComments` for every package involved.
- **Whitespace aware** – After harvesting comments, schemator collapses wrapped lines and newlines so descriptions appear as clean single-line sentences in your final JSON schema.
- **Whitespace aware** – After harvesting comments, schemator collapses wrapped lines and newlines so descriptions appear as clean single-line sentences in your final JSON schema.
- **Automatic import discovery** – When you do not provide any import configuration, schemator inspects the types you generate from and infers all packages (local module, standard library, third-party dependencies) required for comment extraction.
- **Multi-package support** – Manually add extra packages when you want to enrich the generated schema with comments from other modules or custom directories.

The repository includes an end-to-end example under [`example/`](example/) that uses `go generate` to produce `Subject.schema.json` and `Example.schema.json` files.

## Installation

```bash
go get github.com/sa6mwa/schemator
```

## Core Concepts

### ImportPath

`ImportPath` describes a module path and the source directory where schemator should look for Go files to scrape comments from:

```go
schemator.ImportPath{
    ModuleImportPath: "github.com/your/module/example",
    SourceDirectory:  "./internal/example", // optional: schemator can discover this automatically
}
```

You rarely need to add these by hand. Schemator provides helpers to infer them for the current module and any packages referenced by your model types.

### Generator

Create a generator with `schemator.New`. The generator needs:

1. A `context.Context` (used for logging and `go list` lookups).
2. A list of files that **must exist** before generation (optional safeguard).
3. A variadic list of `ImportPath` values (optional; may be empty).

```go
ctx := context.Background()
required := []string{"example.go"}
importPaths := []schemator.ImportPath{
    schemator.InferImportPath(ctx), // discovers the local package
}

// You can omit importPaths entirely and schemator will infer everything based on the model types.

generator := schemator.New(ctx, required, importPaths...)
```

Calling `generator.Generate(model)` returns the JSON Schema bytes for the supplied model type. `WriteSchema` writes a single schema to an explicit path, while `WriteSchemas` takes an output directory and emits one `<Type>.schema.json` file per model.

## Usage Examples

### 1. Minimal standalone generation

```go
package main

import (
    "context"
    "fmt"

    "github.com/sa6mwa/schemator"
)

type Address struct {
    Street string `json:"street"`
    Zip    string `json:"zip"`
}

type Customer struct {
    // CustomerID is the unique identifier of the customer.
    CustomerID int `json:"customerId"`
    // Shipping is the default shipping address.
    Shipping Address `json:"shipping"`
}

func main() {
    ctx := context.Background()
    gen := schemator.New(ctx, nil)

    schema, err := gen.Generate(Customer{})
    if err != nil {
        panic(err)
    }
    fmt.Println(string(schema))
}
```

No import paths were passed to `New`. Schemator inspects `Customer{}` and automatically resolves the module containing `Customer` and the nested `Address` type.

### 2. Build-time generation with `go generate`

Create a small wrapper that lives next to your types:

```go
//go:generate go run ./cmd/schemagen
```

`cmd/schemagen/main.go`:

```go
package main

import (
    "context"

    "github.com/sa6mwa/logport"
    "github.com/sa6mwa/logport/adapters/zerologger"
    "github.com/sa6mwa/schemator"
    "github.com/sa6mwa/schemator/example"
)

func main() {
    logger := zerologger.New(os.Stderr)
    ctx := logport.ContextWithLogger(context.Background(), logger)

    // Fail if required files are missing.
    required := []string{"example.go"}

    // Add the local module plus standard library helpers we know we use.
    gen := schemator.New(ctx, required,
        schemator.InferImportPath(ctx, "./example"),
        schemator.ImportPath{ModuleImportPath: "time"},
    )

    if err := gen.WriteSchemas("schemas", example.Subject{}, example.Example{}); err != nil {
        logger.Fatal("failed to generate schemas", "error", err)
    }
}
```

Running `go generate ./example` will create fresh schema files under `example/schemas/`. See the shipped [`example/`](example/) directory for a working project.

### 3. Pulling in external packages automatically

If your model references types from other modules (for example `github.com/google/uuid.UUID`), schemator automatically resolves and includes those packages the first time you call `Generate`. You can still add them explicitly when you need to override the source directory:

```go
gen := schemator.New(ctx, nil,
    schemator.InferImportPath(ctx),
    schemator.ImportPath{ModuleImportPath: "github.com/google/uuid"},
    schemator.ImportPath{ModuleImportPath: "time"},
)
```

When `SourceDirectory` is omitted, schemator shells out to `go list -f '{{.Dir}}' <module>` to locate the directory. This works for both module-aware and standard-library packages, so no additional handling is required for packages such as `time`.

### 4. Custom directories

Sometimes schema comments live in a directory different from the module root. Supply the path explicitly:

```go
schemator.ImportPath{
    ModuleImportPath: "github.com/acme/contracts",
    SourceDirectory:  "../contracts", // relative to current working directory
}
```

Absolute directories are supported as well; schemator temporarily changes the working directory while scraping comments to keep `AddGoComments` happy.

## Key Helpers

| Helper | Purpose |
| --- | --- |
| `InferImportPath(ctx, sourceDir ...)` | Finds the module import path and directory for the current project or an optional directory. |
| `ImportPaths(paths ...string)` | Convenience helper that turns a list of strings into `[]ImportPath`. |
| `ImportPathsWithLocal(ctx, paths ...string)` | Prepends the local `ImportPath` (via `InferImportPath`) to the supplied modules. |
| `collectDependentPackages(models ...any)` | Internal helper that inspects model types to discover all referenced packages. |

## Logging

Schemator uses [`github.com/sa6mwa/logport`](https://github.com/sa6mwa/logport) for structured logging. Provide a logger in your context if you want insight into import-path resolution, filesystem writes, or `go list` lookups.

## Testing

```bash
go test ./...
```

## License

[MIT](LICENSE)
