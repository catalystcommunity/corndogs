#!/usr/bin/env bash
#
# Generate the transport-neutral client code. Corndogs supplies its TCP transport
# and README for each language.
#
# Use this csilgen revision so that generation is reproducible. Install csilgen
# at the same revision before you run this script:
#   git -C <csilgen> checkout <CSILGEN_REV> && cargo build -p csilgen --release \
#     && cargo run -p xtask install-wasm   # then `csilgen` on PATH + ./csil/generate.sh
set -euo pipefail

CSILGEN_REV="abac53b"   # csilgen git rev this output was generated against (matches the image's ARG CSILGEN_REF)

# Go is generated separately because the server also uses its service interface.
LANGUAGES=(rust typescript python java csharp c swift kotlin zig ocaml elixir ruby dart)

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

# Preserve the Corndogs TCP transports and READMEs when csilgen replaces a package.
declare -A CORNDOGS_CARRIERS
CORNDOGS_CARRIERS[python]="corndogs/transport.py corndogs/transport_async.py README.md"
CORNDOGS_CARRIERS[typescript]="transport.ts README.md package.json"
CORNDOGS_CARRIERS[rust]="src/transport.rs README.md src/lib.rs"
CORNDOGS_CARRIERS[csharp]="Transport.cs TransportAsync.cs README.md"
CORNDOGS_CARRIERS[dart]="lib/transport.dart README.md"
CORNDOGS_CARRIERS[java]="src/main/java/csilgen/generated/TcpTransport.java README.md"
CORNDOGS_CARRIERS[kotlin]="src/main/kotlin/community/catalyst/csilgen/generated/TcpTransport.kt README.md"
CORNDOGS_CARRIERS[ruby]="lib/transport.rb README.md"
CORNDOGS_CARRIERS[elixir]="lib/transport.ex README.md"
CORNDOGS_CARRIERS[c]="transport.h transport.c README.md"
CORNDOGS_CARRIERS[zig]="transport.zig README.md"
CORNDOGS_CARRIERS[ocaml]="lib/transport.ml README.md lib/dune"
CORNDOGS_CARRIERS[swift]="Sources/Corndogs/Transport.swift README.md"
# These files also contain the changes that expose their TCP transports.

for lang in "${LANGUAGES[@]}"; do
  echo "=== package ${lang}-client -> clients/${lang} ==="
  bak="$(mktemp -d)"
  for f in ${CORNDOGS_CARRIERS[$lang]:-}; do
    if [ -f "${OUT}/${lang}/${f}" ]; then
      mkdir -p "$(dirname "${bak}/${f}")"
      cp "${OUT}/${lang}/${f}" "${bak}/${f}"
    fi
  done
  rm -rf "${OUT:?}/${lang}"
  mkdir -p "${OUT}/${lang}"
  csilgen generate --input "${SPEC}" --target "${lang}-client" --output "${OUT}/${lang}"
  for f in ${CORNDOGS_CARRIERS[$lang]:-}; do
    if [ -f "${bak}/${f}" ]; then
      mkdir -p "$(dirname "${OUT}/${lang}/${f}")"
      cp "${bak}/${f}" "${OUT}/${lang}/${f}"
    fi
  done
  rm -rf "${bak}"
done

# The server imports the generated Go module through a local replace directive.
GO_LIB="${OUT}/corndogs"
echo "=== package go (all surfaces) -> clients/corndogs ==="
mkdir -p "${GO_LIB}"
rm -f "${GO_LIB}"/*.gen.go "${GO_LIB}/go.mod" "${GO_LIB}/genquickstart.md"
csilgen generate --input "${SPEC}" --target go --output "${GO_LIB}"

echo "=== done; every client packaged under ${OUT}/ (Go module at ${GO_LIB}) ==="
