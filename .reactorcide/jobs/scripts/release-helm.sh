#!/usr/bin/env bash
#
# Helm chart release. semver-tags analyzes commits under helm_chart/ (scoped with
# --directories) to compute the next `helm_chart/vX.Y.Z` version. We bump
# Chart.yaml `version`, TAG THE BUMP COMMIT (so the tag points at the resulting
# HEAD of main rather than trailing it), package, and publish a GitHub release
# with the .tgz.
#
# Ordering matters: semver-tags is run in --dry_run to compute the version
# WITHOUT tagging; we create the version-bump commit, then create the tag and
# push the branch + tag atomically.
#
# Independent of release-server (separate prefixed tag sequence). runnerbase
# ships only curl/git/bash, so semver-tags, helm, and gh are curl-installed. Runs
# in the dir the job command cloned (an authed full clone of main).
set -euo pipefail

SEMVER_TAGS_VERSION="${SEMVER_TAGS_VERSION:-v0.4.0}"
GHCLI_VERSION="${GHCLI_VERSION:-2.63.2}"

export HOME="${HOME:-/root}"
LOCAL_BIN="${HOME}/.local/bin"
mkdir -p "${LOCAL_BIN}"
export PATH="${LOCAL_BIN}:${PATH}"

git config user.name "catalystcommunityci"
git config user.email "ci@catalystcommunity.org"
git fetch --tags --force origin

echo "=== install semver-tags ${SEMVER_TAGS_VERSION} ==="
curl -fsSL "https://github.com/catalystcommunity/semver-tags/releases/download/${SEMVER_TAGS_VERSION}/semver-tags.tar.gz" -o /tmp/semver-tags.tar.gz
tar -xzf /tmp/semver-tags.tar.gz -C "${LOCAL_BIN}"
chmod +x "${LOCAL_BIN}/semver-tags"

echo "=== compute version bump for helm_chart/ (dry run, no tagging) ==="
# --dry_run computes the next version + tag name but does NOT create or push the
# tag, so we control exactly which commit gets tagged (below).
semver-tags run --dry_run --output_json --directories helm_chart > /tmp/semver.txt 2>&1
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
echo "=== releasing ${NEW_TAG} (version ${VERSION}) ==="

echo "=== bump chart version to ${VERSION}, commit, tag the commit, push ==="
# Create the bump commit FIRST, then tag it, so ${NEW_TAG} points at the commit
# that becomes HEAD of main. The retry loop serializes against a concurrent
# release-server push (it also commits Chart.yaml, a different line). The branch
# and tag are pushed in one --atomic push so the tag never lands without its commit.
sed -i "s/^version: .*/version: \"${VERSION}\"/" helm_chart/chart/Chart.yaml
git add helm_chart/chart/Chart.yaml
git commit -m "ci: bump chart version to ${VERSION}" || echo "nothing to commit (version already ${VERSION})"

pushed=false
for attempt in $(seq 1 5); do
  if ! git pull --rebase origin main; then
    git rebase --abort 2>/dev/null || true
    sleep $((attempt * 3)); continue
  fi
  # Point the tag at the final (possibly rebased) HEAD just before pushing.
  git tag -f "${NEW_TAG}"
  if git push --atomic origin "HEAD:main" "refs/tags/${NEW_TAG}"; then
    pushed=true
    break
  fi
  git tag -d "${NEW_TAG}" 2>/dev/null || true
  sleep $((attempt * 3))
done
if [ "${pushed}" != "true" ]; then
  echo "ERROR: failed to push ${NEW_TAG} + main after retries" >&2
  exit 1
fi

echo "=== install helm + package chart ==="
if ! command -v helm >/dev/null 2>&1; then
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 \
    | USE_SUDO=false HELM_INSTALL_DIR="${LOCAL_BIN}" bash
fi
helm repo add bitnami https://charts.bitnami.com/bitnami
helm dependency update ./helm_chart/chart
helm package ./helm_chart/chart   # produces corndogs-${VERSION}.tgz in cwd

echo "=== publish GitHub release ${NEW_TAG} ==="
if ! command -v gh >/dev/null 2>&1; then
  curl -fsSL "https://github.com/cli/cli/releases/download/v${GHCLI_VERSION}/gh_${GHCLI_VERSION}_linux_amd64.tar.gz" -o /tmp/gh.tar.gz
  tar -xzf /tmp/gh.tar.gz -C /tmp
  cp "/tmp/gh_${GHCLI_VERSION}_linux_amd64/bin/gh" "${LOCAL_BIN}/gh"
fi
GH_TOKEN="${GITHUB_PAT}" gh release create "${NEW_TAG}" \
  --repo "${REACTORCIDE_REPO}" \
  --title "${NEW_TAG}" \
  --notes "Helm chart ${VERSION}" \
  ./corndogs-*.tgz

echo "=== released chart ${NEW_TAG} (version ${VERSION}) ==="
