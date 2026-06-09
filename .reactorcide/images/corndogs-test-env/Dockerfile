# corndogs-test-env — a reactorcide runner image with Go + PostgreSQL + csilgen
# baked in, so the test-server and csil-gen-check jobs need no per-run apt/sudo.
#
# Designed to run correctly as ANY uid (the worker runs jobs as runner/1001;
# `reactorcide run-local` runs as the host uid). Everything baked in is
# world-readable; the jobs write only to /tmp (postgres data dir, Go caches), so
# they don't depend on a writable HOME. See .reactorcide/jobs/test-server.yaml.
#
# Published to: containers.catalystsquad.com/public/catalystcommunity/corndogs-test-env

# ---- stage 1: build csilgen (CLI binary + wasm generators) ----
FROM rust:1-bookworm AS csilgen
ARG CSILGEN_REF=ccc3f02
RUN git clone https://github.com/catalystcommunity/csilgen /src/csilgen
WORKDIR /src/csilgen
RUN git checkout "${CSILGEN_REF}" \
 && rustup target add wasm32-unknown-unknown \
 && cargo build -p csilgen --release \
 && cargo run -p xtask install-wasm
# -> /src/csilgen/target/release/csilgen
# -> /root/.csilgen/generators/*.wasm

# ---- final image ----
FROM containers.catalystsquad.com/public/reactorcide/runnerbase:dev

USER root

# PostgreSQL (bookworm ships 15) + the build essentials corndogs' tests want.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      postgresql postgresql-client ca-certificates curl git \
 && rm -rf /var/lib/apt/lists/*

# Go toolchain.
ARG GO_VERSION=1.23.4
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xz

# csilgen CLI + generators. Generators go in /home/runner/.csilgen so a job that
# sets HOME=/home/runner finds them; made world-readable so any uid can read.
COPY --from=csilgen /src/csilgen/target/release/csilgen /usr/local/bin/csilgen
COPY --from=csilgen /root/.csilgen/generators /home/runner/.csilgen/generators
RUN chmod -R a+rX /home/runner/.csilgen

# go + postgres binaries on PATH for every user.
ENV PATH="/usr/local/go/bin:/usr/lib/postgresql/15/bin:${PATH}"

# Smoke-check the toolchain is wired up at build time.
RUN go version && csilgen --help >/dev/null && initdb --version

USER runner
