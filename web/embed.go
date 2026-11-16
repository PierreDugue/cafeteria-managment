// Package web holds the HTML templates, static assets and translations,
// embedded in the binary.
package web

import "embed"

//go:embed templates static locales
var FS embed.FS
