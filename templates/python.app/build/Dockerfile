# syntax=docker/dockerfile:1
ARG PYTHON_VERSION=[[ .params.pythonVersion ]]
FROM python:${PYTHON_VERSION}-slim

# uv resolves and installs far faster than pip, which matters because these
# templates re-sync dependencies on every container start.
RUN pip install --no-cache-dir uv

# A venv outside /app, so the bind mount cannot shadow it.
ENV UV_PROJECT_ENVIRONMENT=/opt/venv \
    VIRTUAL_ENV=/opt/venv \
    PATH=/opt/venv/bin:$PATH
RUN uv venv /opt/venv

# NOTE: -slim carries no gcc and no -dev headers. A dependency without a
# manylinux/aarch64 wheel (mysqlclient, python-ldap, old psycopg2) will fail to
# install — prefer psycopg[binary] / *-binary wheels, or add build-essential here.

WORKDIR /app
EXPOSE [[ .params.port ]]
