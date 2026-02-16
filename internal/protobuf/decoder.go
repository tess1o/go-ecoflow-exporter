package protobuf

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"google.golang.org/protobuf/proto"
)

// MessageFormat represents the format of an MQTT message
type MessageFormat int

const (
	FormatUnknown MessageFormat = iota
	FormatJSON
	FormatProtobuf
)

// isProtobufDebugEnabled checks if PROTOBUF_DEBUG environment variable is set
func isProtobufDebugEnabled() bool {
	val := os.Getenv("PROTOBUF_DEBUG")
	return val == "true" || val == "1" || val == "TRUE"
}

// ProtobufDecoder handles decoding of protobuf messages
type ProtobufDecoder struct {
	unknownCommands map[string]int  // Track unknown cmd_func/cmd_id combinations
	seenDevices     map[string]bool // Track which devices have sent protobuf (for first-time logging)
}

// NewProtobufDecoder creates a new decoder instance
func NewProtobufDecoder() *ProtobufDecoder {
	return &ProtobufDecoder{
		unknownCommands: make(map[string]int),
		seenDevices:     make(map[string]bool),
	}
}

// LogFirstProtobufMessage logs INFO on first protobuf message from a device (only if PROTOBUF_DEBUG=true)
func (d *ProtobufDecoder) LogFirstProtobufMessage(serialNumber string) {
	if !isProtobufDebugEnabled() {
		return
	}
	if !d.seenDevices[serialNumber] {
		d.seenDevices[serialNumber] = true
		slog.Info("[PROTOBUF_DEBUG] First protobuf message received from device",
			"device", serialNumber,
			"note", "This device uses protobuf format (likely Delta3/River3/STREAM Ultra)")
	}
}

// DetectFormat determines if a message is JSON or protobuf
func DetectFormat(payload []byte) MessageFormat {
	// First try JSON - quick check
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' {
		var test map[string]interface{}
		if json.Unmarshal(trimmed, &test) == nil {
			return FormatJSON
		}
	}

	// Try protobuf - attempt to unmarshal as HeaderMessage
	var msg HeaderMessage
	if proto.Unmarshal(payload, &msg) == nil && len(msg.Header) > 0 {
		return FormatProtobuf
	}

	return FormatUnknown
}

// xorDecrypt decrypts XOR-encrypted pdata using the sequence number
func xorDecrypt(data []byte, seq int32) []byte {
	if len(data) == 0 {
		return data
	}

	result := make([]byte, len(data))
	seqByte := byte(seq & 0xFF) // Use lowest byte of sequence number

	for i, b := range data {
		result[i] = b ^ seqByte
	}

	return result
}

// DecodeProtobufMessage decodes a protobuf message and returns params map
func (d *ProtobufDecoder) DecodeProtobufMessage(payload []byte) (map[string]interface{}, error) {
	debugEnabled := isProtobufDebugEnabled()

	// Dump raw payload if debug enabled
	if debugEnabled {
		slog.Info("[PROTOBUF_DEBUG] Raw protobuf payload",
			"payload_len", len(payload),
			"payload_hex", hex.EncodeToString(payload))
	}

	// 1. Unmarshal the HeaderMessage
	var headerMsg HeaderMessage
	if err := proto.Unmarshal(payload, &headerMsg); err != nil {
		previewLen := min(256, len(payload)) // Larger preview for errors
		slog.Error("Failed to unmarshal protobuf message",
			"error", err,
			"payload_len", len(payload),
			"payload_hex", hex.EncodeToString(payload[:previewLen]),
			"help", "Please report to https://github.com/tess1o/go-ecoflow-exporter/issues with PROTOBUF_DEBUG=true logs")
		return nil, fmt.Errorf("failed to unmarshal protobuf message: %w", err)
	}

	if len(headerMsg.Header) == 0 {
		slog.Error("Protobuf message contains no headers",
			"payload_len", len(payload),
			"payload_hex", hex.EncodeToString(payload[:min(256, len(payload))]))
		return nil, fmt.Errorf("protobuf message contains no headers")
	}

	// Log summary of message structure
	if debugEnabled {
		slog.Info("[PROTOBUF_DEBUG] Decoding protobuf message",
			"header_count", len(headerMsg.Header),
			"payload_size", len(payload))

		// Dump full header structures
		for i, header := range headerMsg.Header {
			slog.Info("[PROTOBUF_DEBUG] Header structure",
				"header_index", i,
				"pdata_len", len(header.Pdata),
				"enc_type", header.EncType,
				"seq", header.Seq,
				"cmd_func", header.CmdFunc,
				"cmd_id", header.CmdId,
				"src", header.Src,
				"dest", header.Dest,
				"product_id", header.ProductId,
				"version", header.Version)
		}
	}

	// 2. Process all headers and merge their params
	params := make(map[string]interface{})
	encryptedCount := 0
	parsedCount := 0
	failedCount := 0

	for i, header := range headerMsg.Header {
		// Log raw pdata if debug enabled (before decryption)
		if debugEnabled {
			slog.Info("[PROTOBUF_DEBUG] Processing header pdata",
				"header_index", i,
				"pdata_len", len(header.Pdata),
				"pdata_hex", hex.EncodeToString(header.Pdata),
				"pdata_ascii", formatAscii(header.Pdata),
				"enc_type", header.EncType)
		}

		// 3. Get pdata and decrypt if necessary
		pdata := header.Pdata
		if header.EncType == 1 && header.Seq != 0 {
			encryptedCount++
			pdata = xorDecrypt(pdata, header.Seq)

			if debugEnabled {
				slog.Info("[PROTOBUF_DEBUG] Decrypted XOR-encrypted pdata",
					"header_index", i,
					"seq", header.Seq,
					"cmd_func", header.CmdFunc,
					"cmd_id", header.CmdId,
					"original_len", len(header.Pdata),
					"decrypted_len", len(pdata),
					"decrypted_hex", hex.EncodeToString(pdata),
					"decrypted_ascii", formatAscii(pdata))
			}
		}

		// 4. Parse pdata as JSON (most common case)
		var pdataParams map[string]interface{}
		if err := json.Unmarshal(pdata, &pdataParams); err == nil {
			// Successfully parsed as JSON - merge into params
			parsedCount++

			if debugEnabled {
				jsonBytes, _ := json.MarshalIndent(pdataParams, "", "  ")
				slog.Info("[PROTOBUF_DEBUG] Parsed pdata as JSON",
					"header_index", i,
					"cmd_func", header.CmdFunc,
					"cmd_id", header.CmdId,
					"param_count", len(pdataParams),
					"params_json", string(jsonBytes))
			}

			for k, v := range pdataParams {
				params[k] = v
			}
		} else {
			// Unknown pdata format - log for debugging
			failedCount++
			d.logUnknownCommand(header.CmdFunc, header.CmdId, pdata, err)
		}
	}

	// Log summary of what was extracted
	if debugEnabled {
		jsonBytes, _ := json.MarshalIndent(params, "", "  ")
		slog.Info("[PROTOBUF_DEBUG] Protobuf message decoded - FINAL PARAMS",
			"total_params", len(params),
			"headers_parsed", parsedCount,
			"headers_encrypted", encryptedCount,
			"headers_failed", failedCount,
			"all_params_json", string(jsonBytes))
	}

	if len(params) == 0 {
		slog.Error("No params extracted from protobuf message",
			"headers_total", len(headerMsg.Header),
			"headers_failed", failedCount,
			"help", "Enable PROTOBUF_DEBUG=true to see full message details")
		return nil, fmt.Errorf("no params extracted from protobuf message")
	}

	return params, nil
}

// formatAscii converts bytes to a safe ASCII representation
func formatAscii(data []byte) string {
	var sb strings.Builder
	for _, b := range data {
		if b >= 32 && b <= 126 {
			sb.WriteByte(b)
		} else {
			sb.WriteByte('.')
		}
	}
	return sb.String()
}

// logUnknownCommand logs unknown cmd_func/cmd_id combinations (ALWAYS logs, not gated by debug flag)
func (d *ProtobufDecoder) logUnknownCommand(cmdFunc, cmdId int32, pdata []byte, parseErr error) {
	key := fmt.Sprintf("%d:%d", cmdFunc, cmdId)
	d.unknownCommands[key]++
	count := d.unknownCommands[key]

	debugEnabled := isProtobufDebugEnabled()
	previewLen := 128 // Show more in preview for troubleshooting
	if len(pdata) < previewLen {
		previewLen = len(pdata)
	}

	// Log at INFO on first occurrence with full details for troubleshooting
	if count == 1 {
		logArgs := []interface{}{
			"cmd_func", cmdFunc,
			"cmd_id", cmdId,
			"pdata_len", len(pdata),
			"parse_error", parseErr.Error(),
			"pdata_hex_preview", hex.EncodeToString(pdata[:previewLen]),
			"pdata_ascii_preview", formatAscii(pdata[:previewLen]),
			"help", "Enable PROTOBUF_DEBUG=true and report to https://github.com/tess1o/go-ecoflow-exporter/issues",
		}

		// If debug enabled, include FULL pdata dump
		if debugEnabled {
			logArgs = append(logArgs,
				"pdata_hex_full", hex.EncodeToString(pdata),
				"pdata_ascii_full", formatAscii(pdata))
		}

		slog.Info("Unknown protobuf pdata format - cannot parse as JSON", logArgs...)
	}

	// Log at WARN if seen frequently (every 100 occurrences)
	if count%100 == 0 {
		slog.Warn("Frequently seen unknown protobuf command",
			"cmd_func", cmdFunc,
			"cmd_id", cmdId,
			"occurrence_count", count,
			"help", "This command type is not yet supported - enable PROTOBUF_DEBUG=true for full dumps")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
