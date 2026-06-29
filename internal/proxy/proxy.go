// Package proxy is the single source of truth for local (and, later, public)
// HTTP routing (spec 05). It builds a []Route from the workspace config; that one
// table renders BOTH the Caddy reverse-proxy labels (caddy-docker-proxy, emitted
// onto each project service so adding/removing a project never edits central
// config) AND — later, in internal/tunnel — the cloudflared ingress block, so
// local and public routing can never drift.
//
// Routing is opt-in: routes exist only when the workspace declares a proxy engine
// (network.proxy.engine: caddy). A broken/absent proxy never blocks `up`.
package proxy

import (
	"fmt"
	"sort"

	"github.com/open-source-cloud/devstack/internal/config"
)

// Engine names. Only Caddy ships in v1; Traefik/nginx stay pluggable.
const EngineCaddy = "caddy"

// Domain suffix for local routing (spec 05). `.test` is opt-in behind `dns setup`.
const LocalDomain = "localhost"

// Route is one host→service mapping. Upstream resolution for Caddy is implicit
// (caddy-docker-proxy discovers the container from its own labels via
// {{upstreams <port>}}); Project/Service identify the owning container and Port
// is the in-container port. TLS requests an internal-CA cert (httpsLocal).
type Route struct {
	Project string `json:"project"`
	Service string `json:"service"`
	Host    string `json:"host"` // <service>.<project>.localhost
	Port    int    `json:"port"`
	TLS     bool   `json:"tls"`
}

// Enabled reports whether the workspace has a (supported) reverse proxy declared.
func Enabled(m *config.Model) bool {
	return m.Workspace.Network.Proxy.Engine == EngineCaddy
}

// BuildRoutes derives the deterministic route table from every project service
// that exposes a port, when the proxy is enabled. Returns nil when disabled.
func BuildRoutes(m *config.Model) []Route {
	if !Enabled(m) {
		return nil
	}
	tls := m.Workspace.Network.Proxy.HTTPSLocal
	var routes []Route
	for _, pname := range sortedKeys(m.Projects) {
		p := m.Projects[pname]
		for _, sname := range sortedKeys(p.Services) {
			port := primaryPort(p.Services[sname].Ports)
			if port == 0 {
				continue
			}
			routes = append(routes, Route{
				Project: pname,
				Service: sname,
				Host:    HostFor(sname, pname),
				Port:    port,
				TLS:     tls,
			})
		}
	}
	return routes
}

// HostFor returns the local host for a service: <service>.<project>.localhost.
func HostFor(service, project string) string {
	return service + "." + project + "." + LocalDomain
}

// LabelsForService returns the Caddy labels a generated project service should
// carry when the proxy is enabled and the service exposes a port; nil otherwise.
// This is the single seam generate uses to emit routing (no central config).
func LabelsForService(m *config.Model, project, service string) map[string]string {
	if !Enabled(m) {
		return nil
	}
	p, ok := m.Projects[project]
	if !ok {
		return nil
	}
	port := primaryPort(p.Services[service].Ports)
	if port == 0 {
		return nil
	}
	return CaddyLabels(Route{
		Project: project, Service: service,
		Host: HostFor(service, project), Port: port,
		TLS: m.Workspace.Network.Proxy.HTTPSLocal,
	})
}

// CaddyLabels renders the caddy-docker-proxy labels for a route. These are merged
// onto the project service in the generated compose so caddy reloads on the
// Docker event with no central-config edit (spec 05).
func CaddyLabels(r Route) map[string]string {
	out := map[string]string{
		"caddy":               r.Host,
		"caddy.reverse_proxy": fmt.Sprintf("{{upstreams %d}}", r.Port),
	}
	if r.TLS {
		out["caddy.tls"] = "internal"
	}
	return out
}

// URLs returns the user-facing URLs for a route (scheme by TLS).
func (r Route) URL() string {
	scheme := "http"
	if r.TLS {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// primaryPort picks a deterministic port for a service: the "http" port if named,
// else the lowest port number. 0 when the service exposes none.
func primaryPort(ports map[string]int) int {
	if len(ports) == 0 {
		return 0
	}
	if p, ok := ports["http"]; ok && p > 0 {
		return p
	}
	best := 0
	for _, p := range ports {
		if p > 0 && (best == 0 || p < best) {
			best = p
		}
	}
	return best
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
