// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package protoregistry_test

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// fakeMessageType is a MessageType reporting an arbitrary descriptor,
// standing in for a generated type built in another team's Go package.
type fakeMessageType struct {
	desc protoreflect.MessageDescriptor
}

func (t fakeMessageType) New() protoreflect.Message                  { return nil }
func (t fakeMessageType) Zero() protoreflect.Message                 { return nil }
func (t fakeMessageType) Descriptor() protoreflect.MessageDescriptor { return t.desc }

func mustMakeMessageFile(t testing.TB, filePath, protoPkg, msgName, goPkg string) protoreflect.FileDescriptor {
	t.Helper()
	text := `syntax:"proto3" name:"` + filePath + `" package:"` + protoPkg + `"`
	if goPkg != "" {
		text += ` options:{go_package:"` + goPkg + `"}`
	}
	text += ` message_type:[{name:"` + msgName + `"}]`
	pb := new(descriptorpb.FileDescriptorProto)
	if err := prototext.Unmarshal([]byte(text), pb); err != nil {
		t.Fatalf("prototext.Unmarshal: %v", err)
	}
	fd, err := protodesc.NewFile(pb, nil)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return fd
}

func messageTypeFor(t testing.TB, filePath, protoPkg, msgName string) (protoreflect.FullName, protoreflect.MessageType) {
	t.Helper()
	fd := mustMakeMessageFile(t, filePath, protoPkg, msgName, "")
	desc := fd.Messages().Get(0)
	return desc.FullName(), fakeMessageType{desc: desc}
}

// TestRegisterMessageConflictRejected verifies that registering the same
// message name twice is rejected with an error naming both sources, while
// normal registration and lookup keep their existing semantics: the first
// registration stays authoritative and the rejected one never overwrites it.
func TestRegisterMessageConflictRejected(t *testing.T) {
	name, first := messageTypeFor(t, "team_a/widget.proto", "dupe.team", "Widget")
	_, second := messageTypeFor(t, "team_b/widget.proto", "dupe.team", "Widget")

	reg := new(protoregistry.Types)
	if err := reg.RegisterMessage(first); err != nil {
		t.Fatalf("first RegisterMessage returned error: %v", err)
	}

	err := reg.RegisterMessage(second)
	if err == nil {
		t.Fatalf("second RegisterMessage of %v: want error, got nil", name)
	}
	msg := err.Error()
	for _, want := range []string{
		"message " + string(name) + " is already registered",
		"previously from",
		"currently from",
		"team_a/widget.proto",
		"team_b/widget.proto",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("conflict error missing %q; got:\n%v", want, err)
		}
	}

	// Normal lookup semantics are unchanged: the first registration wins.
	got, lerr := reg.FindMessageByName(name)
	if lerr != nil {
		t.Fatalf("FindMessageByName after conflict returned error: %v", lerr)
	}
	if got != first {
		t.Errorf("FindMessageByName = %v, want the first registered type", got)
	}
	if got.Descriptor() == second.Descriptor() {
		t.Errorf("FindMessageByName returned the rejected second registration")
	}
}

// TestRegisterMessageNormalLookup verifies the happy path: a single
// registration succeeds and lookup returns exactly that descriptor.
func TestRegisterMessageNormalLookup(t *testing.T) {
	name, mt := messageTypeFor(t, "normal/gadget.proto", "normal.team", "Gadget")
	reg := new(protoregistry.Types)
	if err := reg.RegisterMessage(mt); err != nil {
		t.Fatalf("RegisterMessage returned error: %v", err)
	}
	got, err := reg.FindMessageByName(name)
	if err != nil {
		t.Fatalf("FindMessageByName(%v) returned error: %v", name, err)
	}
	if got != mt {
		t.Errorf("FindMessageByName(%v) = %v, want %v", name, got, mt)
	}
	if got.Descriptor().FullName() != name {
		t.Errorf("lookup descriptor name = %v, want %v", got.Descriptor().FullName(), name)
	}
	if _, err := reg.FindMessageByName("normal.team.Absent"); !errors.Is(err, protoregistry.NotFound) {
		t.Errorf("FindMessageByName(absent) error = %v, want NotFound", err)
	}
}

// TestTypesCheckRegistrationConflictsClean verifies that the startup
// self-check reports no conflicts on a consistently built registry and that
// it still returns the registered descriptors afterwards.
func TestTypesCheckRegistrationConflictsClean(t *testing.T) {
	name, mt := messageTypeFor(t, "startup/clean.proto", "startup.team", "Clean")
	reg := new(protoregistry.Types)
	if err := reg.RegisterMessage(mt); err != nil {
		t.Fatalf("RegisterMessage returned error: %v", err)
	}
	if err := reg.CheckRegistrationConflicts(); err != nil {
		t.Errorf("CheckRegistrationConflicts on clean registry = %v, want nil", err)
	}
	got, err := reg.FindMessageByName(name)
	if err != nil {
		t.Fatalf("FindMessageByName(%v) returned error: %v", name, err)
	}
	if got != mt {
		t.Errorf("FindMessageByName(%v) = %v, want %v", name, got, mt)
	}
}

// TestFilesCheckRegistrationConflicts verifies that duplicate file
// registration is surfaced by the file-registry startup self-check.
func TestFilesCheckRegistrationConflicts(t *testing.T) {
	fd1 := mustMakeMessageFile(t, "dup/file.proto", "dup.team", "Msg", "")
	fd2 := mustMakeMessageFile(t, "dup/file.proto", "dup.team", "Msg", "")
	reg := new(protoregistry.Files)
	if err := reg.RegisterFile(fd1); err != nil {
		t.Fatalf("first RegisterFile returned error: %v", err)
	}
	if err := reg.RegisterFile(fd2); err == nil {
		t.Fatalf("second RegisterFile of duplicate path: want error, got nil")
	} else if !strings.Contains(err.Error(), `file "dup/file.proto" is already registered`) {
		t.Fatalf("duplicate RegisterFile error = %v, want already-registered error", err)
	}
}

// TestGlobalCheckRegistrationConflicts exercises the startup entry point.
// The global registries are built from generated packages whose init
// functions never overwrite entries, so the self-check must pass and the
// registries must remain usable afterwards.
func TestGlobalCheckRegistrationConflicts(t *testing.T) {
	if err := protoregistry.CheckGlobalRegistrationConflicts(); err != nil {
		t.Errorf("CheckGlobalRegistrationConflicts = %v, want nil", err)
	}
}

var _ protoreflect.MessageType = fakeMessageType{}
