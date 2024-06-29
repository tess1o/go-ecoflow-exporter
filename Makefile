.PHONY: build push start start-local sqlc migrateup migrateup1 migratedown migratedown1 new_migration

DB_URL=postgresql://postgres:postgres@timescaledb.docker-compose.orb.local:5432/postgres?sslmode=disable
DOCKER_IMAGE_NAME = tess1o/go-ecoflow-exporter
DOCKER_COMPOSE_FILE = docker-compose/compose.yaml
DOCKER_COMPOSE_LOCAL_BUILD_FILE = docker-compose/compose-local-build.yaml
DOCKER_COMPOSE_TIMESCALEDB_LOCAL_BUILD_FILE = docker-compose/compose-timescaledb.yaml

sqlc:
	sqlc generate

migrateup:
	migrate -path db/migration -database "$(DB_URL)" -verbose up

migrateup1:
	migrate -path db/migration -database "$(DB_URL)" -verbose up 1

migratedown:
	migrate -path db/migration -database "$(DB_URL)" -verbose down

migratedown1:
	migrate -path db/migration -database "$(DB_URL)" -verbose down 1

new_migration:
	migrate create -ext sql -dir db/migration -seq $(name)
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

start-local-timescaledb:
    #build exporter from local source and then start it with grafana and prometheus
	docker-compose -f $(DOCKER_COMPOSE_TIMESCALEDB_LOCAL_BUILD_FILE) build --no-cache
	docker-compose -f $(DOCKER_COMPOSE_TIMESCALEDB_LOCAL_BUILD_FILE) up -d
	docker-compose -f $(DOCKER_COMPOSE_TIMESCALEDB_LOCAL_BUILD_FILE) ps
build-push: build push