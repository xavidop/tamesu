//go:build ignore

// gen regenerates the JSON Schemas under schema/ and the suites reference page
// of the docs site from the Go structs:
//
//	go run ./schema/gen.go [schema-dir] [suites-page]
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
	page := filepath.Join("site", "src", "pages", "docs", "suites.md")
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if len(os.Args) > 2 {
		page = os.Args[2]
	}
	for name, s := range map[string]*jsonschema.Schema{
		"tamesu.schema.json":        config.SuiteSchema(),
		"tamesu-config.schema.json": config.ProjectSchema(),
	} {
		b, err := config.SchemaJSON(s)
		check(err)
		check(os.MkdirAll(dir, 0o755))
		check(os.WriteFile(filepath.Join(dir, name), b, 0o644))
	}
	check(os.MkdirAll(filepath.Dir(page), 0o755))
	check(os.WriteFile(page, config.SuitesMarkdown(), 0o644))
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}
