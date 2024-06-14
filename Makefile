.PHONY: build push start start-local

DOCKER_IMAGE_NAME = tess1o/go-ecoflow-exporter
DOCKER_COMPOSE_FILE = docker-compose/compose.yaml
DOCKER_COMPOSE_LOCAL_BUILD_FILE = docker-compose/compose-local-build.yaml

build:
	docker build --platform linux/amd64 -t $(DOCKER_IMAGE_NAME):latest .

push:
    # you have to login to docker registry first
	docker push $(DOCKER_IMAGE_NAME)

start:
    # start exporter, grafana and prometheus from external images
	docker-compose -f $(DOCKER_COMPOSE_FILE) up
	docker-compose -f $(DOCKER_COMPOSE_FILE) ps

start-local:
    #build exporter from local source and then start it with grafana and prometheus
	docker-compose -f $(DOCKER_COMPOSE_LOCAL_BUILD_FILE) build --no-cache
	docker-compose -f $(DOCKER_COMPOSE_LOCAL_BUILD_FILE) up -d
	docker-compose -f $(DOCKER_COMPOSE_LOCAL_BUILD_FILE) ps
build-push: build push