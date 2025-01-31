#!/usr/bin/env bash

set -e

RELEASE_NAME="${RELEASE_NAME:-$1}"
RELEASE_VERSION="${RELEASE_VERSION:-$1}"
GITHUB_TOKEN="${GITHUB_TOKEN:-notarealtoken}"
GITHUB_REPOSITORY="${GITHUB_REPOSITORY:-catalystcommunity/corndogs}"
DEFAULT_GITHUB_API_URL="https://api.github.com"
GITHUB_API_URL="${GITHUB_API_URL:-${DEFAULT_GITHUB_API_URL}}"

CHART_ASSET_NAME="Helm_chart"
CHART_TARFILE_PATH=$(find . -maxdepth 1 -type f -iname "*.tgz")

echo ${RELEASE_NAME}

# Create pre-release
curl -L \
  -X POST \
  -H "Accept: application/vnd.github+json" \
  -H "Authorization: Bearer ${GITHUB_TOKEN}"\
  -H "X-GitHub-Api-Version: 2022-11-28" \
  "${GITHUB_API_URL}/repos/${GITHUB_REPOSITORY}/releases" \
  -d '{"tag_name":"'${RELEASE_NAME}'","target_commitish":"main","name":"'${RELEASE_NAME}'","body":"Released by Github Action","draft":false,"prerelease":true,"generate_release_notes":false}'

TAGGED_RELEASE_ID=$(curl -L \
  -H "Accept: application/vnd.github+json" \
  -H "Authorization: Bearer ${GITHUB_TOKEN}"\
  -H "X-GitHub-Api-Version: 2022-11-28" \
   ${GITHUB_API_URL}/repos/${GITHUB_REPOSITORY}/releases/tags/${RELEASE_NAME} | yq ".id")

# Add chart tarfile to release
curl -L \
  -X POST \
  -H "Accept: application/vnd.github+json" \
  -H "Authorization: Bearer ${GITHUB_TOKEN}"\
  -H "X-GitHub-Api-Version: 2022-11-28" \
  -H "Content-Type: application/octet-stream" \
  "https://uploads.github.com/repos/${GITHUB_REPOSITORY}/releases/${TAGGED_RELEASE_ID}/assets?name=${CHART_ASSET_NAME}" \
  --data-binary "@${CHART_TARFILE_PATH}"

# Change pre-release to release
curl -L \
  -X PATCH \
  -H "Accept: application/vnd.github+json" \
  -H "Authorization: Bearer ${GITHUB_TOKEN}" \
  -H "X-GitHub-Api-Version: 2022-11-28" \
  ${GITHUB_API_URL}/repos/${GITHUB_REPOSITORY}/releases/${TAGGED_RELEASE_ID} \
  -d '{"prerelease": false}'