#!/usr/bin/env bash

set -e

RELEASE_NAME="${RELEASE_NAME:-$1}"
GITHUB_TOKEN="${GITHUB_TOKEN:-notarealtoken}"
GITHUB_REPOSITORY="${GITHUB_REPOSITORY:-catalystcommunity/semver-tags}"
DEFAULT_GITHUB_API_URL="https://api.github.com"
GITHUB_API_URL="${GITHUB_API_URL:-${DEFAULT_GITHUB_API_URL}}"

echo ${RELEASE_NAME}

curl -L \
  -X POST \
  -H "Accept: application/vnd.github+json" \
  -H "Authorization: Bearer ${GITHUB_TOKEN}"\
  -H "X-GitHub-Api-Version: 2022-11-28" \
  "${GITHUB_API_URL}/repos/${GITHUB_REPOSITORY}/releases" \
  -d '{"tag_name":"'${RELEASE_NAME}'","target_commitish":"main","name":"'${RELEASE_NAME}'","body":"Released by Github Action","draft":false,"prerelease":false,"generate_release_notes":false}'
