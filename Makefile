NAME := horizon
GIT_COMMIT ?= $(shell git rev-parse --short HEAD)

## default to the old TeamCity-specific Docker tag prefix
DOCKER_TAG_PREFIX ?= TC

## BUILD_COUNTER is set by TeamCity; if not provided, use Circle's equivalent
BUILD_COUNTER ?= $(CIRCLE_BUILD_NUM)

EFFECTIVE_LD_FLAGS ?= "-X main.GitCommit=$(GIT_COMMIT) $(LD_FLAGS)"
JUNIT_XML ?= work/junit.xml

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

## utility for transforming go test output to junit xml for test reporting in ci
GO_JUNIT_REPORT := work/go-junit-report
$(GO_JUNIT_REPORT): $(GO_MOD_SOURCES)
	go build -o $@ github.com/jstemmer/go-junit-report

## run tests only when sources have changed
work/.tests-ran: $(SOURCES) $(TEST_SOURCES) $(GO_JUNIT_REPORT)
ifdef CI ## set by CircleCI
	bash -c 'go test -v ./... | tee >( $(GO_JUNIT_REPORT) > $(JUNIT_XML) )'
else
	go test -v ./...
endif
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
