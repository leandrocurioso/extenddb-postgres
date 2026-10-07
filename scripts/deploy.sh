#!/usr/bin/env bash
# Deploys the three cells in dependency order:
#   consul -> patroni -> pg-router -> extenddb init -> extenddb
#          -> extenddb provision -> clients-api
source "$(dirname "$0")/lib.sh"
need kubectl openssl jq

SECRETS="$ROOT/.secrets"

rand() { LC_ALL=C tr -dc "$1" </dev/urandom | head -c "$2"; }

generate_secrets() {
  if [[ -f "$SECRETS/env" ]]; then
    log "reusing generated secrets in .secrets/"
    return
  fi
  log "generating credentials and TLS certificate in .secrets/"
  mkdir -p "$SECRETS"
  chmod 700 "$SECRETS"
  {
    echo "PG_SUPERUSER_PASSWORD=$(rand 'A-Za-z0-9' 32)"
    echo "PG_REPLICATION_PASSWORD=$(rand 'A-Za-z0-9' 32)"
    echo "PG_APP_PASSWORD=$(rand 'A-Za-z0-9' 32)"
    echo "EXTENDDB_ADMIN_PASSWORD=$(rand 'A-Za-z0-9' 32)"
    for cell in "${CELLS[@]}"; do
      var=$(echo "$cell" | tr 'a-z-' 'A-Z_')
      # ExtendDB accepts imported keys that are 20 chars and start with AKIA.
      echo "${var}_ACCESS_KEY_ID=AKIAEXTENDDB$(rand 'A-Z0-9' 8)"
      echo "${var}_SECRET_ACCESS_KEY=extenddb$(rand 'A-Za-z0-9' 32)"
    done
  } >"$SECRETS/env"
  chmod 600 "$SECRETS/env"

  # One self-signed server certificate valid for ExtendDB in every cell.
  local san="DNS:extenddb,DNS:localhost,IP:127.0.0.1"
  for cell in "${CELLS[@]}"; do
    san+=",DNS:extenddb.${cell},DNS:extenddb.${cell}.svc,DNS:extenddb.${cell}.svc.cluster.local"
  done
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
    -days 825 -subj "/CN=extenddb" \
    -addext "subjectAltName=${san}" \
    -addext "basicConstraints=critical,CA:FALSE" \
    -addext "keyUsage=critical,digitalSignature" \
    -addext "extendedKeyUsage=serverAuth" \
    -keyout "$SECRETS/key.pem" -out "$SECRETS/cert.pem" 2>/dev/null
  chmod 600 "$SECRETS/key.pem"
}

apply_secret() { # namespace name args...
  local ns=$1 name=$2
  shift 2
  kubectl -n "$ns" create secret generic "$name" "$@" --dry-run=client -o yaml \
    | kubectl apply -f - >/dev/null
}

create_secrets() {
  # shellcheck disable=SC1091
  source "$SECRETS/env"
  for cell in "${CELLS[@]}"; do
    kubectl apply -f "$ROOT/k8s/cells/$cell/namespace.yaml" >/dev/null
    apply_secret "$cell" postgres-credentials \
      --from-literal=superuser-password="$PG_SUPERUSER_PASSWORD" \
      --from-literal=replication-password="$PG_REPLICATION_PASSWORD" \
      --from-literal=app-password="$PG_APP_PASSWORD"
    apply_secret "$cell" extenddb-tls \
      --from-file=cert.pem="$SECRETS/cert.pem" --from-file=key.pem="$SECRETS/key.pem"
    var=$(echo "$cell" | tr 'a-z-' 'A-Z_')
    key_id_var="${var}_ACCESS_KEY_ID"
    secret_var="${var}_SECRET_ACCESS_KEY"
    apply_secret "$cell" clients-api-aws \
      --from-literal=AWS_ACCESS_KEY_ID="${!key_id_var}" \
      --from-literal=AWS_SECRET_ACCESS_KEY="${!secret_var}"
  done
  # Bootstrap-only secrets live in cell-1, where the one-time Jobs run.
  apply_secret cell-1 extenddb-admin --from-literal=password="$EXTENDDB_ADMIN_PASSWORD"
  apply_secret cell-1 clients-api-keys --from-env-file=<(grep '^CELL_' "$SECRETS/env")
  log "secrets applied to ${CELLS[*]}"
}

apply_component() { # component
  log "applying $1 to ${CELLS[*]}"
  for cell in "${CELLS[@]}"; do
    kubectl apply -k "$ROOT/k8s/cells/$cell" -l "app.kubernetes.io/component=$1" >/dev/null
  done
}

rollout() { # kind/name
  for cell in "${CELLS[@]}"; do
    kubectl -n "$cell" rollout status "$1" --timeout=300s
  done
}

run_job() { # job name
  log "running job $1 in cell-1"
  kubectl -n cell-1 delete job "$1" --ignore-not-found >/dev/null
  kubectl apply -k "$ROOT/k8s/bootstrap" -l "app.kubernetes.io/name=$1" >/dev/null
  if ! kubectl -n cell-1 wait --for=condition=complete "job/$1" --timeout=600s; then
    kubectl -n cell-1 logs "job/$1" --tail=50 || true
    die "job $1 did not complete"
  fi
  kubectl -n cell-1 logs "job/$1" --tail=20
}

wait_for() { # description command...
  local what=$1
  shift
  for _ in $(seq 1 60); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 5
  done
  die "timed out waiting for $what"
}

consul_has_3_voters() {
  [[ $(kubectl -n cell-1 exec consul-0 -- consul operator raft list-peers 2>/dev/null \
        | grep -cE '[[:space:]](leader|follower)[[:space:]]') -eq 3 ]]
}

patroni_has_leader_and_2_streaming() {
  patronictl list -f json | jq -e '
    ([.[] | select(.Role == "Leader")] | length == 1) and
    ([.[] | select(.State == "streaming")] | length == 2)'
}

kubectl config current-context | grep -q "k3d-${CLUSTER}" \
  || die "kubectl context is not k3d-${CLUSTER}; run scripts/setup-k3d.sh first"

generate_secrets
create_secrets

apply_component consul
rollout statefulset/consul
wait_for "consul raft (3 voters)" consul_has_3_voters
kubectl -n cell-1 exec consul-0 -- consul members

apply_component postgres
rollout statefulset/patroni
wait_for "patroni leader + 2 streaming replicas" patroni_has_leader_and_2_streaming
patronictl list

apply_component pg-router
rollout deployment/pg-router

run_job extenddb-init

apply_component extenddb
rollout deployment/extenddb

run_job extenddb-provision

apply_component clients-api
rollout deployment/clients-api

log "done. Try: scripts/verify.sh"
