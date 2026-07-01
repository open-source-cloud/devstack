package orchestrate

import (
	"context"
	"strings"
	"time"

	"github.com/open-source-cloud/devstack/internal/provision"
)

// This file hardens the host-side Postgres admin connection against the
// readiness race that surfaced as `db create` failing with "connect to shared
// postgres on 127.0.0.1:<port>: read: connection reset by peer".
//
// Why the race exists: the imperative resource path (and the up provision phase)
// publishes the shared engine's host port via an up-time compose overlay, then
// `docker compose up -d <inst>` applies it. Adding a published port RECREATES the
// container, so Postgres restarts; for a second or two afterwards Docker's
// userland proxy accepts the TCP connection on 127.0.0.1:<port> but the backend
// isn't listening yet, so it RSTs the handshake ("connection reset by peer",
// "failed to receive message", EOF). A single immediate connect loses the race.
//
// The fix mirrors how any client should treat a just-(re)started server: retry
// the connect with backoff for a bounded window. Idempotent and safe — a healthy
// server connects on the first try, so this only ever adds latency on the race.

const (
	// connectRetryBudget bounds how long we retry a transient connect before
	// giving up and surfacing the real error (Postgres genuinely down / wrong
	// creds fail fast because those errors are not transient).
	connectRetryBudget = 30 * time.Second
	// connectRetryStart is the initial backoff; it doubles up to connectRetryMax.
	connectRetryStart = 200 * time.Millisecond
	connectRetryMax   = 2 * time.Second
)

// transientConnErr reports whether a Postgres connect error is the engine still
// coming up after a port-overlay recreate (retry) rather than a permanent
// failure like bad credentials or an unknown database (fail fast).
func transientConnErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, m := range []string{
		"connection reset by peer",
		"connection refused",
		"failed to receive message",
		"the database system is starting up",
		"broken pipe",
		"unexpected eof",
		"eof",
		"i/o timeout",
		"no route to host",
		"server closed the connection unexpectedly",
	} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// sleepFn is indirected so tests can drive the backoff without real time.
var sleepFn = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// nowFn is indirected for tests.
var nowFn = time.Now

// retryingPgConnect wraps a PgConnector so a transient connect error (the engine
// just restarted to bind its host port) is retried with capped backoff for
// connectRetryBudget. A nil connector passes through nil (the default connector
// is substituted downstream). Non-transient errors and a cancelled context
// return immediately.
func retryingPgConnect(connect PgConnector) PgConnector {
	if connect == nil {
		return nil
	}
	return func(ctx context.Context, dsn string) (provision.Conn, func() error, error) {
		deadline := nowFn().Add(connectRetryBudget)
		backoff := connectRetryStart
		for {
			conn, closeFn, err := connect(ctx, dsn)
			if err == nil {
				return conn, closeFn, nil
			}
			if !transientConnErr(err) || !nowFn().Before(deadline) || ctx.Err() != nil {
				return nil, nil, err
			}
			if serr := sleepFn(ctx, backoff); serr != nil {
				return nil, nil, err
			}
			if backoff < connectRetryMax {
				backoff *= 2
			}
		}
	}
}
