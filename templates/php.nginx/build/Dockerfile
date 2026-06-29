# syntax=docker/dockerfile:1
# Rendered by devstack's text engine, which uses custom double-square-bracket
# action delimiters. Everything in $-syntax below is LITERAL and must survive
# templating untouched — this file is the spec-02 acceptance #2 fixture for
# delimiter non-collision.
ARG PHP_VERSION=[[ .params.phpVersion ]]
FROM php:${PHP_VERSION}-fpm

# Literal shell / Dockerfile variables (NOT template actions):
ENV XDEBUG_HOST=${XDEBUG_HOST}
ENV APP_TAG=$TAG
ENV FALLBACK=${VAR:-""}

RUN docker-php-ext-install pdo pdo_pgsql

WORKDIR /var/www/html
CMD ["php-fpm"]
