// Package templates embeds the built-in service templates compiled into the
// binary via go:embed (ARCHITECTURE §6, spec 02). Each top-level directory is one
// template; a template name may contain dots (e.g. php.laravel.nginx). The
// templating/generation pipeline consumes this through internal/template's
// FSSource.
package templates

import "embed"

//go:embed all:postgres all:mysql all:mariadb all:mongodb all:cassandra all:arangodb all:redis all:minio all:php.nginx all:php.laravel.nginx all:node.vite all:localstack all:ministack all:nats all:kafka all:rabbitmq all:node.express all:node.nestjs all:node.next all:react.vite all:bun.app all:turborepo all:valkey all:mailpit all:meilisearch all:etcd all:clickhouse all:neo4j all:timescaledb all:mosquitto all:keycloak all:jaeger all:consul all:opensearch all:rustfs all:go.app all:rust.app all:python.app all:python.fastapi all:python.django all:python.flask all:elixir.phoenix all:ruby.rails all:java.spring all:dotnet.aspnet all:deno.app all:smee all:vue.vite all:svelte.kit all:nuxt all:astro
var builtinFS embed.FS

// FS is the embedded built-in templates root: template-name directories at the
// top level, each containing a template.yaml (and an optional build/ tree).
var FS = builtinFS
