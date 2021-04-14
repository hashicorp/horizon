NAME := horizon
GIT_COMMIT ?= $(shell git rev-parse --short HEAD)

## default to the old TeamCity-specific Docker tag prefix
DOCKER_TAG_PREFIX ?= TC

## BUILD_COUNTER is set by TeamCity; if not provided, use Circle's equivalent
BUILD_COUNTER ?= $(CIRCLE_BUILD_NUM)

EFFECTIVE_LD_FLAGS ?= "-X main.GitCommit=$(GIT_COMMIT) $(LD_FLAGS)"

## fully-qualified path to this Makefile
MKFILE_PATH := $(realpath $(lastword $(MAKEFILE_LIST)))

## fully-qualified path to the current directory
CURRENT_DIR := $(patsubst %/,%,$(dir $(MKFILE_PATH)))

## all non-test source files
GO_MOD_SOURCES := go.mod go.sum
SOURCES := $(GO_MOD_SOURCES) $(shell go list -mod=readonly -f '{{range .GoFiles}}{{ $$.Dir }}/{{.}} {{end}}' ./... | sed -e 's@$(CURRENT_DIR)/@@g' )
TEST_SOURCES := $(GO_MOD_SOURCES) $(shell go list -mod=readonly -f '{{range .XTestGoFiles}}{{ $$.Dir }}/{{.}} {{end}}' ./... | sed -e 's@$(CURRENT_DIR)/@@g' )

.PHONY: help
help:
	@echo "Valid targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MKFILE_PATH) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-15s\033[0m %s\n", $$1, $$2}'

## https://stackoverflow.com/a/36045843
require-%:
	$(if ${${*}},,$(error You must pass the $* environment variable))

## run tests only when sources have change
work/.tests-ran: $(SOURCES) $(TEST_SOURCES)
	go test -v ./... || exit 1
	@touch $@

.PHONY: test
test: work/.tests-ran ## Run tests

.PHONY: bin
BIN := bin/hzn
bin: $(BIN) ## Build application binary
$(BIN): $(SOURCES) | test
	go build -o $@ -ldflags $(EFFECTIVE_LD_FLAGS) ./cmd/hzn

linux: ## Build a linux/amd64 version of the binary (mainly used for local development)
	GOOS=linux GOARCH=amd64 go build -o "$(BIN)" -ldflags $(EFFECTIVE_LD_FLAGS) ./cmd/hzn

.PHONY: pkg
pkg: pkg/$(NAME).tar.gz ## Build application 'serviceball'
pkg/$(NAME).tar.gz: bin
	@mkdir -p $(dir $@)
	tar -czf $@ --xform='s,bin/,,' --xform='s,_build/,,' bin/*

.PHONY: clean
clean:
	rm -r $(CURDIR)/bin
	rm -r $(CURDIR)/pkg
	rm -r $(CURDIR)/work

## an error is raised if these are not set
.PHONY: require-docker-vars
require-docker-vars: require-DOCKER_USER require-DOCKER_PASS require-DOCKER_URL \
	require-BUILD_NUMBER require-BUILD_COUNTER

DOCKER_IMAGE := $(DOCKER_URL)/$(DOCKER_ORG)/$(NAME)
.PHONY: pkg-docker
pkg-docker: require-docker-vars ## Creates a docker container and uploads it to the repo
	@echo $(DOCKER_PASS) | docker login --username "$(DOCKER_USER)" --password-stdin $(DOCKER_URL)

	DOCKER_BUILDKIT=1 docker build -t $(DOCKER_IMAGE):$(BUILD_NUMBER) .

	docker tag $(DOCKER_IMAGE):$(BUILD_NUMBER) $(DOCKER_IMAGE):$(DOCKER_TAG_PREFIX)$(BUILD_COUNTER)

	docker push $(DOCKER_IMAGE):$(DOCKER_TAG_PREFIX)$(BUILD_COUNTER)
	docker push $(DOCKER_IMAGE):$(BUILD_NUMBER)


dev-setup:
	docker-compose exec -- postgres psql --username postgres -c "CREATE DATABASE horizon_dev;" || true
	DATABASE_URL=postgres://postgres:postgres@localhost/horizon_dev?sslmode=disable MIGRATIONS_PATH=./pkg/control/migrations  go run ./cmd/hzn/main.go migrate || true
	docker-compose up -d

dev-start:
	DATABASE_URL=postgres://postgres:postgres@localhost/horizon_dev?sslmode=disable go run ./cmd/hzn/ dev



run-agent:
	go run ./cmd/hznagent agent \
	--control dev://localhost:24403 \
	--token "$$(< dev-agent-token.txt)" \
	--http 8085 \
	--labels service=test,env=test \
	--verbose


run-label-link:
	go run ./cmd/hznctl/main.go \
	create-label-link \
	--control-addr localhost:24401 \
	--token "$$(< dev-mgmt-token.txt)" \
	--label :hostname=app-1.waypoint.local:24404 \
	--account "$$(< dev-agent-id.txt)" \
	--target "service=test,env=test" \
	--insecure

.PHONY: dev-setup
