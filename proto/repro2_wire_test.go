// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package proto_test

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	testpb "google.golang.org/protobuf/internal/testprotos/test"
	test3pb "google.golang.org/protobuf/internal/testprotos/test3"
)

// unknownWire is a valid wire payload with field numbers not in TestAllTypes.
var unknownWire = func() []byte {
	var b []byte
	b = protowire.AppendTag(b, 9001, protowire.VarintType)
	b = protowire.AppendVarint(b, 123456789)
	b = protowire.AppendTag(b, 9002, protowire.BytesType)
	b = protowire.AppendString(b, "unknown payload content")
	b = protowire.AppendTag(b, 9003, protowire.Fixed32Type)
	b = protowire.AppendFixed32(b, 42)
	b = protowire.AppendTag(b, 9004, protowire.Fixed64Type)
	b = protowire.AppendFixed64(b, 424242)
	return b
}()

func checkRoundTrip(t *testing.T, name string, m proto.Message) {
	t.Helper()
	unknown := m.ProtoReflect().GetUnknown()
	if len(unknown) == 0 {
		t.Fatalf("%s: expected unknown fields, got none", name)
	}
	size := proto.Size(m)
	wire, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("%s: Marshal: %v", name, err)
	}
	t.Logf("%s: unknownLen=%2d Size=%3d wireLen=%3d match=%v", name, len(unknown), size, len(wire), size == len(wire))
	if size != len(wire) {
		t.Errorf("%s: MISMATCH Size=%d len(Marshal)=%d", name, size, len(wire))
	}
	round := m.ProtoReflect().New().Interface()
	if err := proto.Unmarshal(wire, round); err != nil {
		t.Fatalf("%s: Unmarshal: %v", name, err)
	}
	if string(round.ProtoReflect().GetUnknown()) != string(unknown) {
		t.Errorf("%s: unknown fields not preserved", name)
	}
}

func TestWireUnknownFastPath(t *testing.T) {
	m := &testpb.TestAllTypes{}
	if err := proto.Unmarshal(unknownWire, m); err != nil {
		t.Fatal(err)
	}
	checkRoundTrip(t, "fastpath", m)

	// Now with known fields mixed in.
	m.OptionalInt32 = proto.Int32(7)
	m.OptionalString = proto.String("known")
	checkRoundTrip(t, "fastpath-mixed", m)
}

func TestWireUnknownOpaque(t *testing.T) {
	m := &test3pb.TestAllTypes{}
	if err := proto.Unmarshal(unknownWire, m); err != nil {
		t.Fatal(err)
	}
	checkRoundTrip(t, "opaque", m)
	m.OptionalInt32 = proto.Int32(7)
	checkRoundTrip(t, "opaque-mixed", m)
}

func TestWireUnknownDynamic(t *testing.T) {
	desc := (&testpb.TestAllTypes{}).ProtoReflect().Descriptor()
	m := dynamicpb.NewMessage(desc)
	if err := proto.Unmarshal(unknownWire, m); err != nil {
		t.Fatal(err)
	}
	checkRoundTrip(t, "dynamic", m)
	fd := desc.Fields().ByName("optional_int32")
	m.Set(fd, protoreflect.ValueOfInt32(7))
	checkRoundTrip(t, "dynamic-mixed", m)
}

func TestWireUnknownNested(t *testing.T) {
	outer := &testpb.TestAllTypes{
		OptionalNestedMessage: &testpb.TestAllTypes_NestedMessage{},
	}
	if err := proto.Unmarshal(unknownWire, outer.OptionalNestedMessage); err != nil {
		t.Fatal(err)
	}
	size := proto.Size(outer)
	wire, err := proto.Marshal(outer)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("nested: Size=%d wireLen=%d", size, len(wire))
	if size != len(wire) {
		t.Errorf("nested MISMATCH Size=%d len(Marshal)=%d", size, len(wire))
	}
	got := &testpb.TestAllTypes{}
	if err := proto.Unmarshal(wire, got); err != nil {
		t.Fatal(err)
	}
	if string(got.GetOptionalNestedMessage().ProtoReflect().GetUnknown()) != string(unknownWire) {
		t.Errorf("nested unknown not preserved")
	}
}

// Normal message without unknown fields, baseline regression check.
func TestWireKnownOnly(t *testing.T) {
	for name, m := range map[string]proto.Message{
		"fastpath": &testpb.TestAllTypes{OptionalInt32: proto.Int32(7), OptionalString: proto.String("s")},
		"opaque":   &test3pb.TestAllTypes{OptionalInt32: proto.Int32(7)},
		"dynamic": func() proto.Message {
			desc := (&testpb.TestAllTypes{}).ProtoReflect().Descriptor()
			m := dynamicpb.NewMessage(desc)
			m.Set(desc.Fields().ByName("optional_int32"), protoreflect.ValueOfInt32(7))
			return m
		}(),
	} {
		size := proto.Size(m)
		wire, err := proto.Marshal(m)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("%s known-only: Size=%d wireLen=%d", name, size, len(wire))
		if size != len(wire) {
			t.Errorf("%s: MISMATCH Size=%d len(Marshal)=%d", name, size, len(wire))
		}
	}
}

func TestWireUnknownLarge(t *testing.T) {
	// Large unknown payload: the length prefix needs a multi-byte varint,
	// and buffer allocation is directly driven by the computed size.
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	var wire []byte
	wire = protowire.AppendTag(wire, 77777, protowire.BytesType)
	wire = protowire.AppendBytes(wire, payload)

	for name, newMsg := range map[string]func() proto.Message{
		"fastpath": func() proto.Message { return &testpb.TestAllTypes{} },
		"opaque":   func() proto.Message { return &test3pb.TestAllTypes{} },
		"dynamic": func() proto.Message {
			return dynamicpb.NewMessage((&testpb.TestAllTypes{}).ProtoReflect().Descriptor())
		},
	} {
		m := newMsg()
		if err := proto.Unmarshal(wire, m); err != nil {
			t.Fatalf("%s: Unmarshal: %v", name, err)
		}
		checkRoundTrip(t, "large-"+name, m)

		// Exact preallocation based on Size must be sufficient: no truncation.
		sized := make([]byte, 0, proto.Size(m))
		out, err := proto.MarshalOptions{}.MarshalAppend(sized, m)
		if err != nil {
			t.Fatalf("%s: MarshalAppend: %v", name, err)
		}
		if cap(out) != proto.Size(m) {
			t.Errorf("%s: buffer was reallocated: cap=%d Size=%d", name, cap(out), proto.Size(m))
		}
		if len(out) != proto.Size(m) {
			t.Errorf("%s: len(out)=%d Size=%d", name, len(out), proto.Size(m))
		}
	}
}
