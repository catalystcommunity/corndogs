#!/usr/bin/env bash

set -euo pipefail

export HOME=/tmp/home
export GOCACHE=/tmp/gocache
export GOMODCACHE=/tmp/gomod
export GOPATH=/tmp/gopath
export GOFLAGS="${GOFLAGS:+${GOFLAGS} }-buildvcs=false"
mkdir -p "$HOME" "$GOCACHE" "$GOMODCACHE" "$GOPATH"

echo "=== install PostgreSQL ==="
sudo apt-get update
sudo apt-get install -y --no-install-recommends postgresql postgresql-client
PG_BIN="$(dirname "$(find /usr/lib/postgresql -name initdb -type f | sort -V | tail -1)")"
export PATH="${PG_BIN}:${PATH}"

echo "=== start PostgreSQL on 127.0.0.1:5432 ==="
PGDATA=/tmp/pgdata
initdb -D "${PGDATA}" --auth=trust --username=postgres
pg_ctl -D "${PGDATA}" -l /tmp/pg.log -o "-k /tmp -h 127.0.0.1 -p 5432" start
for _ in $(seq 1 30); do
  pg_isready -h 127.0.0.1 -p 5432 && break
  sleep 1
done
createdb -h 127.0.0.1 -U postgres corndogs

export DATABASE_HOST=127.0.0.1
export DATABASE_PORT=5432
export DATABASE_USER=postgres
export DATABASE_PASSWORD=unused-trust-auth
export DATABASE_NAME=corndogs
export DATABASE_SSL_MODE=disable

cd "${REACTORCIDE_CODE_DIR:-/job/src}/corndogs"
go build -o /tmp/corndogs .

/tmp/corndogs run &
SERVER_PID=$!
trap 'kill "${SERVER_PID}" >/dev/null 2>&1 || true; pg_ctl -D "${PGDATA}" stop >/dev/null 2>&1 || true' EXIT

for _ in $(seq 1 30); do
  bash -c '(exec 3<>/dev/tcp/127.0.0.1/5080) 2>/dev/null' && break
  sleep 1
done

go test -v ./...
