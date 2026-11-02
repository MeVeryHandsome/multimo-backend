VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X multimo-backend/internal/consts.Version=$(VERSION)

.PHONY: build run test vet fmt docker

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/multimo .

run: build
	./bin/multimo

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

docker:
	docker build -f manifest/docker/Dockerfile --build-arg VERSION=$(VERSION) -t multimo-backend:$(VERSION) .
