#!/usr/bin/env bash
#
# Generate corndogs clients from csil/corndogs.csil into clients/<lang>/.
#
# Pinned to a known-good csilgen revision so codegen is reproducible (csilgen is
# alpha). Bump CSILGEN_REV deliberately and re-run; the reactorcide
# `csil-gen-check` job fails if committed output drifts from a fresh run.
#
# Requires `csilgen` on PATH (build: `cargo install --path crates/csilgen-cli`
# in ~/repos/catalystcommunity/csilgen, then `cargo run -p xtask install-wasm`).
#
# IMPORTANT: regenerate with the SAME csilgen binary CI uses — the
# corndogs-test-env image (which bakes csilgen at the pinned rev) — e.g.:
#   docker run --rm -v "$PWD:/src" -w /src --entrypoint bash \
#     containers.catalystsquad.com/public/catalystcommunity/corndogs-test-env:latest \
#     -lc 'HOME=/home/runner ./csil/generate.sh'
# A locally-built csilgen can emit byte-different output (e.g. Python import
# ordering) and trip csil-gen-check. See csilgen request
# 2026-06-09-nondeterministic-python-imports.md.
#
# All four languages now generate real, typed, transport-agnostic clients via the
# *-client targets (csilgen requests resolved 2026-06-08). See clients/README.md.
set -euo pipefail

CSILGEN_REV="ccc3f02"   # csilgen git rev this output was generated against

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${HERE}/.." && pwd)"
SPEC="${HERE}/corndogs.csil"
OUT="${ROOT}/clients"

if ! command -v csilgen >/dev/null 2>&1; then
  echo "error: csilgen not found on PATH (rev ${CSILGEN_REV} expected)" >&2
  exit 1
fi

echo "=== validating ${SPEC} ==="
csilgen validate --input "${SPEC}"

# Generated code lands in clients/<lang>/gen/ (wholly owned by csilgen, wiped and
# rewritten each run). Hand-written support files — the per-language CBOR
# transport, package manifests — live in clients/<lang>/ and import from ./gen/,
# so they survive regeneration and there are no stale leftovers. The
# csil-gen-check job diffs clients/ after running this, so output must be
# deterministic.
gen() {
  local target="$1" subdir="$2"
  echo "=== generate ${target} -> clients/${subdir}/gen ==="
  rm -rf "${OUT:?}/${subdir}/gen"
  mkdir -p "${OUT}/${subdir}/gen"
  csilgen generate --input "${SPEC}" --target "${target}" --output "${OUT}/${subdir}/gen"
}

gen typescript-client typescript
gen go-client          go
gen rust-client        rust
gen python-client      python

# Server-side Go for the corndogs server: the `go` (server) target emits the
# typed CorndogsService interface + types (package api). Wholly generated dir.
SERVER_API="${ROOT}/corndogs/server/csilapi"
echo "=== generate go (server) -> corndogs/server/csilapi ==="
rm -rf "${SERVER_API}"
mkdir -p "${SERVER_API}"
csilgen generate --input "${SPEC}" --target go --output "${SERVER_API}"

echo "=== done; clients under ${OUT}/, server types under ${SERVER_API} ==="
