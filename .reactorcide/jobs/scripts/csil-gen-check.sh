#!/usr/bin/env bash

set -euo pipefail

export HOME=/tmp/home
export CARGO_HOME=/tmp/cargo
mkdir -p "$HOME" "$CARGO_HOME"
cd "${REACTORCIDE_CODE_DIR:-/job/src}"

CSILGEN_RELEASE="$(sed -n 's/^CSILGEN_RELEASE="\([^"]*\)".*/\1/p' csil/generate.sh)"
CSILGEN_TOOLS="$(mktemp -d /tmp/csilgen-tools.XXXXXX)"
echo "=== install ${CSILGEN_RELEASE} ==="
git clone --depth 1 --branch "${CSILGEN_RELEASE}" \
  https://github.com/catalystcommunity/csilgen "${CSILGEN_TOOLS}"
INSTALL_OUTPUT="$("${CSILGEN_TOOLS}/tools.sh" install-all)"
printf '%s\n' "${INSTALL_OUTPUT}"
INSTALLED_RELEASE="$(printf '%s\n' "${INSTALL_OUTPUT}" | sed -n 's/^Installed GitHub Release: //p')"
if [ "${INSTALLED_RELEASE}" != "${CSILGEN_RELEASE}" ]; then
  echo "error: expected ${CSILGEN_RELEASE}; installer selected ${INSTALLED_RELEASE}" >&2
  exit 1
fi
export PATH="${CARGO_HOME}/bin:${PATH}"

csilgen validate --input csil/corndogs.csil
./csil/generate.sh
git diff --exit-code -- clients/ || {
  printf '\nGenerated code is stale. Run ./csil/generate.sh and commit the result.\n'
  exit 1
}
