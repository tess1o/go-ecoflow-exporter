#!/bin/bash
set -e

# Generate Go code from protobuf schema
protoc --go_out=../internal/protobuf --go_opt=paths=source_relative \
    --proto_path=. ecoflow.proto

echo "Protobuf code generated successfully"
