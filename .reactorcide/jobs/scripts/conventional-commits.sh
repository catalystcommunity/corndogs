#!/usr/bin/env bash

set -euo pipefail

cd "${REACTORCIDE_CODE_DIR:-/job/src}"

echo "=== validate Conventional Commits ==="
PATTERN='^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert|norelease)(\(.+\))?!?: .+'
FAILED=0
git log "${REACTORCIDE_DIFF_BASE}..HEAD" --pretty=format:"%H %s" > /tmp/commits.txt
while IFS= read -r LINE || [ -n "${LINE}" ]; do
  HASH="${LINE%% *}"
  MSG="${LINE#* }"
  if printf '%s\n' "${MSG}" | grep -qE "${PATTERN}"; then
    echo "OK: ${MSG}"
  else
    echo "FAIL: ${MSG} (${HASH})"
    FAILED=1
  fi
done < /tmp/commits.txt

if [ "${FAILED}" = "1" ]; then
  echo "Commit messages must match: type(scope)?: description"
  echo "Valid types: feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert, norelease"
  exit 1
fi
