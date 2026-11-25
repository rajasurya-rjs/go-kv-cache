# Go KV Cache

High-performance in-memory key-value store built with Go and `fasthttp`.

## Features

- **Sharded cache**: 256 independent shards, each protected by a `sync.RWMutex`,
  so concurrent requests rarely contend for the same lock.
- **Lock-light reads**: `GET` only takes a shard's *read* lock. Eviction uses
  the CLOCK ("second chance") algorithm, which approximates LRU without
  reordering data on reads.
- **Fast HTTP API**: `fasthttp` with pre-built responses, a hand-written JSON
  encoder and pooled buffers. A full `GET` handler call takes about 100 ns with
  1 small allocation.
- **Bounded memory**: fixed capacity (`CACHE_CAPACITY`, default 1,000,000
  keys); the coldest keys are evicted when full.
- **Optimized Docker image**: multi-stage build onto `scratch`, non-root user,
  built-in healthcheck, **~8.4 MB** image.
- **CI/CD pipeline**: lint, SAST (gosec, CodeQL), SCA (govulncheck), unit tests
  with a 75% coverage gate (currently ~96%), Trivy image scan, smoke test,
  DockerHub push and optional Kubernetes rollout.

## Quick Start

```bash
# Build and run locally (requires Go; the right toolchain downloads automatically)
make run

# Or with Docker
make docker-up

# Run with maximum performance (host networking, Linux)
make docker-up-host
```

## API Endpoints

### PUT /put

`POST` and `PUT` are both accepted.

```bash
curl -X POST "http://localhost:7171/put" \
  -H "Content-Type: application/json" \
  -d '{"key":"name", "value":"GoKVCache"}'
```

```json
{"status":"OK","message":"Key inserted/updated successfully."}
```

### GET /get

```bash
curl "http://localhost:7171/get?key=name"
```

```json
{"status":"OK","key":"name","value":"GoKVCache"}
```

A missing key is a normal cache outcome, so it returns HTTP 200:

```json
{"status":"ERROR","message":"Key not found."}
```

### GET /health

```json
{"status":"healthy"}
```

### GET /stats

```json
{"status":"OK","cache":{"entries":1,"capacity":1000000,"shards":256,"hits":4,"misses":1,"evictions":0}}
```

### Errors

Every error uses the same JSON shape: `{"status":"ERROR","message":"..."}`.

| Status | When |
|--------|------|
| 400 | Malformed JSON, missing/empty key, non-string or missing value, key or value over 256 bytes |
| 404 | Unknown path |
| 405 | Wrong method on a known path (the `Allow` header lists valid methods) |
| 413 | Request body larger than `MAX_BODY_BYTES` |

## Makefile Commands

| Command | Description |
|---------|-------------|
| `make build` | Build the application into `bin/kvcache` |
| `make run` | Build and run locally |
| `make test` | Run unit tests |
| `make test-race` | Run tests with the race detector |
| `make test-coverage` | Run tests with coverage (fails below 75%) |
| `make bench` | Run micro-benchmarks |
| `make loadtest` | Load-test a running server |
| `make lint` | Run golangci-lint |
| `make sec` | Run gosec (SAST) |
| `make vuln` | Run govulncheck (SCA) |
| `make check` | Run every CI quality gate locally |
| `make docker-build` | Build Docker image |
| `make docker-up` | Start container |
| `make docker-up-host` | Start container on the host network |
| `make docker-down` | Stop container |
| `make smoke-test` | Run smoke test against the image |
| `make clean` | Remove artifacts |

Install the lint/security tools once with:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
go install github.com/securego/gosec/v2/cmd/gosec@v2.30.0
go install golang.org/x/vuln/cmd/govulncheck@v1.10.0
```

## Configuration

All settings come from environment variables, and each has a default.

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `7171` | TCP port |
| `HOST` | `0.0.0.0` | Bind address |
| `CACHE_CAPACITY` | `1000000` | Maximum number of keys |
| `CACHE_SHARDS` | `256` | Number of shards (rounded up to a power of two) |
| `MAX_KEY_BYTES` | `256` | Maximum key size |
| `MAX_VALUE_BYTES` | `256` | Maximum value size |
| `MAX_BODY_BYTES` | `4096` | Maximum request body size |
| `READ_TIMEOUT` | `5s` | Max time to read a request |
| `WRITE_TIMEOUT` | `5s` | Max time to write a response |
| `IDLE_TIMEOUT` | `60s` | Keep-alive idle timeout |
| `SHUTDOWN_TIMEOUT` | `10s` | Grace period for in-flight requests on shutdown |

Invalid values (e.g. `PORT=abc`) stop the server at startup with a clear error.

**Memory sizing:** measured at ~125 bytes per entry for short keys/values and
~580 bytes at the 256-byte maximum, so 1M entries need roughly 125-600 MB.

**Capacity note:** capacity is split evenly across shards and each shard
evicts on its own. Keys never land perfectly evenly, so a full cache holds
about 98-99% of `CACHE_CAPACITY` (it never exceeds it).

## Performance

Measured on an Apple M1 (8 cores) with the load generator on the same
machine. Absolute numbers depend on hardware; compare runs on one machine.

| Scenario (100 connections, 10k keys) | Throughput | p50 | p99 |
|---|---|---|---|
| GET only | ~185,000 req/s | ~0.44 ms | ~2.2 ms |
| PUT only | ~182,000 req/s | ~0.44 ms | ~2.3 ms |
| 90% GET / 10% PUT | ~184,000 req/s | ~0.44 ms | ~2.2 ms |

Micro-benchmarks (`make bench`): parallel cache `Get` ~27 ns/op with 0
allocations; full `GET` handler ~100 ns/op.

```bash
make run                                          # terminal 1
make loadtest                                     # terminal 2
go run ./cmd/loadgen -c 200 -d 30s -read 80       # custom run
```

For very high connection counts on Linux, `scripts/tune-host.sh` prints
recommended kernel settings (`--apply` to set them).

## How it works

```
request ──► fasthttp server ──► Handler.Serve (switch on path)
                                   │
                    /put: decode JSON, validate, Cache.Set
                    /get: validate key, Cache.Get, encode JSON
                                   │
                                   ▼
               maphash(key) & mask ──► one of 256 shards
                                   │
                    shard: RWMutex + map[key]slot + ring of slots
                    Get = RLock + map lookup + set "referenced" bit
                    Set = Lock  + update in place, append, or evict (CLOCK)
```

- **Why shards?** One global lock would make every core wait on every other.
  With 256 shards, two requests only contend if their keys hash to the same
  shard.
- **Why CLOCK instead of strict LRU?** Strict LRU moves an entry to the front
  of a list on every read, which needs the exclusive lock. CLOCK only flips an
  atomic bit, so reads stay on the shared read lock. Eviction sweeps a "hand"
  around the ring and evicts the first entry that hasn't been used since the
  last sweep.
- **Why `maphash`?** It's fast, allocation-free and randomly seeded per
  process, so nobody can precompute keys that all land on one shard.

## CI/CD Pipeline

### Pipeline Architecture

```
┌─────────────────────────────────────────┐
│           CI - Build & Test             │
├─────────────────────────────────────────┤
│ • Checkout code                         │
│ • Setup Go (version from go.mod)        │
│ • Format check (gofmt)                  │
│ • Lint (golangci-lint)                  │
│ • SAST (gosec)                          │
│ • SCA (govulncheck)                     │
│ • Unit tests + race detector (75%+)     │
│ • Build binary                          │
└──────────────────┬──────────────────────┘
                   │ depends on CI
                   ▼
┌─────────────────────────────────────────┐
│          CD - Docker & Deploy           │
├─────────────────────────────────────────┤
│ • Build Docker image                    │
│ • Trivy vulnerability scan              │
│ • Container smoke test                  │
│ • Push to DockerHub (main only)         │
│ • Deploy to Kubernetes (optional)       │
└─────────────────────────────────────────┘
```

### Pipeline Stages

| Stage | Tool | Purpose |
|-------|------|---------|
| Formatting | gofmt | Consistent code style |
| Linting | golangci-lint | Prevents technical debt |
| SAST | gosec, CodeQL | Detects OWASP Top 10 issues |
| SCA | govulncheck | Identifies supply-chain risks |
| Unit Tests | go test -race | Prevents regressions and data races (75%+ coverage) |
| Build | go build | Validates compilation |
| Docker Build | docker | Creates container image |
| Image Scan | Trivy | Prevents vulnerable images |
| Smoke Test | curl | Ensures container is runnable |
| Registry Push | DockerHub | Enables deployment |
| Deploy | kubectl | Rolls out the new image (opt-in) |

### Triggers

- **Push to main**: Full CI/CD with DockerHub push
- **Pull Request**: CI/CD without DockerHub push
- **Manual**: `workflow_dispatch`
- **Weekly**: CodeQL re-scan; Dependabot update PRs

## Security

### Security Scanning Tools

1. **gosec**: Go static security analyzer (SAST)
2. **govulncheck**: Go vulnerability checker (SCA)
3. **CodeQL**: GitHub's semantic code analysis
4. **Trivy**: container vulnerability scanner

All GitHub Actions are pinned to commit SHAs.

### Container hardening

- `scratch` base: no shell, no package manager, nothing extra to exploit
- runs as non-root user `65532`
- works with a read-only root filesystem and all Linux capabilities dropped
  (the Kubernetes manifest enforces both)

### Branch Protection

- Both `CI - Build & Test` and `CD - Docker & Deploy` must pass
- Strict mode enabled (branch must be up to date)

After pushing to GitHub, enable it with:

```bash
scripts/setup-branch-protection.sh
```

## Secrets Configuration

Configure these in GitHub: **Settings → Secrets and variables → Actions**.

| Name | Type | Purpose |
|------|------|---------|
| `DOCKERHUB_USERNAME` | Secret | Docker registry username |
| `DOCKERHUB_TOKEN` | Secret | Docker registry access token |
| `KUBE_CONFIG` | Secret (optional) | Base64-encoded kubeconfig for deployment |
| `DEPLOY_ENABLED` | Variable (optional) | Set to `true` to enable the Kubernetes deploy step |

**DockerHub token:**
1. Go to DockerHub → Account Settings → Security
2. Create an Access Token with Read/Write permissions
3. In the GitHub repo: Settings → Secrets → Actions → New repository secret

## Docker Image

```bash
# Pull from DockerHub (after the pipeline has pushed it)
docker pull <your-dockerhub-username>/kv-cache:latest

# Run with optimizations
docker run -d \
  --name kv-cache \
  -p 7171:7171 \
  --ulimit nofile=1048576:1048576 \
  <your-dockerhub-username>/kv-cache:latest
```

### Performance Optimizations

The `docker-up` targets include:
- `--ulimit nofile=1048576:1048576`: high file-descriptor limit for many connections
- `--restart=unless-stopped`: auto-restart on failure
- `docker-up-host` adds `--network=host` to skip Docker's network translation (Linux)

## Kubernetes

```bash
# Replace IMAGE_PLACEHOLDER in k8s/deployment.yaml, then:
kubectl apply -f k8s/
kubectl port-forward svc/kv-cache 7171:7171
```

One replica (the data lives in memory), `Recreate` strategy, liveness and
readiness probes on `/health`, CPU/memory limits, a `GOMEMLIMIT` soft heap
limit, read-only root filesystem, and all capabilities dropped.

## Project Structure

```
.
├── .github/
│   ├── workflows/
│   │   ├── ci.yml              # Main CI/CD pipeline
│   │   └── codeql.yml          # CodeQL security analysis
│   └── dependabot.yml          # Automated dependency updates
├── cmd/
│   ├── kvcache/main.go         # Server entrypoint, graceful shutdown, -healthcheck
│   └── loadgen/main.go         # Load-testing CLI
├── internal/
│   ├── api/                    # HTTP handler, JSON encoder, fasthttp server setup
│   ├── cache/                  # Sharded RWMutex cache with CLOCK eviction
│   ├── config/                 # Environment variable configuration
│   └── loadtest/               # Load generator engine
├── k8s/                        # Deployment + Service
├── scripts/
│   ├── smoke-test.sh           # End-to-end check with curl
│   ├── tune-host.sh            # Optional Linux kernel tuning
│   └── setup-branch-protection.sh
├── Dockerfile                  # Multi-stage build → scratch
├── Makefile                    # Build automation
├── go.mod                      # Go module definition
├── .golangci.yml               # Linter configuration
└── README.md                   # This file
```

## License

MIT
