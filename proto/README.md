# Protobuf Support for EcoFlow Devices

This directory contains the protobuf schema for EcoFlow MQTT messages used by newer devices (Delta3, River3, STREAM Ultra).

## Files

- `ecoflow.proto` - Protocol Buffers schema defining the message structure
- `generate.sh` - Script to generate Go code from the protobuf schema

## Protobuf Message Structure

EcoFlow devices send MQTT messages in the following protobuf format:

```
HeaderMessage
├── Header[] (array of headers)
    ├── pdata: bytes        # Device data (JSON, may be XOR encrypted)
    ├── enc_type: int32     # 0 = no encryption, 1 = XOR encrypted
    ├── seq: int32          # Sequence number (used as XOR key)
    ├── cmd_func: int32     # Command function ID
    ├── cmd_id: int32       # Command ID
    └── ... other metadata fields
```

## Encryption

When `enc_type` is 1, the `pdata` field is XOR-encrypted using the lowest byte of the `seq` field:

```
decrypted_byte = encrypted_byte XOR (seq & 0xFF)
```

## Code Generation

To regenerate the Go code from the protobuf schema:

```bash
cd proto
./generate.sh
```

### Prerequisites

- `protoc` compiler: `brew install protobuf` (macOS) or `apt-get install protobuf-compiler` (Linux)
- `protoc-gen-go` plugin: `go install google.golang.org/protobuf/cmd/protoc-gen-go@latest`

## Implementation Details

The protobuf decoder is located in `internal/protobuf/decoder.go` and provides:

1. **Format Detection** - Automatically detects JSON vs protobuf messages
2. **Decryption** - Handles XOR-encrypted pdata
3. **JSON Parsing** - Extracts params from pdata (most devices send JSON inside protobuf)
4. **Header Merging** - Combines params from multiple headers in a single message

## Testing

Unit tests: `internal/protobuf/decoder_test.go`
Integration tests: `mqtt_exporter_test.go` (TestMessageHandler_Protobuf*)

Run tests:
```bash
go test ./internal/protobuf/... -v
go test -v -run TestMessageHandler_Protobuf
```

## References

- Community reverse-engineering: https://github.com/foxthefox/ioBroker.ecoflow-mqtt
- Issue #22: https://github.com/tess1o/go-ecoflow-exporter/issues/22
