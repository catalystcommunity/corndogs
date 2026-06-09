#!/usr/bin/env bash
#
# Build + push the corndogs-test-env runner image. Follows the longhouse
# api-build-and-deploy pattern: with the `docker` capability a daemon is exposed
# via DOCKER_HOST (install the CLI, build, save, crane-push); otherwise fall back
# to a rootless buildkitd. Tags :latest and :<sha>.
set -euo pipefail

LOCAL_BIN="${HOME}/.local/bin"
mkdir -p "${LOCAL_BIN}"
export PATH="${LOCAL_BIN}:${PATH}"

cd /job/src
CONTEXT=".reactorcide/images/corndogs-test-env"
IMAGE="${REGISTRY}/${IMAGE_PATH}"
TAG="${REACTORCIDE_SHA:-latest}"

if ! command -v crane &>/dev/null; then
  echo "=== installing crane ==="
  CRANE_VERSION=0.20.3
  curl -fsSL "https://github.com/google/go-containerregistry/releases/download/v${CRANE_VERSION}/go-containerregistry_Linux_x86_64.tar.gz" -o /tmp/crane.tar.gz
  tar -xzf /tmp/crane.tar.gz -C "${LOCAL_BIN}" crane
  rm /tmp/crane.tar.gz
fi

if [[ -n "${REGISTRY_USER:-}" && -n "${REGISTRY_PASSWORD:-}" ]]; then
  mkdir -p "${HOME}/.docker"
  AUTH=$(printf "%s:%s" "${REGISTRY_USER}" "${REGISTRY_PASSWORD}" | base64 -w 0)
  cat > "${HOME}/.docker/config.json" <<EOF
{ "auths": { "${REGISTRY}": {"auth": "${AUTH}"} } }
EOF
  echo "registry auth configured for ${REGISTRY}"
fi

echo "=== build ${IMAGE}:${TAG} ==="
if [[ -n "${DOCKER_HOST:-}" ]]; then
  if ! command -v docker &>/dev/null; then
    DOCKER_VERSION=27.5.1
    curl -fsSL "https://download.docker.com/linux/static/stable/x86_64/docker-${DOCKER_VERSION}.tgz" -o /tmp/docker.tgz
    tar -xzf /tmp/docker.tgz --strip-components=1 -C "${LOCAL_BIN}" docker/docker
    rm /tmp/docker.tgz
  fi
  for _ in $(seq 1 30); do docker info >/dev/null 2>&1 && break; sleep 1; done

  docker build -t "${IMAGE}:${TAG}" "${CONTEXT}"
  docker save "${IMAGE}:${TAG}" -o /tmp/image.tar
  crane push /tmp/image.tar "${IMAGE}:${TAG}"
  crane push /tmp/image.tar "${IMAGE}:latest"
  rm /tmp/image.tar
else
  if ! command -v buildctl &>/dev/null; then
    BUILDKIT_VERSION=0.17.3
    curl -fsSL "https://github.com/moby/buildkit/releases/download/v${BUILDKIT_VERSION}/buildkit-v${BUILDKIT_VERSION}.linux-amd64.tar.gz" -o /tmp/buildkit.tar.gz
    tar -xzf /tmp/buildkit.tar.gz --strip-components=1 -C "${LOCAL_BIN}"
    rm /tmp/buildkit.tar.gz
  fi
  export XDG_RUNTIME_DIR=/tmp/run-root
  mkdir -p "${XDG_RUNTIME_DIR}"
  buildkitd --oci-worker=true --containerd-worker=false \
    --root="${HOME}/.local/share/buildkit" \
    --addr="unix://${XDG_RUNTIME_DIR}/buildkit/buildkitd.sock" &
  BUILDKITD_PID=$!
  trap "kill ${BUILDKITD_PID} 2>/dev/null || true; wait 2>/dev/null || true" EXIT
  for _ in $(seq 1 30); do
    buildctl --addr="unix://${XDG_RUNTIME_DIR}/buildkit/buildkitd.sock" debug info >/dev/null 2>&1 && break
    sleep 1
  done
  export BUILDKIT_HOST="unix://${XDG_RUNTIME_DIR}/buildkit/buildkitd.sock"
  for t in "${TAG}" latest; do
    buildctl build \
      --frontend dockerfile.v0 \
      --local context="${CONTEXT}" \
      --local dockerfile="${CONTEXT}" \
      --output "type=image,name=${IMAGE}:${t},push=true"
  done
fi

echo "=== pushed ${IMAGE}:${TAG} (+ :latest) ==="
