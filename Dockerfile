# Build app
FROM golang:1.22-alpine AS build
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o ecoflow-exporter .

# Final image
FROM alpine:edge
WORKDIR /app
COPY --from=build /app/ecoflow-exporter .
COPY migrations/timescale ./migrations/timescale
RUN apk --no-cache add ca-certificates tzdata
EXPOSE 2112
ENTRYPOINT ["/app/ecoflow-exporter"]