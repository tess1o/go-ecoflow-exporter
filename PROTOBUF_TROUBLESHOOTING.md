# Protobuf Support - Troubleshooting Guide

This guide explains how to troubleshoot protobuf message issues using the `PROTOBUF_DEBUG` environment variable.

## 🔧 Enabling Debug Mode

Set the environment variable before running:

```bash
PROTOBUF_DEBUG=true ./go-ecoflow-exporter
```

Or with Docker:

```bash
docker run -e PROTOBUF_DEBUG=true ...
```

## 📊 Default Behavior (PROTOBUF_DEBUG=false or unset)

**Minimal logging** - only errors are logged:

- ❌ Failed protobuf unmarshal (with hex preview)
- ❌ Unknown pdata format (first occurrence + every 100th)
- ❌ No params extracted
- ✅ Silent on successful parsing

**Use this for**: Normal production operation

## 🐛 Debug Mode (PROTOBUF_DEBUG=true)

**Comprehensive logging** - every step is logged at INFO level:

### 1. First Protobuf Message from Device

```
INFO [PROTOBUF_DEBUG] First protobuf message received from device
  device=HW51XXXXXXX
  note="This device uses protobuf format (likely Delta3/River3/STREAM Ultra)"
```

### 2. Raw Protobuf Payload

**Full hex dump of the entire protobuf message**:

```
INFO [PROTOBUF_DEBUG] Raw protobuf payload
  payload_len=234
  payload_hex=0a3e0a387b2261632e766f6c74616765223a323330...
```

### 3. Message Structure

```
INFO [PROTOBUF_DEBUG] Decoding protobuf message
  header_count=2
  payload_size=234
```

### 4. Each Header Structure

**All protobuf fields decoded**:

```
INFO [PROTOBUF_DEBUG] Header structure
  header_index=0
  pdata_len=180
  enc_type=1
  seq=12345
  cmd_func=20
  cmd_id=1
  src=0
  dest=0
  product_id=15
  version=1
```

### 5. Raw pdata (Before Decryption)

```
INFO [PROTOBUF_DEBUG] Processing header pdata
  header_index=0
  pdata_len=180
  pdata_hex=5108484b5e5e4f58...
  pdata_ascii=Q.HK^^OX...
  enc_type=1
```

### 6. Decrypted pdata (If Encrypted)

**Full decrypted content in hex AND ascii**:

```
INFO [PROTOBUF_DEBUG] Decrypted XOR-encrypted pdata
  header_index=0
  seq=12345
  cmd_func=20
  cmd_id=1
  original_len=180
  decrypted_len=180
  decrypted_hex=7b22626174746572792e6c6576656c223a3630...
  decrypted_ascii={"battery.level":60,"temperature":25.5}
```

### 7. Parsed JSON Parameters

**Pretty-printed JSON of extracted params**:

```
INFO [PROTOBUF_DEBUG] Parsed pdata as JSON
  header_index=0
  cmd_func=20
  cmd_id=1
  param_count=45
  params_json="{
    \"battery.level\": 60,
    \"ac.voltage\": 230,
    \"dc.current\": 3.8,
    ...
  }"
```

### 8. Final Merged Parameters

**All parameters merged from all headers**:

```
INFO [PROTOBUF_DEBUG] Protobuf message decoded - FINAL PARAMS
  total_params=47
  headers_parsed=2
  headers_encrypted=1
  headers_failed=0
  all_params_json="{
    \"battery.level\": 60,
    \"ac.voltage\": 230,
    ...
  }"
```

## ⚠️ Error Logging (Always Logged, Regardless of Debug Flag)

### Unknown pdata Format

**Always logged on first occurrence** with 128-byte preview:

```
INFO Unknown protobuf pdata format - cannot parse as JSON
  cmd_func=99
  cmd_id=88
  pdata_len=256
  parse_error="invalid character 'x' looking for beginning of value"
  pdata_hex_preview=0x1a2b3c4d... (128 bytes)
  pdata_ascii_preview=".+<M..." (128 bytes)
  help="Enable PROTOBUF_DEBUG=true and report to https://github.com/tess1o/go-ecoflow-exporter/issues"
```

**With PROTOBUF_DEBUG=true**, the full pdata is included:

```
  pdata_hex_full=0x1a2b3c4d... (FULL LENGTH)
  pdata_ascii_full=".+<M..." (FULL LENGTH)
```

### Failed Protobuf Unmarshal

**Always logged** with 256-byte hex preview:

```
ERROR Failed to unmarshal protobuf message
  error="proto: cannot parse invalid wire-format data"
  payload_len=512
  payload_hex=0xdeadbeef... (256 bytes)
  help="Please report to https://github.com/tess1o/go-ecoflow-exporter/issues with PROTOBUF_DEBUG=true logs"
```

## 📋 How to Ask Users for Logs

### Step 1: Quick Check (Default Logs)

Ask user to run normally and check for errors:

```bash
./go-ecoflow-exporter 2>&1 | tee output.log
```

Look for:
- ❌ `Unknown protobuf pdata format` - unsupported command type
- ❌ `Failed to unmarshal protobuf message` - invalid protobuf
- ❌ `No params extracted` - all headers failed

### Step 2: Full Debug Dump (If Issues Found)

Ask user to enable debug and capture full output:

```bash
PROTOBUF_DEBUG=true ./go-ecoflow-exporter 2>&1 | tee debug.log
```

Then ask them to share `debug.log` with:
- Device model (Delta 3, River 3, STREAM Ultra, etc.)
- What's not working (specific metrics missing, all data missing, etc.)
- Relevant section of the log

## 🔍 Common Troubleshooting Scenarios

### Scenario 1: No Data from Device

**Symptoms**: Device appears offline, no metrics

**Debug Steps**:
1. Run with `PROTOBUF_DEBUG=true`
2. Check if `First protobuf message received` appears
3. If NO: Device not sending data or wrong serial number configured
4. If YES but no params: Check `headers_failed` count

### Scenario 2: Encrypted Data Not Decrypting

**Symptoms**: `Unknown protobuf pdata format` with garbage in ascii preview

**Debug Steps**:
1. Run with `PROTOBUF_DEBUG=true`
2. Look at `Header structure` → check `enc_type` field
3. If `enc_type=1` but NO `Decrypted XOR-encrypted pdata` log → bug in decryption logic
4. If decryption happens but `parse_error` → wrong encryption method or corrupted data

### Scenario 3: Some Metrics Missing

**Symptoms**: Battery shows but voltage doesn't

**Debug Steps**:
1. Run with `PROTOBUF_DEBUG=true`
2. Check `Parsed pdata as JSON` → see if missing metric is in the JSON
3. If present in JSON but not in Prometheus → handler issue (not protobuf)
4. If NOT in JSON → device doesn't send that metric in this command type

### Scenario 4: Different Protobuf Structure

**Symptoms**: `Failed to unmarshal protobuf message`

**Debug Steps**:
1. Run with `PROTOBUF_DEBUG=true` (will show 256-byte hex preview on error)
2. Share full `payload_hex` from error log
3. Device likely uses different protobuf schema (new model?)
4. Need to reverse-engineer the proto definition

### Scenario 5: Unknown Command Type

**Symptoms**: `Unknown protobuf pdata format` appears repeatedly

**Debug Steps**:
1. Run with `PROTOBUF_DEBUG=true`
2. Look at `cmd_func` and `cmd_id` values
3. Check `pdata_hex_full` - might be binary data, not JSON
4. Report to GitHub with full debug log - may need new parser for this command type

## 🎯 Example: Successful Debug Session

```
# User reports: "Delta 3 shows battery but no AC voltage"

$ PROTOBUF_DEBUG=true ./go-ecoflow-exporter

INFO [PROTOBUF_DEBUG] First protobuf message received from device device=HW51ZJH6EB12345
INFO [PROTOBUF_DEBUG] Raw protobuf payload payload_hex=0a3e0a38...
INFO [PROTOBUF_DEBUG] Header structure cmd_func=20 cmd_id=1 enc_type=1 seq=12345
INFO [PROTOBUF_DEBUG] Decrypted XOR-encrypted pdata decrypted_ascii={"battery.level":85.5}
INFO [PROTOBUF_DEBUG] Parsed pdata as JSON param_count=1 params_json={"battery.level":85.5}

# Analysis: Device only sends battery in this message!
# AC voltage likely in different cmd_func/cmd_id
# Need to wait for next message or check if device actually reports AC
```

## 🎯 Example: Troubleshooting Unknown Format

```
# User reports: "River 3 not showing any data"

$ PROTOBUF_DEBUG=true ./go-ecoflow-exporter

INFO [PROTOBUF_DEBUG] First protobuf message received from device device=HW52ABC123
INFO [PROTOBUF_DEBUG] Raw protobuf payload payload_hex=0a120a0c0801101a...
INFO [PROTOBUF_DEBUG] Header structure cmd_func=99 cmd_id=88 enc_type=0
INFO [PROTOBUF_DEBUG] Processing header pdata pdata_hex=0801101a18021f20...
INFO Unknown protobuf pdata format cmd_func=99 cmd_id=88 pdata_hex_full=0801101a...

# Analysis: cmd_func=99 is unknown, pdata is NOT JSON (starts with 0x08 = protobuf varint)
# This command uses nested protobuf, not JSON!
# Need to define new proto message for this command type
```

## 📝 What to Include in Bug Reports

When reporting issues on GitHub:

1. **Device Model**: Delta 3 / River 3 / STREAM Ultra / etc.
2. **Firmware Version**: (if known)
3. **Full Debug Log**: Run with `PROTOBUF_DEBUG=true` for at least 2-3 messages
4. **Symptom**: What metrics are missing or wrong
5. **Frequency**: Does it always fail or intermittently?

## 🔒 Privacy Note

⚠️ **Debug logs contain device data** (battery levels, voltages, etc.)

If privacy is a concern:
- Redact device serial numbers
- Share only the structural parts (hex dumps, not final params)
- Or share privately via email/DM instead of public GitHub

## 🚀 Performance Impact

**PROTOBUF_DEBUG=true** impact:
- 📊 Logs are verbose (5-10x more log volume)
- ⏱️ Minimal CPU impact (<1% overhead)
- 💾 Disk I/O from logging
- ✅ Safe for temporary troubleshooting in production

**Recommendation**: Enable only when actively troubleshooting, disable after

## 🔗 Getting Help

- **GitHub Issues**: https://github.com/tess1o/go-ecoflow-exporter/issues
- **Label**: Use `protobuf` label
- **Template**: Include device model + full debug log + symptom description
