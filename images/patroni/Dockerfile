# PostgreSQL 17 + Patroni (Consul DCS). Patroni is PID 1's child via tini and
# owns the postgres lifecycle: bootstrap, replication, leader election, failover.
FROM postgres:17.11-bookworm

ARG PATRONI_VERSION=4.1.5

RUN apt-get update \
 && apt-get install -y --no-install-recommends python3 python3-venv tini curl \
 && python3 -m venv /opt/patroni \
 && /opt/patroni/bin/pip install --no-cache-dir "patroni[consul,psycopg3]==${PATRONI_VERSION}" \
 && ln -s /opt/patroni/bin/patroni /opt/patroni/bin/patronictl /usr/local/bin/ \
 && apt-get purge -y python3-venv && apt-get autoremove -y \
 && rm -rf /var/lib/apt/lists/*

USER postgres
WORKDIR /var/lib/postgresql
EXPOSE 5432 8008
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["patroni", "/etc/patroni/patroni.yml"]
