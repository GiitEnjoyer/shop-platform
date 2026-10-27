COMPOSE := docker compose -f deploy/compose/docker-compose.yml

.PHONY: help test lint up down logs loadtest canary-demo e2e template

help: ## Show targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*## "}{printf "  \033[36m%-12s\033[0m %s\n",$$1,$$2}'

test: ## Go tests with race detector + rule tests + script tests
	go test -race -count=1 ./...
	cd observability/rules/tests && promtool test rules *.yaml
	python3 -m pytest scripts/ -q

lint: ## golangci-lint, shellcheck, helm lint
	golangci-lint run ./...
	shellcheck scripts/*.sh
	helm lint deploy/helm/microservice -f deploy/helm/values/orders/common.yaml -f deploy/helm/values/orders/prod.yaml --set image.tag=lint

up: ## Start local stack (services, Postgres, RabbitMQ, Prometheus, Grafana)
	$(COMPOSE) up -d --build
	@echo "catalog  http://localhost:8080/api/v1/products"
	@echo "orders   http://localhost:8081/api/v1/orders"
	@echo "grafana  http://localhost:3000  (dashboard: Shop / Service SLO and canary)"

down: ## Stop local stack and remove volumes
	$(COMPOSE) down -v

logs: ## Tail service logs
	$(COMPOSE) logs -f catalog orders

loadtest: ## k6 against the local stack
	docker run --rm -i --network host -v $(PWD)/loadtest:/scripts grafana/k6:0.54.0 run \
	  -e CATALOG_URL=http://localhost:8080 -e ORDERS_URL=http://localhost:8081 -e PEAK_RPS=200 /scripts/k6-shop.js

canary-demo: ## Restart orders with 20% injected errors to watch SLO alerts fire
	FAULT_RATE=0.2 $(COMPOSE) up -d orders
	@echo "Now run 'make loadtest' and watch Grafana / Prometheus alerts"

e2e: ## Blue-Green e2e on kind
	./scripts/e2e-kind.sh

template: ## Render chart in canary mode
	helm template orders deploy/helm/microservice -f deploy/helm/values/orders/common.yaml \
	  -f deploy/helm/values/orders/prod.yaml --set image.tag=v1 --set canary.enabled=true --set canary.tag=v2
