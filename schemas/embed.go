// Package schemas embeds the published JSON Schema documents compiled into the
// binary via go:embed (spec 01, DECISIONS D16). They are the editor/agent
// authoring aid — yaml-language-server picks them up through the $schema modeline
// internal/ide writes, and `devstack config schema` prints them for any other
// consumer.
//
// The schemas are HAND-AUTHORED, not generated: validator/v10's tag vocabulary
// (dsname/duration/cpus/platform/dockerhost, oneof, dive) plus the cross-field
// resolvers and the ${env./self./ref:/profile} grammar do not round-trip through
// tag introspection. The Go validator in internal/config stays the source of
// truth; TestSchemaRoundTrip keeps the two aligned by validating every fixture
// through both paths.
package schemas

import "embed"

//go:embed devstack.schema.json workspace.schema.json
var files embed.FS

// FS is the embedded schema root: one <kind>.schema.json per config file kind.
var FS = files
