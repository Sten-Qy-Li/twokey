// Package twokey holds the data files that ship inside the twokey binary.
// They live in assets/ at the repository root, so they are easy to find
// and edit; go:embed can't reach parent folders, so this root package
// embeds them for the internal packages.
package twokey

import _ "embed"

// ConfigTemplate is the config file that "twokey init" writes.
//
//go:embed assets/config.toml
var ConfigTemplate string
