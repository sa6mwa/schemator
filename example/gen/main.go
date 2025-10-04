package main

import (
	"context"
	"os"

	"github.com/sa6mwa/logport"
	"github.com/sa6mwa/logport/adapters/zerologger"
	"github.com/sa6mwa/schemator"
	"github.com/sa6mwa/schemator/example"
)

func main() {
	l := zerologger.New(os.Stderr)
	ctx := logport.ContextWithLogger(context.Background(), l)
	mustExist := []string{"example.go"}

	// importPaths := []schemator.ImportPath{
	// 	schemator.InferImportPath(ctx),
	// 	{ModuleImportPath: "github.com/google/uuid"},
	// 	{ModuleImportPath: "time"},
	// }
	// // Or...
	// importPaths := schemator.ImportPathsWithLocal(ctx, "github.com/google/uuid", "time")
	// // Or...
	// generator := schemator.New(ctx, files, schemator.ImportPathsWithLocal(ctx, "github.com/google/uuid", "time")...)

	generator := schemator.New(ctx, mustExist)
	if err := generator.WriteSchemas("schemas/", example.Subject{}, example.Example{}); err != nil {
		l.Fatal("Error generating schemas", "error", err)
	}
}
