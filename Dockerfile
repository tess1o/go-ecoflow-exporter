# Build app
FROM golang:1.27.2-alpine3.24 AS build
ARG VERSION=dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o ecoflow-exporter .

# Final image
FROM alpine:3.24
WORKDIR /app
RUN apk --no-cache add ca-certificates tzdata \
    && addgroup -S exporter && adduser -S -G exporter exporter
COPY --from=build /app/ecoflow-exporter .
USER exporter
EXPOSE 2112
HEALTHCHECK --interval=30s --timeout=5s --start-period=90s --retries=3 CMD ["/app/ecoflow-exporter", "healthcheck"]
ENTRYPOINT ["/app/ecoflow-exporter"]
