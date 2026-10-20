DOCKER_IMAGE_NAME = tess1o/go-ecoflow-exporter
VERSION ?= dev

build:
	go build -ldflags="-X main.version=$(VERSION)" -o ecoflow-exporter .

test:
	go test -race -count=1 ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...
	golangci-lint run ./...

docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(DOCKER_IMAGE_NAME):$(VERSION) .

run:
	docker compose up -d

logs:
	docker compose logs -f

stop:
	docker compose down

.PHONY: build test lint docker-build run logs stop
