DB_URL=postgresql://postgres:postgres@timescaledb.docker-compose.orb.local:5432/postgres?sslmode=disable
DOCKER_IMAGE_NAME = tess1o/go-ecoflow-exporter

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

build-push: build push

start-grafana:
	docker-compose -f docker-compose/grafana-compose.yml up -d

start-prometheus:
	docker-compose -f docker-compose/prometheus-compose.yml up -d

start-timescale:
	docker-compose -f docker-compose/timescale-compose.yml up -d

start-redis:
	docker-compose -f docker-compose/redis-compose.yml up -d

start-exporter-local:
	docker-compose -f docker-compose/exporter-local-compose.yml up --build --force-recreate --no-deps -d

start-exporter-remote:
	docker-compose -f docker-compose/exporter-remote-compose.yml up

stop-exporter:
	docker stop go_ecoflow_exporter
	docker rm go_ecoflow_exporter
	docker ps

exporter-logs:
	docker logs -f go_ecoflow_exporter

.PHONY: build push build-push migrateup migrateup1 migratedown migratedown1 new_migration start-grafana start-prometheus start-timescale start-redis start-exporter-local start-exporter-remote stop-all