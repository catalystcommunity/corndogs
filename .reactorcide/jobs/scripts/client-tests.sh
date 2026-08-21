#!/usr/bin/env bash

set -euo pipefail

export HOME=/tmp/home
export GOCACHE=/tmp/gocache
export GOMODCACHE=/tmp/gomod
export GOPATH=/tmp/gopath
export CATALYST_CACHE=/tmp/catalyst-cache
export GOFLAGS="${GOFLAGS:+${GOFLAGS} }-buildvcs=false"
mkdir -p "$HOME" "$GOCACHE" "$GOMODCACHE" "$GOPATH" "$CATALYST_CACHE"

cd "${REACTORCIDE_CODE_DIR:-/job/src}"
LANGS="go python" ./clients/run-tests.sh
