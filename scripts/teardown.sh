#!/usr/bin/env bash
# Deletes the k3d cluster. Generated secrets in .secrets/ are kept unless
# --purge is given.
source "$(dirname "$0")/lib.sh"
k3d cluster delete "$CLUSTER"
if [[ "${1:-}" == "--purge" ]]; then
  rm -rf "$ROOT/.secrets"
  log "removed .secrets/"
fi
