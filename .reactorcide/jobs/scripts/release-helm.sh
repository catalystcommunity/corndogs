#!/usr/bin/env bash
#
# Helm chart release. semver-tags analyzes commits under helm_chart/ (scoped with
# --directories) and, on a real bump, creates + pushes a prefixed tag
# `helm_chart/vX.Y.Z`. We then bump Chart.yaml `version`, package, and publish a
# GitHub release with the .tgz. Independent of release-server (separate prefixed
# tag sequence). Mirrors the old helm_release.yaml + upload_chart.yaml flow.
set -euo pipefail

SEMVER_TAGS_VERSION="${SEMVER_TAGS_VERSION:-v0.4.0}"
GHCLI_VERSION="${GHCLI_VERSION:-2.63.2}"

cd /job/src

# semver-tags pushes the tag (and main) via `origin`.
git config user.name "catalystcommunityci"
git config user.email "ci@catalystcommunity.org"
git remote set-url origin "https://x-access-token:${GITHUB_PAT}@github.com/${REACTORCIDE_REPO}.git"
git fetch --unshallow origin 2>/dev/null || true
git fetch --tags --force origin

echo "=== install semver-tags ${SEMVER_TAGS_VERSION} ==="
wget -q "https://github.com/catalystcommunity/semver-tags/releases/download/${SEMVER_TAGS_VERSION}/semver-tags.tar.gz" \
  -O /tmp/semver-tags.tar.gz
tar -xzf /tmp/semver-tags.tar.gz -C /tmp
chmod +x /tmp/semver-tags
export PATH="/tmp:${PATH}"

echo "=== compute version bump for helm_chart/ ==="
semver-tags run --output_json --directories helm_chart > /tmp/semver.txt 2>&1
OUTPUT=$(tail -1 /tmp/semver.txt)
NEW_TAG=$(echo "${OUTPUT}"   | grep -o '"New_release_git_tag":"[^"]*"'  | cut -d'"' -f4)
PUBLISHED=$(echo "${OUTPUT}" | grep -o '"New_release_published":"[^"]*"' | cut -d'"' -f4)

if [ "${PUBLISHED}" != "true" ]; then
  echo "No new chart release needed."
  exit 0
fi
# tag is "helm_chart/vX.Y.Z" -> VERSION "X.Y.Z"
VERSION="${NEW_TAG##*/}"
VERSION="${VERSION#v}"

echo "=== bump chart version to ${VERSION} ==="
sed -i "s/^version: .*/version: \"${VERSION}\"/" helm_chart/chart/Chart.yaml
git add helm_chart/chart/Chart.yaml
if ! git diff --cached --quiet; then
  git commit -m "ci: bump chart version to ${VERSION}"
  # release-server may also be committing Chart.yaml (a different line); rebase to
  # serialize cleanly before pushing.
  git pull --rebase origin main || true
  git push origin main
fi

echo "=== package chart ==="
helm repo add bitnami https://charts.bitnami.com/bitnami
helm dependency update ./helm_chart/chart
helm package ./helm_chart/chart   # produces corndogs-${VERSION}.tgz in cwd

echo "=== publish GitHub release with chart asset ==="
wget -q "https://github.com/cli/cli/releases/download/v${GHCLI_VERSION}/gh_${GHCLI_VERSION}_linux_amd64.tar.gz" -O /tmp/gh.tar.gz
tar -xzf /tmp/gh.tar.gz -C /tmp
export PATH="/tmp/gh_${GHCLI_VERSION}_linux_amd64/bin:${PATH}"
GH_TOKEN="${GITHUB_PAT}" gh release create "${NEW_TAG}" \
  --repo "${REACTORCIDE_REPO}" \
  --title "${NEW_TAG}" \
  --notes "Helm chart ${VERSION}" \
  ./corndogs-*.tgz

echo "=== released chart ${NEW_TAG} (version ${VERSION}) ==="
