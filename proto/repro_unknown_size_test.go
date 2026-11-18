// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package proto_test

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/testing/protopack"

	"google.golang.org/protobuf/types/dynamicpb"

	testpb "google.golang.org/protobuf/internal/testprotos/test"
	test3pb "google.golang.org/protobuf/internal/testprotos/test3"
)

func TestReproUnknownSizeFastPath(t *testing.T) {
	unknown := protopack.Message{
		protopack.Tag{Number: 10000, Type: protopack.VarintType}, protopack.Varint(12345),
		protopack.Tag{Number: 10001, Type: protopack.BytesType}, protopack.String("hello unknown"),
	}.Marshal()

	m := &testpb.TestAllTypes{
		OptionalInt32: proto.Int32(42),
	}
	m.ProtoReflect().SetUnknown(unknown)

	size := proto.Size(m)
	wire, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	t.Logf("fast path: unknown=%d Size=%d len(wire)=%d", len(unknown), size, len(wire))
	if size != len(wire) {
		t.Errorf("MISMATCH: Size(m)=%d; len(Marshal(m))=%d", size, len(wire))
	}

	got := &testpb.TestAllTypes{}
	if err := proto.Unmarshal(wire, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if string(got.ProtoReflect().GetUnknown()) != string(unknown) {
		t.Errorf("unknown fields not preserved")
	}
}

func TestReproUnknownSizeOpaque(t *testing.T) {
	unknown := protopack.Message{
		protopack.Tag{Number: 10000, Type: protopack.VarintType}, protopack.Varint(12345),
		protopack.Tag{Number: 10001, Type: protopack.BytesType}, protopack.String("hello unknown"),
	}.Marshal()

	m := &test3pb.TestAllTypes{
		OptionalInt32: proto.Int32(42),
	}
	m.ProtoReflect().SetUnknown(unknown)

	size := proto.Size(m)
	wire, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	t.Logf("opaque path: unknown=%d Size=%d len(wire)=%d", len(unknown), size, len(wire))
	if size != len(wire) {
		t.Errorf("MISMATCH: Size(m)=%d; len(Marshal(m))=%d", size, len(wire))
	}

	got := &test3pb.TestAllTypes{}
	if err := proto.Unmarshal(wire, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if string(got.ProtoReflect().GetUnknown()) != string(unknown) {
		t.Errorf("unknown fields not preserved")
	}
}

func TestReproUnknownSizeNested(t *testing.T) {
	unknown := protopack.Message{
		protopack.Tag{Number: 10000, Type: protopack.VarintType}, protopack.Varint(12345),
		protopack.Tag{Number: 10001, Type: protopack.BytesType}, protopack.String("hello unknown"),
	}.Marshal()

	inner := &testpb.TestAllTypes_NestedMessage{A: proto.Int32(11)}
	inner.ProtoReflect().SetUnknown(unknown)
	m := &testpb.TestAllTypes{
		OptionalNestedMessage: inner,
	}

	size := proto.Size(m)
	wire, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	t.Logf("nested fast path: unknown=%d Size=%d len(wire)=%d", len(unknown), size, len(wire))
	if size != len(wire) {
		t.Errorf("MISMATCH: Size(m)=%d; len(Marshal(m))=%d", size, len(wire))
	}

	got := &testpb.TestAllTypes{}
	if err := proto.Unmarshal(wire, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if string(got.GetOptionalNestedMessage().ProtoReflect().GetUnknown()) != string(unknown) {
		t.Errorf("unknown fields not preserved in nested message")
	}
}

func TestReproUnknownSizeDynamic(t *testing.T) {
	unknown := protopack.Message{
		protopack.Tag{Number: 10000, Type: protopack.VarintType}, protopack.Varint(12345),
		protopack.Tag{Number: 10001, Type: protopack.BytesType}, protopack.String("hello unknown"),
	}.Marshal()

	m := dynamicpb.NewMessage((&testpb.TestAllTypes{}).ProtoReflect().Descriptor())
	fd := m.Descriptor().Fields().ByName("optional_int32")
	m.Set(fd, protoreflect.ValueOfInt32(42))
	m.SetUnknown(unknown)

	size := proto.Size(m)
	wire, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	t.Logf("dynamic slow path: unknown=%d Size=%d len(wire)=%d", len(unknown), size, len(wire))
	if size != len(wire) {
		t.Errorf("MISMATCH: Size(m)=%d; len(Marshal(m))=%d", size, len(wire))
	}

	got := dynamicpb.NewMessage(m.Descriptor())
	if err := proto.Unmarshal(wire, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if string(got.GetUnknown()) != string(unknown) {
		t.Errorf("unknown fields not preserved")
	}
}

func TestReproUnknownSizeCacheMutation(t *testing.T) {
	unknown := protopack.Message{
		protopack.Tag{Number: 10000, Type: protopack.VarintType}, protopack.Varint(12345),
	}.Marshal()

	m := &testpb.TestAllTypes{OptionalInt32: proto.Int32(42)}
	sizeBefore := proto.Size(m)
	m.ProtoReflect().SetUnknown(unknown)
	sizeAfter := proto.Size(m)
	wire, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	t.Logf("cache mutation: sizeBefore=%d sizeAfter=%d len(wire)=%d", sizeBefore, sizeAfter, len(wire))
	if sizeAfter != len(wire) {
		t.Errorf("MISMATCH: Size(m)=%d; len(Marshal(m))=%d", sizeAfter, len(wire))
	}
}
