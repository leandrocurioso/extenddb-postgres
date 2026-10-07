# Shared settings for the scripts in this directory.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER="${CLUSTER:-extenddb-cells}"
CELLS=(cell-1 cell-2 cell-3)

EXTENDDB_IMAGE="ghcr.io/extenddb/extenddb-postgres:0.1.13"
PATRONI_IMAGE="extenddb-lab/patroni:4.1.5-pg17"
APP_IMAGE="extenddb-lab/clients-api:dev"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mxx\033[0m %s\n' "$*" >&2; exit 1; }

need() {
  for bin in "$@"; do
    command -v "$bin" >/dev/null || die "missing required command: $bin"
  done
}

# patronictl inside any cell's Patroni pod (they all see the same cluster).
patronictl() {
  kubectl -n "${PATRONI_CELL:-cell-1}" exec patroni-0 -c patroni -- \
    patronictl -c /etc/patroni/patroni.yml "$@"
}
