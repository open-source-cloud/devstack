// Package pluginsdk is the reserved public contract for out-of-tree providers
// (secrets, tunnel, proxy). It is DEFERRED TO v2 (DECISIONS D17): v1 ships all
// providers compiled in-process behind internal Go interfaces, with no
// go-plugin/gRPC and no use of Go's native (OS-limited, brittle) plugin package.
//
// Nothing here is stable yet. Do not depend on it.
package pluginsdk
