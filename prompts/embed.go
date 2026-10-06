// Package prompts embeds the agent and extraction prompt templates. Prompts
// live here as files, never inline in Go code.
package prompts

import "embed"

//go:embed *.tmpl *.json
var FS embed.FS
