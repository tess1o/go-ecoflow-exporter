package protobuf

import (
	"encoding/json"
	"fmt"
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestDetectFormat_JSON(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		want    MessageFormat
	}{
		{
			name:    "valid JSON object",
			payload: []byte(`{"key": "value"}`),
			want:    FormatJSON,
		},
		{
			name:    "JSON with whitespace",
			payload: []byte(`  {"key": "value"}  `),
			want:    FormatJSON,
		},
		{
			name:    "complex JSON",
			payload: []byte(`{"params": {"soc": 50, "voltage": 12.5}}`),
			want:    FormatJSON,
		},
		{
			name:    "empty JSON object",
			payload: []byte(`{}`),
			want:    FormatJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectFormat(tt.payload)
			if got != tt.want {
				t.Errorf("DetectFormat() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectFormat_Protobuf(t *testing.T) {
	// Create a valid protobuf message
	msg := &HeaderMessage{
		Header: []*Header{
			{
				Pdata:   []byte(`{"soc": 50}`),
				EncType: 0,
				CmdFunc: 1,
				CmdId:   2,
				Seq:     100,
			},
		},
	}

	payload, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to marshal protobuf: %v", err)
	}

	got := DetectFormat(payload)
	if got != FormatProtobuf {
		t.Errorf("DetectFormat() = %v, want %v", got, FormatProtobuf)
	}
}

func TestDetectFormat_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{
			name:    "empty payload",
			payload: []byte{},
		},
		{
			name:    "invalid JSON",
			payload: []byte(`{invalid json`),
		},
		{
			name:    "random bytes",
			payload: []byte{0x00, 0x01, 0x02, 0x03},
		},
		{
			name:    "text string",
			payload: []byte("hello world"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectFormat(tt.payload)
			if got != FormatUnknown {
				t.Errorf("DetectFormat() = %v, want %v", got, FormatUnknown)
			}
		})
	}
}

func TestXORDecrypt(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		seq  int32
		want []byte
	}{
		{
			name: "basic decryption",
			data: []byte{0x10, 0x20, 0x30, 0x40},
			seq:  0x0F,
			want: []byte{0x1F, 0x2F, 0x3F, 0x4F},
		},
		{
			name: "seq uses only lowest byte",
			data: []byte{0xFF, 0xAA},
			seq:  0x12345678, // Only 0x78 matters
			want: []byte{0xFF ^ 0x78, 0xAA ^ 0x78},
		},
		{
			name: "empty data",
			data: []byte{},
			seq:  100,
			want: []byte{},
		},
		{
			name: "zero seq",
			data: []byte{0x10, 0x20},
			seq:  0,
			want: []byte{0x10, 0x20},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := xorDecrypt(tt.data, tt.seq)
			if len(got) != len(tt.want) {
				t.Errorf("xorDecrypt() length = %v, want %v", len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("xorDecrypt()[%d] = %x, want %x", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestXORDecrypt_Reversible(t *testing.T) {
	// XOR is reversible - encrypting twice with same key gives original
	original := []byte("test data for encryption")
	seq := int32(42)

	encrypted := xorDecrypt(original, seq)
	decrypted := xorDecrypt(encrypted, seq)

	if string(decrypted) != string(original) {
		t.Errorf("XOR decryption not reversible: got %v, want %v", decrypted, original)
	}
}

func TestDecodeProtobufMessage_NoEncryption(t *testing.T) {
	// Create a protobuf message with unencrypted JSON data
	pdataJSON := map[string]interface{}{
		"soc":     50,
		"voltage": 12.5,
		"status":  "charging",
	}
	pdataBytes, _ := json.Marshal(pdataJSON)

	msg := &HeaderMessage{
		Header: []*Header{
			{
				Pdata:   pdataBytes,
				EncType: 0, // No encryption
				CmdFunc: 1,
				CmdId:   2,
				Seq:     0,
			},
		},
	}

	payload, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to marshal protobuf: %v", err)
	}

	decoder := NewProtobufDecoder()
	params, err := decoder.DecodeProtobufMessage(payload)
	if err != nil {
		t.Fatalf("DecodeProtobufMessage() error = %v", err)
	}

	// Verify all params were extracted
	if params["soc"] != float64(50) { // JSON numbers are float64
		t.Errorf("params[soc] = %v, want %v", params["soc"], 50)
	}
	if params["voltage"] != 12.5 {
		t.Errorf("params[voltage] = %v, want %v", params["voltage"], 12.5)
	}
	if params["status"] != "charging" {
		t.Errorf("params[status] = %v, want %v", params["status"], "charging")
	}
}

func TestDecodeProtobufMessage_WithEncryption(t *testing.T) {
	// Create JSON data
	pdataJSON := map[string]interface{}{
		"battery_level": 75,
		"temperature":   25.5,
	}
	pdataBytes, _ := json.Marshal(pdataJSON)

	// Encrypt it
	seq := int32(123)
	encryptedPdata := xorDecrypt(pdataBytes, seq)

	msg := &HeaderMessage{
		Header: []*Header{
			{
				Pdata:   encryptedPdata,
				EncType: 1, // XOR encryption
				CmdFunc: 10,
				CmdId:   20,
				Seq:     seq,
			},
		},
	}

	payload, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to marshal protobuf: %v", err)
	}

	decoder := NewProtobufDecoder()
	params, err := decoder.DecodeProtobufMessage(payload)
	if err != nil {
		t.Fatalf("DecodeProtobufMessage() error = %v", err)
	}

	// Verify decryption worked
	if params["battery_level"] != float64(75) {
		t.Errorf("params[battery_level] = %v, want %v", params["battery_level"], 75)
	}
	if params["temperature"] != 25.5 {
		t.Errorf("params[temperature] = %v, want %v", params["temperature"], 25.5)
	}
}

func TestDecodeProtobufMessage_MultipleHeaders(t *testing.T) {
	// Test that multiple headers merge their params correctly
	pdata1, _ := json.Marshal(map[string]interface{}{"param1": "value1", "shared": "first"})
	pdata2, _ := json.Marshal(map[string]interface{}{"param2": "value2", "shared": "second"})

	msg := &HeaderMessage{
		Header: []*Header{
			{
				Pdata:   pdata1,
				EncType: 0,
				CmdFunc: 1,
				CmdId:   1,
			},
			{
				Pdata:   pdata2,
				EncType: 0,
				CmdFunc: 2,
				CmdId:   2,
			},
		},
	}

	payload, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to marshal protobuf: %v", err)
	}

	decoder := NewProtobufDecoder()
	params, err := decoder.DecodeProtobufMessage(payload)
	if err != nil {
		t.Fatalf("DecodeProtobufMessage() error = %v", err)
	}

	// Should have all params, with later header overwriting shared key
	if params["param1"] != "value1" {
		t.Errorf("params[param1] = %v, want %v", params["param1"], "value1")
	}
	if params["param2"] != "value2" {
		t.Errorf("params[param2] = %v, want %v", params["param2"], "value2")
	}
	if params["shared"] != "second" {
		t.Errorf("params[shared] = %v, want %v (should be overwritten by second header)", params["shared"], "second")
	}
}

func TestDecodeProtobufMessage_EmptyMessage(t *testing.T) {
	msg := &HeaderMessage{
		Header: []*Header{},
	}

	payload, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to marshal protobuf: %v", err)
	}

	decoder := NewProtobufDecoder()
	_, err = decoder.DecodeProtobufMessage(payload)
	if err == nil {
		t.Error("DecodeProtobufMessage() should error on empty headers")
	}
}

func TestDecodeProtobufMessage_InvalidProtobuf(t *testing.T) {
	decoder := NewProtobufDecoder()
	_, err := decoder.DecodeProtobufMessage([]byte("not a protobuf message"))
	if err == nil {
		t.Error("DecodeProtobufMessage() should error on invalid protobuf")
	}
}

func TestDecodeProtobufMessage_NonJSONPdata(t *testing.T) {
	// Test that non-JSON pdata is handled gracefully
	msg := &HeaderMessage{
		Header: []*Header{
			{
				Pdata:   []byte("not json data"),
				EncType: 0,
				CmdFunc: 99,
				CmdId:   88,
			},
		},
	}

	payload, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to marshal protobuf: %v", err)
	}

	decoder := NewProtobufDecoder()
	_, err = decoder.DecodeProtobufMessage(payload)
	if err == nil {
		t.Error("DecodeProtobufMessage() should error when no params are extracted")
	}
}

func TestLogUnknownCommand(t *testing.T) {
	decoder := NewProtobufDecoder()

	// First call should log at INFO (no assertion, just checking no panic)
	testErr := fmt.Errorf("test parse error")
	decoder.logUnknownCommand(10, 20, []byte("test data"), testErr)

	// Verify count is tracked
	count := decoder.unknownCommands["10:20"]
	if count != 1 {
		t.Errorf("unknownCommands[10:20] = %v, want %v", count, 1)
	}

	// Call 99 more times to reach 100
	for i := 0; i < 99; i++ {
		decoder.logUnknownCommand(10, 20, []byte("test data"), testErr)
	}

	// Should have logged at WARN on 100th occurrence
	count = decoder.unknownCommands["10:20"]
	if count != 100 {
		t.Errorf("unknownCommands[10:20] = %v, want %v", count, 100)
	}
}
