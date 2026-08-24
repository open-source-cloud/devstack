# syntax=docker/dockerfile:1
ARG RUST_VERSION=[[ .params.rustVersion ]]
FROM rust:${RUST_VERSION}-trixie

# bacon replaces cargo-watch, which is no longer maintained (its own README points
# users at bacon). bacon publishes no prebuilt binaries, so this compiles from
# source and makes the first image build slow — it is cached thereafter.
RUN cargo install --locked bacon

# The official image installs rustup with --profile minimal: no clippy, no
# rustfmt. Add clippy so `bacon -j clippy` works without a rebuild.
RUN rustup component add clippy rustfmt

WORKDIR /app
EXPOSE [[ .params.port ]]
CMD ["bacon", "--headless", "-j", "run-long"]
