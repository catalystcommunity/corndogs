# corndogs-test-env — a reactorcide runner image with Go + PostgreSQL + python3 baked
# in, so the test-server and client-tests jobs need no per-run apt/sudo.
#
# NOTE: csilgen is intentionally NOT baked here. It changes rev frequently, and baking
# it coupled the image to a csilgen rev (every bump needed an image rebuild before
# csil-gen-check could pass). csil-gen-check now BUILDS csilgen from the rev pinned in
# csil/generate.sh on a stock rust image instead — see .reactorcide/jobs/csil-gen-check.yaml.
# This image only carries the stable toolchains (Go, Postgres, python3).
#
# Designed to run correctly as ANY uid (the worker runs jobs as runner/1001;
# `reactorcide run-local` runs as the host uid). Everything baked in is world-readable;
# the jobs write only to /tmp (postgres data dir, Go caches), so they don't depend on a
# writable HOME. See .reactorcide/jobs/test-server.yaml.
#
# Published to: containers.catalystsquad.com/public/catalystcommunity/corndogs-test-env

FROM containers.catalystsquad.com/public/reactorcide/runnerbase:dev

USER root

# PostgreSQL (version depends on the base distro: bookworm=15, trixie=17, ...). The
# server binaries (initdb/pg_ctl/...) live in a version-specific
# /usr/lib/postgresql/<ver>/bin, so symlink them into /usr/local/bin (a stable, on-PATH
# location) rather than hardcoding the version. python3 is for the Python client test.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      postgresql postgresql-client ca-certificates curl git python3 \
 && rm -rf /var/lib/apt/lists/* \
 && PGBIN="$(dirname "$(find /usr/lib/postgresql -name initdb -type f | sort -V | tail -1)")" \
 && for b in initdb pg_ctl createdb pg_isready postgres psql; do ln -sf "${PGBIN}/${b}" "/usr/local/bin/${b}"; done

# Go toolchain.
ARG GO_VERSION=1.25.7
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xz

# Go on PATH; postgres binaries are symlinked into /usr/local/bin above.
ENV PATH="/usr/local/go/bin:${PATH}"

# Smoke-check the toolchain is wired up at build time.
RUN go version && python3 --version && initdb --version

USER runner
