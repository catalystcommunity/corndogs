#!/usr/bin/env bash
#
# Server release. semver-tags analyzes commits under corndogs/ (scoped with
# --directories) and, on a real bump, creates + pushes a prefixed tag
# `corndogs/vX.Y.Z`. We then build+push the image and bump the chart appVersion.
# Each component (corndogs, helm_chart) has its own prefixed tag sequence, so this
# job and release-helm run independently without colliding.
#
# Publishes to containers.catalystsquad.com/public/catalystcommunity/corndogs.
set -euo pipefail

SEMVER_TAGS_VERSION="${SEMVER_TAGS_VERSION:-v0.4.0}"
GHCLI_VERSION="${GHCLI_VERSION:-2.63.2}"

# Runs in the dir the job command cloned/cd'd into (PWD): an authed full clone of
# main, so origin can push and semver-tags sees full history.
git config user.name "catalystcommunityci"
git config user.email "ci@catalystcommunity.org"
git fetch --tags --force origin

echo "=== install semver-tags ${SEMVER_TAGS_VERSION} ==="
wget -q "https://github.com/catalystcommunity/semver-tags/releases/download/${SEMVER_TAGS_VERSION}/semver-tags.tar.gz" \
  -O /tmp/semver-tags.tar.gz
tar -xzf /tmp/semver-tags.tar.gz -C /tmp
chmod +x /tmp/semver-tags
export PATH="/tmp:${PATH}"

echo "=== compute version bump for corndogs/ ==="
semver-tags run --output_json --directories corndogs > /tmp/semver.txt 2>&1
OUTPUT=$(tail -1 /tmp/semver.txt)
NEW_TAG=$(echo "${OUTPUT}"   | grep -o '"New_release_git_tag":"[^"]*"'  | cut -d'"' -f4)
PUBLISHED=$(echo "${OUTPUT}" | grep -o '"New_release_published":"[^"]*"' | cut -d'"' -f4)

if [ "${PUBLISHED}" != "true" ]; then
  echo "No new server release needed."
  exit 0
fi
# tag is "corndogs/vX.Y.Z" -> VERSION "X.Y.Z"
VERSION="${NEW_TAG##*/}"
VERSION="${VERSION#v}"
echo "=== releasing ${NEW_TAG} (version ${VERSION}) ==="

echo "=== build + push image ==="
IMAGE="${REGISTRY}/${IMAGE_PATH}"
echo "${REGISTRY_PASSWORD}" | docker login "${REGISTRY}" -u "${REGISTRY_USER}" --password-stdin
docker build -t "${IMAGE}:${VERSION}" -t "${IMAGE}:latest" ./corndogs
docker push "${IMAGE}:${VERSION}"
docker push "${IMAGE}:latest"

echo "=== bump chart appVersion to ${VERSION} ==="
sed -i "s/^appVersion: .*/appVersion: \"${VERSION}\"/" helm_chart/chart/Chart.yaml
git add helm_chart/chart/Chart.yaml
if ! git diff --cached --quiet; then
  git commit -m "ci: bump corndogs appVersion to ${VERSION}"
  # release-helm may also be committing Chart.yaml (a different line); rebase to
  # serialize cleanly before pushing.
  git pull --rebase origin main || true
  git push origin main
fi

echo "=== create GitHub release ${NEW_TAG} ==="
wget -q "https://github.com/cli/cli/releases/download/v${GHCLI_VERSION}/gh_${GHCLI_VERSION}_linux_amd64.tar.gz" -O /tmp/gh.tar.gz
tar -xzf /tmp/gh.tar.gz -C /tmp
export PATH="/tmp/gh_${GHCLI_VERSION}_linux_amd64/bin:${PATH}"
GH_TOKEN="${GITHUB_PAT}" gh release create "${NEW_TAG}" \
  --repo "${REACTORCIDE_REPO}" \
  --title "${NEW_TAG}" \
  --generate-notes

echo "=== released ${NEW_TAG} (image ${IMAGE}:${VERSION}) ==="
