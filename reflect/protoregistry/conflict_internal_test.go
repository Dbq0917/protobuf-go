// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package protoregistry

import (
	stderrors "errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// fakeDesc is a minimal descriptor for exercising provenance tracking
// without depending on a generated proto package.
type fakeDesc struct {
	protoreflect.Descriptor
	name protoreflect.FullName
	file fakeFile
}

func (d fakeDesc) FullName() protoreflect.FullName         { return d.name }
func (d fakeDesc) ParentFile() protoreflect.FileDescriptor { return d.file }

type fakeFile struct {
	protoreflect.FileDescriptor
	path string
	pkg  string
}

func (f fakeFile) Path() string                            { return f.path }
func (f fakeFile) GoPackagePath() string                   { return f.pkg }
func (f fakeFile) ParentFile() protoreflect.FileDescriptor { return f }

func desc(goPkg, filePath, fullName string) fakeDesc {
	return fakeDesc{
		name: protoreflect.FullName(fullName),
		file: fakeFile{path: filePath, pkg: goPkg},
	}
}

// TestCheckRegistrationConflictsDetectsOverwrite reproduces the production
// failure mode: two teams register the same message name, and a warn/ignore
// policy lets the second registration overwrite the first. The startup
// self-check must detect the overwrite and identify both sources.
func TestCheckRegistrationConflictsDetectsOverwrite(t *testing.T) {
	const name protoreflect.FullName = "shop.order.Order"
	first := desc("example.com/team/orderpb", "team/order.proto", string(name))
	second := desc("example.com/platform/orderpb", "platform/order.proto", string(name))

	reg := new(Types)
	if err := reg.register("message", first, first); err != nil {
		t.Fatalf("first register: %v", err)
	}

	// Simulate the warn/ignore conflict policy overwriting the entry while
	// preserving the original provenance, mirroring the global registry path.
	reg.typesByName[name].typ = second
	if reg.typesByName[name].source != sourceOf(first) {
		t.Fatalf("overwrite must retain the first registration source")
	}

	err := reg.CheckRegistrationConflicts()
	if err == nil {
		t.Fatalf("CheckRegistrationConflicts after overwrite: want conflict, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		string(name),
		"example.com/team/orderpb",
		"example.com/platform/orderpb",
		"team/order.proto",
		"platform/order.proto",
		"previously from",
		"currently from",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("conflict report missing %q; got:\n%v", want, err)
		}
	}

	var conflict RegistrationConflict
	if !stderrors.As(err, &conflict) {
		t.Fatalf("conflict error does not contain RegistrationConflict: %v", err)
	}
	if conflict.Name != name {
		t.Errorf("conflict.Name = %v, want %v", conflict.Name, name)
	}
}

// TestCheckRegistrationConflictsCleanRegistry verifies that an ordinary
// registry with no overwritten entries passes the startup self-check.
func TestCheckRegistrationConflictsCleanRegistry(t *testing.T) {
	reg := new(Types)
	d := desc("example.com/team/pb", "team/thing.proto", "team.Thing")
	if err := reg.register("message", d, d); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.CheckRegistrationConflicts(); err != nil {
		t.Errorf("CheckRegistrationConflicts = %v, want nil", err)
	}
}

// TestRegisterEnumConflictRejected verifies that duplicate enum registration
// is rejected and names both sources, matching the message behavior.
func TestRegisterEnumConflictRejected(t *testing.T) {
	const name protoreflect.FullName = "shop.order.Status"
	first := desc("example.com/team/statuspb", "team/status.proto", string(name))
	second := desc("example.com/platform/statuspb", "platform/status.proto", string(name))

	reg := new(Types)
	if err := reg.register("enum", first, first); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := reg.register("enum", second, second)
	if err == nil {
		t.Fatalf("duplicate enum register: want error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"enum " + string(name) + " is already registered",
		"example.com/team/statuspb",
		"example.com/platform/statuspb",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("enum conflict error missing %q; got:\n%v", want, err)
		}
	}
}

// TestCheckFileRegistrationConflictsDetectsOverwrite verifies that the file
// registry self-check flags a duplicate file path retained under the
// warn/ignore policy and names both sources.
func TestCheckFileRegistrationConflictsDetectsOverwrite(t *testing.T) {
	reg := new(Files)
	first := fakeFile{path: "order/order.proto", pkg: "example.com/team/orderpb"}
	second := fakeFile{path: "order/order.proto", pkg: "example.com/platform/orderpb"}
	reg.filesByPath = map[string][]protoreflect.FileDescriptor{
		"order/order.proto": {first, second},
	}

	err := reg.CheckRegistrationConflicts()
	if err == nil {
		t.Fatalf("Files.CheckRegistrationConflicts after duplicate: want conflict, got nil")
	}
	msg := err.Error()
	for _, want := range []string{
		"order/order.proto",
		"example.com/team/orderpb",
		"example.com/platform/orderpb",
		"previously from",
		"currently from",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("file conflict report missing %q; got:\n%v", want, err)
		}
	}

	var conflict RegistrationConflict
	if !stderrors.As(err, &conflict) {
		t.Fatalf("conflict error does not contain RegistrationConflict: %v", err)
	}
	if conflict.Kind != "file" {
		t.Errorf("conflict.Kind = %q, want file", conflict.Kind)
	}
}
