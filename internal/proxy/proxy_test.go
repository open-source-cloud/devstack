package proxy

import (
	"testing"

	"github.com/open-source-cloud/devstack/internal/config"
)

func modelWith(proxy config.Proxy, services map[string]map[string]int) *config.Model {
	projects := map[string]config.Project{}
	svcs := map[string]config.Service{}
	for sname, ports := range services {
		svcs[sname] = config.Service{Template: "t", Ports: ports}
	}
	projects["shop"] = config.Project{Name: "shop", Services: svcs}
	return &config.Model{
		Workspace: config.Workspace{Name: "ws", Network: config.Network{Proxy: proxy}},
		Projects:  projects,
	}
}

func TestEnabled(t *testing.T) {
	if Enabled(modelWith(config.Proxy{}, nil)) {
		t.Error("no engine → disabled")
	}
	if !Enabled(modelWith(config.Proxy{Engine: "caddy"}, nil)) {
		t.Error("caddy engine → enabled")
	}
	if Enabled(modelWith(config.Proxy{Engine: "traefik"}, nil)) {
		t.Error("unsupported engine → disabled in v1")
	}
}

func TestBuildRoutesDisabled(t *testing.T) {
	m := modelWith(config.Proxy{}, map[string]map[string]int{"api": {"http": 8080}})
	if r := BuildRoutes(m); r != nil {
		t.Errorf("disabled proxy → no routes, got %v", r)
	}
}

func TestBuildRoutesPrimaryPortAndTLS(t *testing.T) {
	m := modelWith(config.Proxy{Engine: "caddy", HTTPSLocal: true}, map[string]map[string]int{
		"api": {"http": 8080, "metrics": 9090},
		"web": {"x": 5173},
		"job": {}, // no ports → no route
	})
	routes := BuildRoutes(m)
	if len(routes) != 2 {
		t.Fatalf("routes = %d, want 2 (api, web; job has no port)", len(routes))
	}
	// Deterministic order (sorted by service): api then web.
	if routes[0].Service != "api" || routes[0].Port != 8080 {
		t.Errorf("route[0] = %+v, want api:8080 (http port preferred)", routes[0])
	}
	if routes[0].Host != "api.shop.localhost" {
		t.Errorf("host = %q, want api.shop.localhost", routes[0].Host)
	}
	if !routes[0].TLS {
		t.Error("httpsLocal → TLS true")
	}
	if routes[1].Service != "web" || routes[1].Port != 5173 {
		t.Errorf("route[1] = %+v, want web:5173 (lowest port)", routes[1])
	}
}

func TestCaddyLabels(t *testing.T) {
	l := CaddyLabels(Route{Host: "api.shop.localhost", Port: 8080, TLS: true})
	if l["caddy"] != "api.shop.localhost" {
		t.Errorf("caddy = %q", l["caddy"])
	}
	if l["caddy.reverse_proxy"] != "{{upstreams 8080}}" {
		t.Errorf("reverse_proxy = %q", l["caddy.reverse_proxy"])
	}
	if l["caddy.tls"] != "internal" {
		t.Errorf("tls = %q, want internal", l["caddy.tls"])
	}
	// No TLS → no caddy.tls label.
	if _, ok := CaddyLabels(Route{Host: "h", Port: 1})["caddy.tls"]; ok {
		t.Error("non-TLS route should not emit caddy.tls")
	}
}

func TestRouteURL(t *testing.T) {
	if (Route{Host: "h", TLS: true}).URL() != "https://h" {
		t.Error("TLS route URL should be https")
	}
	if (Route{Host: "h"}).URL() != "http://h" {
		t.Error("non-TLS route URL should be http")
	}
}
