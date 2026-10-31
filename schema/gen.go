//go:build ignore

// gen regenerates the JSON Schemas under schema/ from the Go structs:
//
//	go run ./schema/gen.go [dir]
//
// make schema runs it; make schema-check fails when the committed files differ.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/invopop/jsonschema"
	"github.com/xavidop/tamesu/internal/config"
)

func main() {
	dir := "schema"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	for name, s := range map[string]*jsonschema.Schema{
		"tamesu.schema.json":        config.SuiteSchema(),
		"tamesu-config.schema.json": config.ProjectSchema(),
	} {
		b, err := config.SchemaJSON(s)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
	}
}
