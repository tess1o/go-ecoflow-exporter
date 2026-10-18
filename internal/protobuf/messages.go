package protobuf

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type commandKey struct {
	cmdFunc int32
	cmdId   int32
}

// bmsHeartbeatCommands lists cmd_func/cmd_id pairs that carry a BMSHeartBeatReport
var bmsHeartbeatCommands = map[commandKey]bool{
	{3, 1}: true, {3, 2}: true, {3, 30}: true, {3, 50}: true,
	{32, 1}: true, {32, 3}: true, {32, 50}: true, {32, 51}: true, {32, 52}: true,
	{254, 24}: true, {254, 25}: true, {254, 26}: true, {254, 27}: true,
	{254, 28}: true, {254, 29}: true, {254, 30}: true,
}

// newMessageFor returns an empty protobuf message matching the cmd_func/cmd_id pair,
// or nil if the command is not supported
func newMessageFor(cmdFunc, cmdId int32) proto.Message {
	switch {
	case cmdFunc == 254 && (cmdId == 21 || cmdId == 22):
		return &PropertyUpload{}
	case cmdFunc == 32 && cmdId == 2:
		return &CMSHeartBeatReport{}
	case bmsHeartbeatCommands[commandKey{cmdFunc, cmdId}]:
		return &BMSHeartBeatReport{}
	}
	return nil
}

// decodeProtobufPdata decodes binary protobuf pdata into flat params.
// supported is false when cmd_func/cmd_id has no known message definition.
// unknownFields contains field numbers present in pdata but missing from the schema.
func decodeProtobufPdata(cmdFunc, cmdId int32, pdata []byte) (params map[string]interface{}, unknownFields []protowire.Number, supported bool, err error) {
	msg := newMessageFor(cmdFunc, cmdId)
	if msg == nil {
		return nil, nil, false, nil
	}
	if err := proto.Unmarshal(pdata, msg); err != nil {
		return nil, nil, true, err
	}

	params = make(map[string]interface{})
	switch m := msg.(type) {
	case *BMSHeartBeatReport:
		// Devices with extra batteries send one report per pack, keep them apart
		flattenMessage(m.ProtoReflect(), fmt.Sprintf("bms.%d.", m.GetNum()), false, params)
	case *CMSHeartBeatReport:
		// Wrapper names (msg32_2_1) carry no meaning, leaf names are already cms-prefixed
		flattenMessage(m.ProtoReflect(), "", true, params)
	default:
		flattenMessage(m.ProtoReflect(), "", false, params)
	}
	return params, collectUnknownFields(msg.ProtoReflect()), true, nil
}

// flattenMessage writes all set scalar fields of m into out using their JSON (camelCase) names.
// Numbers and bools are stored as float64 to match JSON-decoded params, nested messages are
// flattened as "parent.child" (or without the parent name when inlineNested is true).
// Repeated fields and bytes are skipped.
func flattenMessage(m protoreflect.Message, prefix string, inlineNested bool, out map[string]interface{}) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.IsList() || fd.IsMap() {
			return true
		}
		key := prefix + fd.JSONName()
		switch fd.Kind() {
		case protoreflect.MessageKind, protoreflect.GroupKind:
			nestedPrefix := key + "."
			if inlineNested {
				nestedPrefix = prefix
			}
			flattenMessage(v.Message(), nestedPrefix, false, out)
		case protoreflect.BoolKind:
			if v.Bool() {
				out[key] = float64(1)
			} else {
				out[key] = float64(0)
			}
		case protoreflect.FloatKind, protoreflect.DoubleKind:
			out[key] = v.Float()
		case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
			protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
			out[key] = float64(v.Int())
		case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
			protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
			out[key] = float64(v.Uint())
		case protoreflect.EnumKind:
			out[key] = float64(v.Enum())
		case protoreflect.StringKind:
			out[key] = v.String()
		}
		return true
	})
}

// collectUnknownFields returns field numbers that are present in the message but not in the schema
func collectUnknownFields(m protoreflect.Message) []protowire.Number {
	var numbers []protowire.Number
	raw := m.GetUnknown()
	for len(raw) > 0 {
		num, _, n := protowire.ConsumeField(raw)
		if n < 0 {
			break
		}
		numbers = append(numbers, num)
		raw = raw[n:]
	}
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Kind() == protoreflect.MessageKind && !fd.IsList() && !fd.IsMap() {
			numbers = append(numbers, collectUnknownFields(v.Message())...)
		}
		return true
	})
	return numbers
}
