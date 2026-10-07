#!/usr/bin/env bash
# Creates the k3d cluster: 1 control-plane server + 3 agents, one agent per
# cell (labelled cell=cell-N), then builds/imports the images.
source "$(dirname "$0")/lib.sh"

K3D_VERSION="${K3D_VERSION:-v5.9.0}"

need docker kubectl curl
docker info >/dev/null 2>&1 || die "cannot talk to Docker (is it running, and are you in the docker group?)"

if ! command -v k3d >/dev/null; then
  log "installing k3d ${K3D_VERSION} into ~/.local/bin"
  mkdir -p "$HOME/.local/bin"
  curl -fsSL https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh \
    | TAG="$K3D_VERSION" K3D_INSTALL_DIR="$HOME/.local/bin" USE_SUDO=false bash
  export PATH="$HOME/.local/bin:$PATH"
fi

if k3d cluster list -o json | grep -q "\"name\":\"${CLUSTER}\""; then
  log "cluster ${CLUSTER} already exists"
else
  log "creating k3d cluster ${CLUSTER}"
  args=(--servers 1 --agents 3 --wait --timeout 180s
        --k3s-arg "--disable=traefik@server:0")
  for i in 0 1 2; do
    cell="cell-$((i + 1))"
    args+=(--k3s-node-label "cell=${cell}@agent:${i}"
           --k3s-node-label "topology.kubernetes.io/zone=${cell}@agent:${i}")
  done
  k3d cluster create "$CLUSTER" "${args[@]}"
fi
kubectl config use-context "k3d-${CLUSTER}" >/dev/null

log "building images"
docker build -t "$PATRONI_IMAGE" "$ROOT/images/patroni"
docker build -t "$APP_IMAGE" "$ROOT/app"
docker pull "$EXTENDDB_IMAGE"

log "importing images into k3d"
# With Docker's containerd image store, a pulled multi-arch image keeps the
# manifests of every platform but only the content of the host's one. Letting
# k3d `docker save` it exports dangling digests and `ctr import` fails on the
# nodes (while k3d still reports success), so save per-platform tarballs.
platform="$(docker version --format '{{.Server.Os}}/{{.Server.Arch}}')"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
tars=()
for img in "$PATRONI_IMAGE" "$APP_IMAGE" "$EXTENDDB_IMAGE"; do
  tar="$tmp/$(echo "$img" | tr '/:' '__').tar"
  docker save --platform "$platform" -o "$tar" "$img"
  tars+=("$tar")
done
k3d image import -c "$CLUSTER" "${tars[@]}"

kubectl get nodes -L cell
