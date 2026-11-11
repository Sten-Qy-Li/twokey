// Package twokey holds the data files that ship inside the twokey binary.
// They live in assets/ at the repository root, so they are easy to find
// and edit; go:embed can't reach parent folders, so this root package
// embeds them for the internal packages.
package twokey

import (
	"embed"
	"io/fs"
)

// ConfigTemplate is the config file that "twokey init" writes.
//
//go:embed assets/config.toml
var ConfigTemplate string

//go:embed assets/prompts/*.md assets/schemas/*.json
var files embed.FS

// Prompts holds the default prompt templates (preamble.md, audit.md, ...).
var Prompts = mustSub("assets/prompts")

// Schemas holds the answer schemas (audit.schema.json, ...).
var Schemas = mustSub("assets/schemas")

func mustSub(dir string) fs.FS {
	sub, err := fs.Sub(files, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
