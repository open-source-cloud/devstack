package resource

import (
	"context"
	"fmt"
)

// This file is the Redis Provisioner (spec 29 §messaging — a lightweight,
// server-object-free queue/topic backend). A Redis "queue" is a LIST/STREAM key
// namespace the app honours (created on first LPUSH/XADD), and a "topic" is a
// Pub/Sub channel namespace — neither needs a server-side create call, so Ensure
// records nothing on the engine and only surfaces the tenant-scoped key + a
// connection hint. Tenant isolation comes from the transparently PROJECT-PREFIXED
// key. There is no external tool and no in-process client: this stays trivially
// inside the single static binary.

// Redis is the redis Provisioner (queue/topic key namespaces). It holds no client.
type Redis struct{}

var _ Provisioner = Redis{}

// Engine reports the shared-template capability this provisioner serves.
func (Redis) Engine() string { return "redis" }

// Kinds are the resource kinds this provisioner can create.
func (Redis) Kinds() []string { return []string{"queue", "topic"} }

// Ensure is a no-op on the engine: a Redis list/stream/channel is created lazily on
// first use. It returns the tenant-scoped key + a connection hint so the caller can
// print how the app reaches it. Idempotent by construction.
func (Redis) Ensure(_ context.Context, t Target, r Resource) (Attrs, error) {
	name := r.Name
	if name == "" {
		name = r.Owner
	}
	kind := r.Kind
	if kind == "" {
		kind = "queue"
	}
	// Logical key convention: <name> is already tenant-prefixed by the CLI.
	key := name
	hint := fmt.Sprintf("LPUSH %s … / BRPOP %s 0", key, key)
	if kind == "topic" {
		hint = fmt.Sprintf("PUBLISH %s … / SUBSCRIBE %s", key, key)
	}
	return Attrs{
		"host": sharedHost(t.Instance),
		"port": "6379",
		"key":  key,
		"type": kind,
		"hint": hint,
	}, nil
}

// Drop is a no-op on the engine (a Redis list/channel has no durable schema object
// to delete beyond its data, which the app owns). Idempotent.
func (Redis) Drop(_ context.Context, _ Target, _ Resource) error { return nil }

// Preflight always succeeds: the Redis queue/topic path needs no admin client.
func (Redis) Preflight(_ context.Context, _ Target) error { return nil }
