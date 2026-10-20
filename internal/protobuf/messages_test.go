package protobuf

import (
	"encoding/hex"
	"math"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
)

// pdata samples captured from real devices (issues #25 and #26)
const (
	delta3DisplayPowerHex = "1d00006b432500006b43b50300006b43851700006bc3"
	delta3DisplayTempHex  = "a81023"
	delta3CMSHex          = "0a4208011002180120c8df022898753000385a400348585000580060ffe40868803770017d3dcaaf42820103030301880102900103980100a00100a8010ab0010bb8015a120b0805100018012000288302"
	streamUltraDisplayHex = "c526357a6042"
	streamUltraBMSHex     = "080010011802200028d0808410305238c599014094d7ffffffffffffff01481f500158a08d0660b1800568a08d06703478648001cd198801ca1990011f98011da00121a80121b00100b80100c001d08603cd0170f0a342d00100d80166e001a507e80103f00100f801038002068a020ccb19ca19cc19cb19cc19cd199002029a02021f1da2020656302e302e30a8028402b002ffff03ba0210424b3331373030414448345630353533c0023ac80205d5020000a442dd0280bdb93ee502a4f0a342e802ffffffff0ff00203f802018003008803009003c4a0c2029803c0c8b302a5035afcc742ad030000b042b5032bf8c742b80301c2030121c80300d80301e203011ee80300f8030080040088041e90041e980400a00400a80400b20406000000000000b80400c00400c80400d00400d80400e00400e80400f00400f804f4bc068005e495068a0510424b3131503730394248355330343234900500980501a00500a80505b00508ba05081e21222220202020"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("invalid hex: %v", err)
	}
	return b
}

func decodeSingleHeader(t *testing.T, cmdFunc, cmdId int32, pdata []byte) map[string]interface{} {
	t.Helper()
	payload, err := proto.Marshal(&HeaderMessage{
		Header: []*Header{{Pdata: pdata, CmdFunc: cmdFunc, CmdId: cmdId, Src: 2, Dest: 32}},
	})
	if err != nil {
		t.Fatalf("Failed to marshal protobuf: %v", err)
	}
	params, err := NewProtobufDecoder(false).DecodeProtobufMessage(payload)
	if err != nil {
		t.Fatalf("DecodeProtobufMessage() error = %v", err)
	}
	return params
}

func assertFloat(t *testing.T, params map[string]interface{}, key string, want float64) {
	t.Helper()
	got, ok := params[key].(float64)
	if !ok {
		t.Errorf("params[%s] = %v (%T), want float64 %v", key, params[key], params[key], want)
		return
	}
	if math.Abs(got-want) > 0.01 {
		t.Errorf("params[%s] = %v, want %v", key, got, want)
	}
}

func TestDecodeProtobufPdata_Delta3DisplayProperty(t *testing.T) {
	params := decodeSingleHeader(t, 254, 21, mustHex(t, delta3DisplayPowerHex))

	assertFloat(t, params, "powInSumW", 235)
	assertFloat(t, params, "powOutSumW", 235)
	assertFloat(t, params, "powGetAcIn", 235)
	assertFloat(t, params, "powGetAcOut", -235)
	if len(params) != 4 {
		t.Errorf("len(params) = %d, want 4: %v", len(params), params)
	}
}

func TestDecodeProtobufPdata_ZeroAndSmallValues(t *testing.T) {
	params := decodeSingleHeader(t, 254, 21, mustHex(t, delta3DisplayTempHex))
	assertFloat(t, params, "bmsMaxMosTemp", 35)
}

func TestDecodeProtobufPdata_Delta3CMSHeartbeat(t *testing.T) {
	params := decodeSingleHeader(t, 32, 2, mustHex(t, delta3CMSHex))

	assertFloat(t, params, "cmsBattSoc", 87.89)
	assertFloat(t, params, "cmsBattSocInt", 88)
	assertFloat(t, params, "cmsBattVolMv", 45000)
	assertFloat(t, params, "cmsChgDsgState", 1)
	assertFloat(t, params, "cmsMaxChgSoc", 90)
	for key := range params {
		if strings.Contains(key, ".") {
			t.Errorf("CMS heartbeat wrapper names should not be part of the keys, got %s", key)
		}
	}
}

func TestDecodeProtobufPdata_StreamUltraDisplayProperty(t *testing.T) {
	params := decodeSingleHeader(t, 254, 21, mustHex(t, streamUltraDisplayHex))
	assertFloat(t, params, "gridConnectionPower", 56.12)
}

func TestDecodeProtobufPdata_StreamUltraBMSHeartbeat(t *testing.T) {
	params := decodeSingleHeader(t, 32, 50, mustHex(t, streamUltraBMSHex))

	assertFloat(t, params, "bms.0.soc", 82)
	assertFloat(t, params, "bms.0.num", 0)
	if _, ok := params["bms.0.cellVol"]; ok {
		t.Error("repeated fields should be skipped")
	}
	if _, ok := params["soc"]; ok {
		t.Error("BMS heartbeat keys should be prefixed with the pack number")
	}
}

func TestDecodeProtobufPdata_UnsupportedCommand(t *testing.T) {
	_, _, supported, err := decodeProtobufPdata(99, 88, []byte{0x08, 0x01})
	if supported || err != nil {
		t.Errorf("decodeProtobufPdata() supported = %v, err = %v, want false, nil", supported, err)
	}
}

func TestDecodeProtobufPdata_UnknownFields(t *testing.T) {
	// Field 1390 (varint) is not part of the schema yet
	_, unknown, supported, err := decodeProtobufPdata(254, 22, []byte{0xf0, 0x56, 0x01})
	if !supported || err != nil {
		t.Fatalf("decodeProtobufPdata() supported = %v, err = %v", supported, err)
	}
	if len(unknown) != 1 || unknown[0] != 1390 {
		t.Errorf("unknown fields = %v, want [1390]", unknown)
	}
}

func TestDecodeProtobufMessage_EmptyKnownMessageIsNotAnError(t *testing.T) {
	payload, _ := proto.Marshal(&HeaderMessage{
		Header: []*Header{{Pdata: []byte{0xf0, 0x56, 0x01}, CmdFunc: 254, CmdId: 22}},
	})
	params, err := NewProtobufDecoder(false).DecodeProtobufMessage(payload)
	if err != nil {
		t.Fatalf("DecodeProtobufMessage() error = %v", err)
	}
	if len(params) != 0 {
		t.Errorf("len(params) = %d, want 0", len(params))
	}
}

func TestDecodeProtobufMessage_NoXORForSrc32(t *testing.T) {
	payload, _ := proto.Marshal(&HeaderMessage{
		Header: []*Header{{
			Pdata:   mustHex(t, delta3DisplayTempHex),
			EncType: 1,
			Seq:     0x55,
			Src:     32,
			CmdFunc: 254,
			CmdId:   21,
		}},
	})
	params, err := NewProtobufDecoder(false).DecodeProtobufMessage(payload)
	if err != nil {
		t.Fatalf("DecodeProtobufMessage() error = %v", err)
	}
	assertFloat(t, params, "bmsMaxMosTemp", 35)
}

func TestDecodeProtobufMessage_XORProtobufPdata(t *testing.T) {
	seq := int32(0x1234)
	payload, _ := proto.Marshal(&HeaderMessage{
		Header: []*Header{{
			Pdata:   xorDecrypt(mustHex(t, delta3DisplayTempHex), seq),
			EncType: 1,
			Seq:     seq,
			Src:     2,
			CmdFunc: 254,
			CmdId:   21,
		}},
	})
	params, err := NewProtobufDecoder(false).DecodeProtobufMessage(payload)
	if err != nil {
		t.Fatalf("DecodeProtobufMessage() error = %v", err)
	}
	assertFloat(t, params, "bmsMaxMosTemp", 35)
}
