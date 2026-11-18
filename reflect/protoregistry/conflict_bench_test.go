// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package protoregistry_test

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// BenchmarkFindMessageByName measures the lookup hot path. Provenance maps
// are only written during registration, so lookup must remain unchanged.
func BenchmarkFindMessageByName(b *testing.B) {
	name, mt := messageTypeFor(b, "bench/lookup.proto", "bench.team", "Lookup")
	reg := new(protoregistry.Types)
	if err := reg.RegisterMessage(mt); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := reg.FindMessageByName(name)
		if err != nil || got != mt {
			b.Fatalf("FindMessageByName = (%v, %v), want the registered type", got, err)
		}
	}
}

// BenchmarkRegisterMessage measures the registration path including the
// provenance bookkeeping added for conflict detection and startup checks.
func BenchmarkRegisterMessage(b *testing.B) {
	names := make([]protoreflect.FullName, b.N)
	types := make([]protoreflect.MessageType, b.N)
	for i := range names {
		names[i], types[i] = messageTypeFor(b, "bench/register.proto", "bench.team", "Register")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reg := new(protoregistry.Types)
		if err := reg.RegisterMessage(types[i]); err != nil {
			b.Fatal(err)
		}
	}
}

var _ protoreflect.MessageType = fakeMessageType{}
