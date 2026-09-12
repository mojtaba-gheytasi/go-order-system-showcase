.DEFAULT_GOAL := help

PROJECT_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
ORDER_DIR := $(PROJECT_DIR)/order-service
INVENTORY_DIR := $(PROJECT_DIR)/inventory-service
API_DIR := $(INVENTORY_DIR)/api

ORDER_ENV := $(ORDER_DIR)/.env
ORDER_ENV_FILE := $(if $(wildcard $(ORDER_ENV)),$(ORDER_ENV),$(ORDER_DIR)/.env.example)
INVENTORY_ENV := $(INVENTORY_DIR)/.env
INVENTORY_ENV_FILE := $(if $(wildcard $(INVENTORY_ENV)),$(INVENTORY_ENV),$(INVENTORY_DIR)/.env.example)

COMPOSE := docker compose --project-directory $(PROJECT_DIR) -f $(PROJECT_DIR)/docker-compose.yml

# Every Go module in the repository, discovered rather than listed. Module-wide
# targets loop over this, so a service added later is covered by fmt, vet, and
# test as soon as it has a go.mod — without editing this file or the CI workflow.
GO_MODULES := $(sort $(patsubst %/,%,$(dir $(shell find $(PROJECT_DIR) -name go.mod \
	-not -path '*/.git/*' -not -path '*/vendor/*' -not -path '*/testdata/*'))))

-include $(ORDER_ENV)
-include $(INVENTORY_ENV)

# Values loaded from the service-local environment files must also be visible to
# Compose while it interpolates the database configuration.
export ORDER_ENV_FILE ORDER_DB_NAME ORDER_DB_USER ORDER_DB_PASSWORD ORDER_DB_PORT
export INVENTORY_ENV_FILE INVENTORY_DB_NAME INVENTORY_DB_USER INVENTORY_DB_PASSWORD \
	INVENTORY_DB_PORT INVENTORY_GRPC_PORT

.PHONY: help dev up down stop ps \
	order-logs order-shell order-db-logs order-db-shell order-db-ready \
	order-migrate-up order-migrate-down order-migrate-create sqlvet \
	inventory-logs inventory-shell inventory-db-logs inventory-db-shell inventory-db-ready \
	inventory-migrate-up inventory-migrate-down inventory-migrate-create inventory-seed \
	proto proto-lint proto-breaking \
	compose-config modules deps fmt test test-integration vet check

help: ## Show available commands
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target>\n\n"} /^[a-zA-Z0-9_-]+:.*## / {printf "  %-26s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

dev: ## Run the whole system in the foreground
	$(COMPOSE) up --build order

up: ## Run the whole system in the background
	$(COMPOSE) up -d --build order

down: ## Stop containers while preserving database data
	$(COMPOSE) down

stop: ## Stop all running project containers
	$(COMPOSE) stop

ps: ## Show project containers
	$(COMPOSE) ps -a

# --- order-service ----------------------------------------------------------

order-logs: ## Follow order-service logs
	$(COMPOSE) logs -f order

order-shell: ## Open a shell in the order-service container
	$(COMPOSE) exec order sh

order-db-logs: ## Follow the order database logs
	$(COMPOSE) logs -f order-postgres

order-db-shell: ## Open psql in the order database
	$(COMPOSE) exec order-postgres psql -U $(ORDER_DB_USER) -d $(ORDER_DB_NAME)

order-db-ready: ## Check whether the order database accepts connections
	$(COMPOSE) exec order-postgres pg_isready -U $(ORDER_DB_USER) -d $(ORDER_DB_NAME)

order-migrate-up: ## Apply every pending order database migration
	migrate -path $(ORDER_DIR)/migrations -database "$(ORDER_DATABASE_URL)" up

order-migrate-down: ## Roll back the latest order database migration
	migrate -path $(ORDER_DIR)/migrations -database "$(ORDER_DATABASE_URL)" down 1

order-migrate-create: ## Create order migration files; usage: make order-migrate-create name=add_column
	@test -n "$(name)" || (echo "name is required; usage: make order-migrate-create name=add_column" && exit 1)
	migrate create -ext sql -dir $(ORDER_DIR)/migrations -seq $(name)

sqlvet: ## Validate order-service SQL against its schema
	cd $(ORDER_DIR) && sqlvet .

# --- inventory-service ------------------------------------------------------

inventory-logs: ## Follow inventory-service logs
	$(COMPOSE) logs -f inventory

inventory-shell: ## Open a shell in the inventory-service container
	$(COMPOSE) exec inventory sh

inventory-db-logs: ## Follow the inventory database logs
	$(COMPOSE) logs -f inventory-postgres

inventory-db-shell: ## Open psql in the inventory database
	$(COMPOSE) exec inventory-postgres psql -U $(INVENTORY_DB_USER) -d $(INVENTORY_DB_NAME)

inventory-db-ready: ## Check whether the inventory database accepts connections
	$(COMPOSE) exec inventory-postgres pg_isready -U $(INVENTORY_DB_USER) -d $(INVENTORY_DB_NAME)

inventory-migrate-up: ## Apply every pending inventory database migration
	migrate -path $(INVENTORY_DIR)/migrations -database "$(INVENTORY_DATABASE_URL)" up

inventory-migrate-down: ## Roll back the latest inventory database migration
	migrate -path $(INVENTORY_DIR)/migrations -database "$(INVENTORY_DATABASE_URL)" down 1

inventory-migrate-create: ## Create inventory migration files; usage: make inventory-migrate-create name=add_column
	@test -n "$(name)" || (echo "name is required; usage: make inventory-migrate-create name=add_column" && exit 1)
	migrate create -ext sql -dir $(INVENTORY_DIR)/migrations -seq $(name)

inventory-seed: ## Reapply the development stock seed
	$(COMPOSE) up --force-recreate inventory-seed

# --- contracts --------------------------------------------------------------

proto: ## Generate Go code from the protobuf contracts
	cd $(PROJECT_DIR) && buf generate

proto-lint: ## Lint the protobuf contracts
	cd $(PROJECT_DIR) && buf lint

proto-breaking: ## Check the contracts for breaking changes against main
	cd $(PROJECT_DIR) && buf breaking --against '.git#branch=main'

# --- all modules ------------------------------------------------------------

compose-config: ## Validate and render the Compose configuration
	$(COMPOSE) config --quiet

modules: ## List every discovered Go module
	@for module in $(GO_MODULES); do echo "$$module"; done

# A for loop reports only its last iteration's status, so every module-wide
# target below exits on the first failure. Without that, a failing module
# earlier in the list would be silently reported as success.

deps: ## Download Go dependencies for every module
	@for module in $(GO_MODULES); do \
		echo "==> $$module" && cd $$module && go mod download || exit 1; \
	done

fmt: ## Format every Go module
	@for module in $(GO_MODULES); do \
		echo "==> $$module" && cd $$module && go fmt ./... || exit 1; \
	done

vet: ## Run go vet across every Go module
	@for module in $(GO_MODULES); do \
		echo "==> $$module" && cd $$module && go vet ./... || exit 1; \
	done

test: ## Run unit tests across every Go module
	@for module in $(GO_MODULES); do \
		echo "==> $$module" && cd $$module && go test -race ./... || exit 1; \
	done

test-integration: ## Run integration-tagged tests across every Go module
	@for module in $(GO_MODULES); do \
		echo "==> $$module" && cd $$module && go test -race -count=1 -tags=integration ./... || exit 1; \
	done

check: compose-config proto-lint vet test ## Run the current local validation suite
