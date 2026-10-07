#!/usr/bin/env bash
# Moves the Patroni primary to another cell and shows the apps riding through.
#   scripts/failover.sh            # switchover to the next healthy replica
#   scripts/failover.sh cell-3     # switchover to a specific cell
#   scripts/failover.sh --kill     # hard-kill the current primary's pod
source "$(dirname "$0")/lib.sh"
need kubectl jq

current_leader() {
  PATRONI_CELL=${PATRONI_CELL:-cell-1} patronictl list -f json 2>/dev/null \
    | jq -r '.[] | select(.Role == "Leader") | .Member'
}

leader=$(current_leader)
[[ -n "$leader" ]] || die "no Patroni leader found"
log "current primary: $leader"

if [[ "${1:-}" == "--kill" ]]; then
  log "deleting patroni-0 in $leader (unplanned failover)"
  kubectl -n "$leader" delete pod patroni-0 --wait=false
  # Ask a surviving cell, since the old leader's pod is going away.
  for cell in "${CELLS[@]}"; do [[ "$cell" != "$leader" ]] && PATRONI_CELL=$cell && break; done
else
  args=(switchover --leader "$leader" --force)
  [[ -n "${1:-}" ]] && args+=(--candidate "$1")
  patronictl "${args[@]}"
fi

new_leader=""
for _ in $(seq 1 24); do
  sleep 5
  new_leader=$(current_leader || true)
  echo "leader: ${new_leader:-<none>}"
  [[ -n "$new_leader" && "$new_leader" != "$leader" ]] && break
done
[[ -n "$new_leader" && "$new_leader" != "$leader" ]] || die "primary did not move off $leader"
log "new primary: $new_leader"
PATRONI_CELL=${PATRONI_CELL:-cell-1} patronictl list

log "clients-api logs since the failover"
for cell in "${CELLS[@]}"; do
  kubectl -n "$cell" logs deploy/clients-api --since=2m | tail -n 4
done
