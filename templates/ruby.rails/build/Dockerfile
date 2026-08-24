# syntax=docker/dockerfile:1
ARG RUBY_VERSION=[[ .params.rubyVersion ]]
FROM ruby:${RUBY_VERSION}

# slim carries no compiler and no client libraries. build-essential covers native
# gem extensions; libpq/libyaml/libvips are the ones a default Rails app reaches
# for (pg, psych, ActiveStorage variants).
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential git pkg-config libpq-dev libyaml-dev libvips \
 && rm -rf /var/lib/apt/lists/*

ENV GEM_HOME=/usr/local/bundle \
    BUNDLE_APP_CONFIG=/usr/local/bundle \
    PATH=/usr/local/bundle/bin:$PATH

WORKDIR /app
EXPOSE [[ .params.port ]]
CMD ["bin/rails", "server", "-b", "0.0.0.0"]
