# Developer shortcuts. Run `make help` to list every target.

APP_NAME      := kvcache
DOCKER_IMAGE  := kv-cache
DOCKER_TAG    := latest
PORT          := 7171
COVERAGE_MIN  := 75
GOBIN         := $(shell go env GOPATH)/bin

.PHONY: build run test test-race test-coverage bench loadtest fmt lint sec vuln check \
        docker-build docker-up docker-up-host docker-down smoke-test clean help

build: ## Build the server binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/$(APP_NAME) ./cmd/kvcache

run: build ## Build and run the server locally
	./bin/$(APP_NAME)

test: ## Run unit tests
	go test ./...

test-race: ## Run tests with the race detector
	go test -race ./...

test-coverage: ## Run tests with coverage and enforce the minimum
	go test -race -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1
	@total=$$(go tool cover -func=coverage.out | awk '/^total:/ {sub("%","",$$3); print $$3}'); \
	if awk "BEGIN {exit !($$total < $(COVERAGE_MIN))}"; then \
		echo "Coverage $$total% is below $(COVERAGE_MIN)%"; exit 1; \
	else \
		echo "Coverage $$total% meets the $(COVERAGE_MIN)% minimum"; \
	fi

bench: ## Run micro-benchmarks (cache, handler, JSON encoder)
	go test -run=NONE -bench=. -benchmem ./internal/...

loadtest: ## Load-test a running server (start it first with `make run`)
	go run ./cmd/loadgen -url http://127.0.0.1:$(PORT)

fmt: ## Format all Go code
	gofmt -w .

lint: ## Run golangci-lint
	$(GOBIN)/golangci-lint run ./...

sec: ## Run gosec static security analysis (SAST)
	$(GOBIN)/gosec ./...

vuln: ## Check dependencies and stdlib for known vulnerabilities (SCA)
	$(GOBIN)/govulncheck ./...

check: lint sec vuln test-coverage ## Run every CI quality gate locally

docker-build: ## Build the Docker image
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

docker-up: docker-build ## Start the container with a high file-descriptor limit
	docker run -d \
		--name $(APP_NAME) \
		-p $(PORT):7171 \
		--ulimit nofile=1048576:1048576 \
		--restart=unless-stopped \
		$(DOCKER_IMAGE):$(DOCKER_TAG)
	@echo "Running at http://localhost:$(PORT)"

docker-up-host: docker-build ## Start the container on the host network (max performance, Linux only)
	docker run -d \
		--name $(APP_NAME) \
		--network=host \
		--ulimit nofile=1048576:1048576 \
		--restart=unless-stopped \
		$(DOCKER_IMAGE):$(DOCKER_TAG)
	@echo "Running on the host network at http://localhost:7171"

docker-down: ## Stop and remove the container
	-docker stop $(APP_NAME)
	-docker rm $(APP_NAME)

smoke-test: ## Run the image and check put/get/miss end to end
	docker run -d --name $(APP_NAME)-smoke -p $(PORT):7171 $(DOCKER_IMAGE):$(DOCKER_TAG)
	./scripts/smoke-test.sh http://localhost:$(PORT); \
		status=$$?; \
		docker rm -f $(APP_NAME)-smoke > /dev/null; \
		exit $$status

clean: ## Remove build and coverage artifacts
	rm -rf bin coverage.out coverage.html

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
