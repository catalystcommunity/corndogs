#!/usr/bin/env bash
#
# Server release. semver-tags analyzes commits under corndogs/ (scoped with
# --directories) to compute the next `corndogs/vX.Y.Z` version. We then build +
# push the image, bump the chart appVersion, and TAG THE BUMP COMMIT so the tag
# points at the resulting HEAD of main (not the pre-bump commit).
#
# Ordering matters: semver-tags is run in --dry_run to compute the version
# WITHOUT tagging; we create the appVersion-bump commit, then create the tag and
# push the branch + tag atomically. This keeps `corndogs/vX.Y.Z` pointing at the
# commit that is HEAD of main, instead of trailing it by the bump commit.
#
# Independent of release-helm (separate prefixed tag sequence). The runnerbase
# image ships only curl/git/bash, so semver-tags, the docker CLI, crane, and gh
# are all curl-installed. Runs in the dir the job command cloned (an authed full
# clone of main).
set -euo pipefail

SEMVER_TAGS_VERSION="${SEMVER_TAGS_VERSION:-v0.4.0}"
GHCLI_VERSION="${GHCLI_VERSION:-2.63.2}"
CRANE_VERSION="${CRANE_VERSION:-0.20.3}"
DOCKER_VERSION="${DOCKER_VERSION:-27.5.1}"

export HOME="${HOME:-/root}"
LOCAL_BIN="${HOME}/.local/bin"
mkdir -p "${LOCAL_BIN}" "${HOME}/.docker"
export PATH="${LOCAL_BIN}:${PATH}"

git config user.name "catalystcommunityci"
git config user.email "ci@catalystcommunity.org"
git fetch --tags --force origin

echo "=== install semver-tags ${SEMVER_TAGS_VERSION} ==="
curl -fsSL "https://github.com/catalystcommunity/semver-tags/releases/download/${SEMVER_TAGS_VERSION}/semver-tags.tar.gz" -o /tmp/semver-tags.tar.gz
tar -xzf /tmp/semver-tags.tar.gz -C "${LOCAL_BIN}"
chmod +x "${LOCAL_BIN}/semver-tags"

echo "=== compute version bump for corndogs/ (dry run, no tagging) ==="
# --dry_run computes the next version + tag name but does NOT create or push the
# tag, so we control exactly which commit gets tagged (below).
semver-tags run --dry_run --output_json --directories corndogs > /tmp/semver.txt 2>&1
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

IMAGE="${REGISTRY}/${IMAGE_PATH}"

echo "=== install crane ==="
if ! command -v crane >/dev/null 2>&1; then
  curl -fsSL "https://github.com/google/go-containerregistry/releases/download/v${CRANE_VERSION}/go-containerregistry_Linux_x86_64.tar.gz" -o /tmp/crane.tar.gz
  tar -xzf /tmp/crane.tar.gz -C "${LOCAL_BIN}" crane
  rm /tmp/crane.tar.gz
fi

# Registry auth for crane (docker config.json).
AUTH=$(printf "%s:%s" "${REGISTRY_USER}" "${REGISTRY_PASSWORD}" | base64 -w 0)
cat > "${HOME}/.docker/config.json" <<EOF
{ "auths": { "${REGISTRY}": {"auth": "${AUTH}"} } }
EOF

echo "=== build + push ${IMAGE}:${VERSION} ==="
if [ -z "${DOCKER_HOST:-}" ]; then
  echo "ERROR: DOCKER_HOST not set (this job needs the 'docker' capability)" >&2
  exit 1
fi
if ! command -v docker >/dev/null 2>&1; then
  curl -fsSL "https://download.docker.com/linux/static/stable/x86_64/docker-${DOCKER_VERSION}.tgz" -o /tmp/docker.tgz
  tar -xzf /tmp/docker.tgz --strip-components=1 -C "${LOCAL_BIN}" docker/docker
  rm /tmp/docker.tgz
fi
for _ in $(seq 1 30); do docker info >/dev/null 2>&1 && break; sleep 1; done
docker build -t "${IMAGE}:${VERSION}" ./corndogs
docker save "${IMAGE}:${VERSION}" -o /tmp/image.tar
crane push /tmp/image.tar "${IMAGE}:${VERSION}"
crane push /tmp/image.tar "${IMAGE}:latest"
rm /tmp/image.tar

echo "=== bump chart appVersion to ${VERSION}, commit, tag the commit, push ==="
# Create the bump commit FIRST, then tag it, so ${NEW_TAG} points at the commit
# that becomes HEAD of main. The retry loop serializes against a concurrent
# release-helm push (it also commits Chart.yaml, a different line). The branch
# and tag are pushed in one --atomic push so the tag never lands without its commit.
sed -i "s/^appVersion: .*/appVersion: \"${VERSION}\"/" helm_chart/chart/Chart.yaml
git add helm_chart/chart/Chart.yaml
git commit -m "ci: bump corndogs appVersion to ${VERSION}" || echo "nothing to commit (appVersion already ${VERSION})"

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

echo "=== create GitHub release ${NEW_TAG} ==="
if ! command -v gh >/dev/null 2>&1; then
  curl -fsSL "https://github.com/cli/cli/releases/download/v${GHCLI_VERSION}/gh_${GHCLI_VERSION}_linux_amd64.tar.gz" -o /tmp/gh.tar.gz
  tar -xzf /tmp/gh.tar.gz -C /tmp
  cp "/tmp/gh_${GHCLI_VERSION}_linux_amd64/bin/gh" "${LOCAL_BIN}/gh"
fi
GH_TOKEN="${GITHUB_PAT}" gh release create "${NEW_TAG}" \
  --repo "${REACTORCIDE_REPO}" \
  --title "${NEW_TAG}" \
  --generate-notes

echo "=== released ${NEW_TAG} (image ${IMAGE}:${VERSION}) ==="
