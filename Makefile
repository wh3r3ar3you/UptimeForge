VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/wh3r3ar3you/uptimeforge/internal/version.Version=$(VERSION)

.PHONY: web build test lint demo docker
web:
	cd web && npm ci --no-audit --no-fund && npm run build

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ ./cmd/...

test:
	go test -race ./...
	cd web && npx tsc -b

lint:
	go vet ./...
	test -z "$$(gofmt -l .)"

# Tests and the demo need PostgreSQL: docker compose -f deploy/compose.test.yml up -d
export UF_TEST_DATABASE_URL ?= postgres://uf:uf@127.0.0.1:55432/uf
export UF_DATABASE_URL ?= postgres://uf:uf@127.0.0.1:55432/uf

# A playground with seeded history: http://127.0.0.1:8080, admin / admin12345
demo:
	go run ./tools/demo

docker:
	docker build --target hub -t uptimeforge-hub:$(VERSION) --build-arg VERSION=$(VERSION) .
	docker build --target agent -t uptimeforge-agent:$(VERSION) --build-arg VERSION=$(VERSION) .
