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
    ├── pdata: bytes        # Device data (JSON or nested protobuf, may be XOR encrypted)
    ├── enc_type: int32     # 0 = no encryption, 1 = XOR encrypted
    ├── seq: int32          # Sequence number (used as XOR key)
    ├── cmd_func: int32     # Command function ID
    ├── cmd_id: int32       # Command ID
    └── ... other metadata fields
```

## pdata Messages

Older devices put JSON into `pdata`. Newer devices (Delta 3, Delta Pro 3, STREAM AC Pro / Ultra / Ultra X)
send a nested protobuf message; its type is selected by `cmd_func`/`cmd_id`:

| cmd_func / cmd_id | Message | Param keys |
|---|---|---|
| 254 / 21, 254 / 22 | `PropertyUpload` (display + runtime properties) | `powInSumW`, `bmsBattSoc`, `powGetSysGrid`, ... |
| 32 / 2 | `CMSHeartBeatReport` | `cmsBattSoc`, `cmsBattVolMv`, ... |
| 32 / 50 and other BMS pairs | `BMSHeartBeatReport` | `bms.<pack>.soc`, `bms.<pack>.cycles`, ... |

Param keys are the protobuf JSON (camelCase) field names, so `powInSumW` becomes the
`ecoflow_pow_in_sum_w` Prometheus metric. Commands not listed above are logged once as unsupported.
With `PROTOBUF_DEBUG=true` the decoder also logs field numbers that are not in the schema yet.

## Encryption

When `enc_type` is 1 (and `src` is not 32), the `pdata` field is XOR-encrypted using the lowest byte of the `seq` field:

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
3. **pdata Parsing** - Extracts params from JSON pdata or from nested protobuf messages (`messages.go`)
4. **Header Merging** - Combines params from multiple headers in a single message

## Testing

Unit tests: `internal/protobuf/decoder_test.go`, `internal/protobuf/messages_test.go` (real device captures)
Integration tests: `mqtt_exporter_test.go` (TestMessageHandler_Protobuf*)

Run tests:
```bash
go test ./internal/protobuf/... -v
go test -v -run TestMessageHandler_Protobuf
```

## References

- Community reverse-engineering: https://github.com/foxthefox/ioBroker.ecoflow-mqtt
- Message definitions derived from https://github.com/tolwi/hassio-ecoflow-cloud (Apache License 2.0)
- Issue #22: https://github.com/tess1o/go-ecoflow-exporter/issues/22
