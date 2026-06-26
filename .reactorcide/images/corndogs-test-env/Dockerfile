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

# PostgreSQL (version depends on the base distro: bookworm=15, trixie=17, ...) +
# build essentials. The server binaries (initdb/pg_ctl/...) live in a
# version-specific /usr/lib/postgresql/<ver>/bin, so symlink them into
# /usr/local/bin (a stable, on-PATH location) rather than hardcoding the version.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      postgresql postgresql-client ca-certificates curl git \
 && rm -rf /var/lib/apt/lists/* \
 && PGBIN="$(dirname "$(find /usr/lib/postgresql -name initdb -type f | sort -V | tail -1)")" \
 && for b in initdb pg_ctl createdb pg_isready postgres psql; do ln -sf "${PGBIN}/${b}" "/usr/local/bin/${b}"; done

# Go toolchain.
ARG GO_VERSION=1.25.0
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xz

# csilgen CLI + generators. Generators go in /home/runner/.csilgen so a job that
# sets HOME=/home/runner finds them; made world-readable so any uid can read.
COPY --from=csilgen /src/csilgen/target/release/csilgen /usr/local/bin/csilgen
COPY --from=csilgen /root/.csilgen/generators /home/runner/.csilgen/generators
RUN chmod -R a+rX /home/runner/.csilgen

# Go on PATH; postgres binaries are symlinked into /usr/local/bin above.
ENV PATH="/usr/local/go/bin:${PATH}"

# Smoke-check the toolchain is wired up at build time.
RUN go version && csilgen --help >/dev/null && initdb --version

USER runner
