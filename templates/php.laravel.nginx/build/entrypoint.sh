#!/bin/sh
# Laravel container entrypoint. Child template adds this file on top of the
# Dockerfile inherited from php.nginx (demonstrates build-tree merge).
set -e
php artisan migrate --force || true
exec "$@"
