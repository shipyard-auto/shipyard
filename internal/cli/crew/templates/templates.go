// Package templates exposes the embedded scaffolding templates used by the
// `shipyard crew hire` command. The templates live in the core CLI (not in
// the addon) because hire provisions local state before the addon runs.
package templates

import "embed"

//go:embed agent.yaml.tmpl prompt.md.tmpl
var FS embed.FS
