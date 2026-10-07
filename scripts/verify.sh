#!/usr/bin/env bash
# Shows the state of every layer and proves the cells share one data set:
# a client registered through cell-1 is read back through cell-2 and cell-3.
source "$(dirname "$0")/lib.sh"
need kubectl

log "consul cluster (one server per cell)"
kubectl -n cell-1 exec consul-0 -- consul members
kubectl -n cell-1 exec consul-0 -- consul operator raft list-peers

log "patroni cluster (leader lock held in consul)"
patronictl list

log "consul service catalog entry registered by patroni"
kubectl -n cell-1 exec consul-0 -- \
  wget -qO- 'http://127.0.0.1:8500/v1/catalog/service/extenddb-pg' \
  | grep -o '"Node":"[^"]*"\|"ServiceTags":\[[^]]*\]' | paste - -

log "pods per cell"
for cell in "${CELLS[@]}"; do
  kubectl -n "$cell" get pods -o wide
done

# Runs curl from inside a cell against that cell's clients-api.
curl_in() { # cell curl-args...
  local cell=$1
  shift
  kubectl -n "$cell" run "verify-$RANDOM" --rm -i --quiet --restart=Never \
    --image=curlimages/curl:8.22.0 --overrides='{"spec":{"nodeSelector":{"cell":"'"$cell"'"}}}' \
    -- curl -sS --fail-with-body "$@"
}

email="verify.$(date +%s)@example.com"
log "registering a client through cell-1 (PutItem + BatchWriteItem)"
created=$(curl_in cell-1 -X POST http://clients-api/clients -H 'Content-Type: application/json' -d '{
  "name": "Maria Verify", "email": "'"$email"'", "document": "12345678901", "segment": "enterprise",
  "addresses": [{"kind": "billing", "street": "Av. Paulista, 1000", "city": "Sao Paulo", "state": "SP", "country": "BR", "postalCode": "01310-100"}],
  "contacts": [{"kind": "mobile", "value": "+55 11 99999-0000"}]
}')
echo "$created"
id=$(grep -o '"id":"[^"]*"' <<<"$created" | head -1 | cut -d'"' -f4)
[[ -n "$id" ]] || die "could not create client"

for cell in cell-2 cell-3; do
  log "reading client $id through $cell"
  curl_in "$cell" "http://clients-api/clients/$id"
  log "looking it up by email through $cell (gsi1)"
  curl_in "$cell" "http://clients-api/clients?email=$email"
done

log "recent simulator activity"
for cell in "${CELLS[@]}"; do
  kubectl -n "$cell" logs deploy/clients-api --tail=2
done
