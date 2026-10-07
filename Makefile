.PHONY: up cluster deploy verify failover failover-kill render app-build down purge

up: cluster deploy            ## create cluster, build images, deploy all cells

cluster:                      ## k3d cluster (1 server + 3 cell agents) + images
	scripts/setup-k3d.sh

deploy:                       ## deploy consul -> patroni -> router -> extenddb -> app
	scripts/deploy.sh

verify:                       ## show cluster state and cross-cell read-after-write
	scripts/verify.sh

failover:                     ## planned Patroni switchover to another cell
	scripts/failover.sh

failover-kill:                ## unplanned failover: delete the primary's pod
	scripts/failover.sh --kill

render:                       ## render all manifests without a cluster
	@for c in cell-1 cell-2 cell-3; do kubectl kustomize k8s/cells/$$c; echo ---; done
	@kubectl kustomize k8s/bootstrap

app-build:
	cd app && go vet ./... && go build -o clients-api .

down:                         ## delete the cluster (keeps .secrets/)
	scripts/teardown.sh

purge:                        ## delete the cluster and generated secrets
	scripts/teardown.sh --purge
