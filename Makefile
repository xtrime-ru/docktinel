GO ?= go
VERSION ?= dev
IMAGE ?= docktinel:local
DOCKTINEL_IMAGE ?= ghcr.io/xtrime-ru/docktinel:latest

.PHONY: build test image demo integration integration-test
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w -X main.version=$(VERSION)' -o bin/docktinel ./cmd/docktinel

test:
	$(GO) test -race ./...
	$(GO) vet ./...

image:
	docker buildx build --load --build-arg VERSION=$(VERSION) -t $(IMAGE) .

demo:
	docker build --builder "$$(docker context show)" --build-arg DOCKTINEL_IMAGE=$(DOCKTINEL_IMAGE) -t docktinel-demo:local examples/basic

integration: image
	$(MAKE) demo DOCKTINEL_IMAGE=$(IMAGE)
	$(MAKE) integration-test

integration-test:
	DOCKER_HOST="$${DOCKER_HOST:-$$(docker context inspect --format '{{.Endpoints.docker.Host}}')}" $(GO) -C integration test -race -count=1 -timeout=3m -v ./...
	$(GO) -C integration vet ./...
