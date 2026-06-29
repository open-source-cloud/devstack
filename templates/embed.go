// Package templates embeds the built-in service templates compiled into the
// binary via go:embed (ARCHITECTURE §6, spec 02). Each top-level directory is one
// template; a template name may contain dots (e.g. php.laravel.nginx). The
// templating/generation pipeline consumes this through internal/template's
// FSSource.
package templates

import "embed"

//go:embed all:postgres all:redis all:minio all:php.nginx all:php.laravel.nginx all:node.vite
var builtinFS embed.FS

// FS is the embedded built-in templates root: template-name directories at the
// top level, each containing a template.yaml (and an optional build/ tree).
var FS = builtinFS
