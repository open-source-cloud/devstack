package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// HTTPSink is a pluggable, best-effort HTTP sink for a self-hoster to POST the
// allowlisted Event as JSON to their OWN collector.
//
// IT IS UNUSED AND DISABLED BY DEFAULT. This build never constructs an HTTPSink
// (DefaultSink returns NoopSink and DefaultEndpoint is empty). It exists only so
// that, once a project-operated collector and its published privacy policy exist,
// wiring real transport is a small, reviewed change rather than a new subsystem.
//
// TODO(spec 20): the real transport should be a pure-Go OTLP/HTTP exporter
// (go.opentelemetry.io/otel + otlploghttp) modeling events as OTel log records on
// the `devstack.cli.command` scope, honoring OTEL_EXPORTER_OTLP_ENDPOINT and a
// deterministic head sampler (errors always sent). This plain JSON POST is a
// scaffold placeholder, deliberately not wired.
type HTTPSink struct {
	// Endpoint is the collector URL. When empty, Send is a no-op (belt-and-braces
	// against accidental activation).
	Endpoint string
	// Client is the HTTP client; a zero value uses a short-timeout default so a
	// hung collector can never block a command.
	Client *http.Client
}

// Send POSTs ev as JSON, best-effort. A missing endpoint, network error, timeout,
// or non-2xx is returned to the caller but the Recorder swallows it — telemetry
// never changes a command's exit code. In this build no caller ever invokes it.
func (s HTTPSink) Send(ctx context.Context, ev Event) error {
	if s.Endpoint == "" {
		return nil // disabled: nothing configured, nothing sent
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 500 * time.Millisecond}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telemetry endpoint returned %s", resp.Status)
	}
	return nil
}
