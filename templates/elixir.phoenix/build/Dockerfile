# syntax=docker/dockerfile:1
ARG ELIXIR_VERSION=[[ .params.elixirVersion ]]
FROM elixir:${ELIXIR_VERSION}

# inotify-tools is a HARD dependency of phoenix_live_reload (via the file_system
# package) and is not in the base image. Without it, live reload silently never
# fires — the server runs, the browser just never refreshes.
RUN apt-get update \
 && apt-get install -y --no-install-recommends inotify-tools build-essential git \
 && rm -rf /var/lib/apt/lists/*

# hex and rebar are NOT preinstalled: the official image only builds Elixir from
# source. mix deps.get fails without them.
RUN mix local.hex --force && mix local.rebar --force

WORKDIR /app
EXPOSE [[ .params.port ]]
CMD ["mix", "phx.server"]
