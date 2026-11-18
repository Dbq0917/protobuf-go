// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package protoregistry provides data structures to register and lookup
// protobuf descriptor types.
//
// The [Files] registry contains file descriptors and provides the ability
// to iterate over the files or lookup a specific descriptor within the files.
// [Files] only contains protobuf descriptors and has no understanding of Go
// type information that may be associated with each descriptor.
//
// The [Types] registry contains descriptor types for which there is a known
// Go type associated with that descriptor. It provides the ability to iterate
// over the registered types or lookup a type by name.
package protoregistry

import (
	stderrors "errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"google.golang.org/protobuf/internal/encoding/messageset"
	"google.golang.org/protobuf/internal/errors"
	"google.golang.org/protobuf/internal/flags"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// conflictPolicy configures the policy for handling registration conflicts.
//
// It can be over-written at compile time with a linker-initialized variable:
//
//	go build -ldflags "-X google.golang.org/protobuf/reflect/protoregistry.conflictPolicy=warn"
//
// It can be over-written at program execution with an environment variable:
//
//	GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn ./main
//
// Neither of the above are covered by the compatibility promise and
// may be removed in a future release of this module.
var conflictPolicy = "panic" // "panic" | "warn" | "ignore"

// ignoreConflict reports whether to ignore a registration conflict
// given the descriptor being registered and the error.
// It is a variable so that the behavior is easily overridden in another file.
var ignoreConflict = func(d protoreflect.Descriptor, err error) bool {
	const env = "GOLANG_PROTOBUF_REGISTRATION_CONFLICT"
	const faq = "https://protobuf.dev/reference/go/faq#namespace-conflict"
	policy := conflictPolicy
	if v := os.Getenv(env); v != "" {
		policy = v
	}
	switch policy {
	case "panic":
		panic(fmt.Sprintf("%v\nSee %v\n", err, faq))
	case "warn":
		fmt.Fprintf(os.Stderr, "WARNING: %v\nSee %v\n\n", err, faq)
		return true
	case "ignore":
		return true
	default:
		panic("invalid " + env + " value: " + os.Getenv(env))
	}
}

var globalMutex sync.RWMutex

// GlobalFiles is a global registry of file descriptors.
var GlobalFiles *Files = new(Files)

// GlobalTypes is the registry used by default for type lookups
// unless a local registry is provided by the user.
var GlobalTypes *Types = new(Types)

// NotFound is a sentinel error value to indicate that the type was not found.
//
// Since registry lookup can happen in the critical performance path, resolvers
// must return this exact error value, not an error wrapping it.
var NotFound = errors.New("not found")

// registrationSource records where a descriptor or type was registered from,
// so that registration conflicts can identify both sources involved.
type registrationSource struct {
	goPackage string // Go package path performing the registration; may be empty
	filePath  string // proto file path; may be empty
}

func (s registrationSource) String() string {
	switch {
	case s.goPackage != "" && s.filePath != "":
		return s.goPackage + " (" + s.filePath + ")"
	case s.goPackage != "":
		return s.goPackage
	default:
		return s.filePath
	}
}

// sourceOf resolves the registration source of a descriptor or registered type.
func sourceOf(v any) registrationSource {
	switch d := v.(type) {
	case protoreflect.EnumType:
		return sourceOfDescriptor(d.Descriptor())
	case protoreflect.MessageType:
		return sourceOfDescriptor(d.Descriptor())
	case protoreflect.ExtensionType:
		return sourceOfDescriptor(d.TypeDescriptor())
	case protoreflect.Descriptor:
		return sourceOfDescriptor(d)
	}
	return registrationSource{}
}

// sourceOfDescriptor resolves the registration source of a descriptor.
func sourceOfDescriptor(d protoreflect.Descriptor) registrationSource {
	var src registrationSource
	v := any(d.ParentFile())
	if fd, ok := v.(protoreflect.FileDescriptor); ok {
		src.filePath = fd.Path()
	}
	if d, ok := v.(interface{ GoPackagePath() string }); ok {
		src.goPackage = d.GoPackagePath()
	}
	return src
}

// formatRegistrationConflict amends a registration conflict error with the
// two sources involved, when they are known and distinct.
func formatRegistrationConflict(err error, prev, curr any) error {
	prevSrc, currSrc := sourceOf(prev), sourceOf(curr)
	if prevSrc == (registrationSource{}) && currSrc == (registrationSource{}) {
		return err
	}
	// Identical sources carry no diagnostic value and would only add noise.
	if prevSrc.goPackage == currSrc.goPackage && prevSrc.filePath == currSrc.filePath {
		return err
	}
	return errors.New("%v\n\tpreviously from: %q\n\tcurrently from:  %q", err, prevSrc, currSrc)
}

// Files is a registry for looking up or iterating over files and the
// descriptors contained within them.
// The Find and Range methods are safe for concurrent use.
type Files struct {
	// The map of descsByName contains:
	//	EnumDescriptor
	//	EnumValueDescriptor
	//	MessageDescriptor
	//	ExtensionDescriptor
	//	ServiceDescriptor
	//	*packageDescriptor
	//
	// Note that files are stored as a slice, since a package may contain
	// multiple files. Only top-level declarations are registered.
	// Note that enum values are in the top-level since that are in the same
	// scope as the parent enum.
	descsByName map[protoreflect.FullName]any
	filesByPath map[string][]protoreflect.FileDescriptor
	numFiles    int
}

type packageDescriptor struct {
	files []protoreflect.FileDescriptor
}

// RegisterFile registers the provided file descriptor.
//
// If any descriptor within the file conflicts with the descriptor of any
// previously registered file (e.g., two enums with the same full name),
// then the file is not registered and an error is returned.
//
// It is permitted for multiple files to have the same file path.
func (r *Files) RegisterFile(file protoreflect.FileDescriptor) error {
	if r == GlobalFiles {
		globalMutex.Lock()
		defer globalMutex.Unlock()
	}
	if r.descsByName == nil {
		r.descsByName = map[protoreflect.FullName]any{
			"": &packageDescriptor{},
		}
		r.filesByPath = make(map[string][]protoreflect.FileDescriptor)
	}
	path := file.Path()
	if prev := r.filesByPath[path]; len(prev) > 0 {
		r.checkGenProtoConflict(path)
		err := errors.New("file %q is already registered", file.Path())
		err = formatRegistrationConflict(err, prev[0], file)
		if !(r == GlobalFiles && ignoreConflict(file, err)) {
			return err
		}
	}

	for name := file.Package(); name != ""; name = name.Parent() {
		switch prev := r.descsByName[name]; prev.(type) {
		case nil, *packageDescriptor:
		default:
			err := errors.New("file %q has a package name conflict over %v", file.Path(), name)
			err = formatRegistrationConflict(err, prev, file)
			if r == GlobalFiles && ignoreConflict(file, err) {
				err = nil
			}
			return err
		}
	}
	var err error
	var hasConflict bool
	rangeTopLevelDescriptors(file, func(d protoreflect.Descriptor) {
		if prev := r.descsByName[d.FullName()]; prev != nil {
			hasConflict = true
			err = errors.New("file %q has a name conflict over %v", file.Path(), d.FullName())
			err = formatRegistrationConflict(err, prev, d)
			if r == GlobalFiles && ignoreConflict(d, err) {
				err = nil
			}
		}
	})
	if hasConflict {
		return err
	}

	for name := file.Package(); name != ""; name = name.Parent() {
		if r.descsByName[name] == nil {
			r.descsByName[name] = &packageDescriptor{}
		}
	}
	p := r.descsByName[file.Package()].(*packageDescriptor)
	p.files = append(p.files, file)
	rangeTopLevelDescriptors(file, func(d protoreflect.Descriptor) {
		r.descsByName[d.FullName()] = d
	})
	r.filesByPath[path] = append(r.filesByPath[path], file)
	r.numFiles++
	return nil
}

// Several well-known types were hosted in the google.golang.org/genproto module
// but were later moved to this module. To avoid a weak dependency on the
// genproto module (and its relatively large set of transitive dependencies),
// we rely on a registration conflict to determine whether the genproto version
// is too old (i.e., does not contain aliases to the new type declarations).
func (r *Files) checkGenProtoConflict(path string) {
	if r != GlobalFiles {
		return
	}
	var prevPath string
	const prevModule = "google.golang.org/genproto"
	const prevVersion = "cb27e3aa (May 26th, 2020)"
	switch path {
	case "google/protobuf/field_mask.proto":
		prevPath = prevModule + "/protobuf/field_mask"
	case "google/protobuf/api.proto":
		prevPath = prevModule + "/protobuf/api"
	case "google/protobuf/type.proto":
		prevPath = prevModule + "/protobuf/ptype"
	case "google/protobuf/source_context.proto":
		prevPath = prevModule + "/protobuf/source_context"
	default:
		return
	}
	pkgName := strings.TrimSuffix(strings.TrimPrefix(path, "google/protobuf/"), ".proto")
	pkgName = strings.Replace(pkgName, "_", "", -1) + "pb" // e.g., "field_mask" => "fieldmaskpb"
	currPath := "google.golang.org/protobuf/types/known/" + pkgName
	panic(fmt.Sprintf(""+
		"duplicate registration of %q\n"+
		"\n"+
		"The generated definition for this file has moved:\n"+
		"\tfrom: %q\n"+
		"\tto:   %q\n"+
		"A dependency on the %q module must\n"+
		"be at version %v or higher.\n"+
		"\n"+
		"Upgrade the dependency by running:\n"+
		"\tgo get -u %v\n",
		path, prevPath, currPath, prevModule, prevVersion, prevPath))
}

// FindDescriptorByName looks up a descriptor by the full name.
//
// This returns (nil, [NotFound]) if not found.
func (r *Files) FindDescriptorByName(name protoreflect.FullName) (protoreflect.Descriptor, error) {
	if r == nil {
		return nil, NotFound
	}
	if r == GlobalFiles {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	prefix := name
	suffix := nameSuffix("")
	for prefix != "" {
		if d, ok := r.descsByName[prefix]; ok {
			switch d := d.(type) {
			case protoreflect.EnumDescriptor:
				if d.FullName() == name {
					return d, nil
				}
			case protoreflect.EnumValueDescriptor:
				if d.FullName() == name {
					return d, nil
				}
			case protoreflect.MessageDescriptor:
				if d.FullName() == name {
					return d, nil
				}
				if d := findDescriptorInMessage(d, suffix); d != nil && d.FullName() == name {
					return d, nil
				}
			case protoreflect.ExtensionDescriptor:
				if d.FullName() == name {
					return d, nil
				}
			case protoreflect.ServiceDescriptor:
				if d.FullName() == name {
					return d, nil
				}
				if d := d.Methods().ByName(suffix.Pop()); d != nil && d.FullName() == name {
					return d, nil
				}
			}
			return nil, NotFound
		}
		prefix = prefix.Parent()
		suffix = nameSuffix(name[len(prefix)+len("."):])
	}
	return nil, NotFound
}

func findDescriptorInMessage(md protoreflect.MessageDescriptor, suffix nameSuffix) protoreflect.Descriptor {
	name := suffix.Pop()
	if suffix == "" {
		if ed := md.Enums().ByName(name); ed != nil {
			return ed
		}
		for i := md.Enums().Len() - 1; i >= 0; i-- {
			if vd := md.Enums().Get(i).Values().ByName(name); vd != nil {
				return vd
			}
		}
		if xd := md.Extensions().ByName(name); xd != nil {
			return xd
		}
		if fd := md.Fields().ByName(name); fd != nil {
			return fd
		}
		if od := md.Oneofs().ByName(name); od != nil {
			return od
		}
	}
	if md := md.Messages().ByName(name); md != nil {
		if suffix == "" {
			return md
		}
		return findDescriptorInMessage(md, suffix)
	}
	return nil
}

type nameSuffix string

func (s *nameSuffix) Pop() (name protoreflect.Name) {
	if i := strings.IndexByte(string(*s), '.'); i >= 0 {
		name, *s = protoreflect.Name((*s)[:i]), (*s)[i+1:]
	} else {
		name, *s = protoreflect.Name((*s)), ""
	}
	return name
}

// FindFileByPath looks up a file by the path.
//
// This returns (nil, [NotFound]) if not found.
// This returns an error if multiple files have the same path.
func (r *Files) FindFileByPath(path string) (protoreflect.FileDescriptor, error) {
	if r == nil {
		return nil, NotFound
	}
	if r == GlobalFiles {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	fds := r.filesByPath[path]
	switch len(fds) {
	case 0:
		return nil, NotFound
	case 1:
		return fds[0], nil
	default:
		return nil, errors.New("multiple files named %q", path)
	}
}

// NumFiles reports the number of registered files,
// including duplicate files with the same name.
func (r *Files) NumFiles() int {
	if r == nil {
		return 0
	}
	if r == GlobalFiles {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	return r.numFiles
}

// RangeFiles iterates over all registered files while f returns true.
// If multiple files have the same name, RangeFiles iterates over all of them.
// The iteration order is undefined.
func (r *Files) RangeFiles(f func(protoreflect.FileDescriptor) bool) {
	if r == nil {
		return
	}
	if r == GlobalFiles {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	for _, files := range r.filesByPath {
		for _, file := range files {
			if !f(file) {
				return
			}
		}
	}
}

// NumFilesByPackage reports the number of registered files in a proto package.
func (r *Files) NumFilesByPackage(name protoreflect.FullName) int {
	if r == nil {
		return 0
	}
	if r == GlobalFiles {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	p, ok := r.descsByName[name].(*packageDescriptor)
	if !ok {
		return 0
	}
	return len(p.files)
}

// RangeFilesByPackage iterates over all registered files in a given proto package
// while f returns true. The iteration order is undefined.
func (r *Files) RangeFilesByPackage(name protoreflect.FullName, f func(protoreflect.FileDescriptor) bool) {
	if r == nil {
		return
	}
	if r == GlobalFiles {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	p, ok := r.descsByName[name].(*packageDescriptor)
	if !ok {
		return
	}
	for _, file := range p.files {
		if !f(file) {
			return
		}
	}
}

// rangeTopLevelDescriptors iterates over all top-level descriptors in a file
// which will be directly entered into the registry.
func rangeTopLevelDescriptors(fd protoreflect.FileDescriptor, f func(protoreflect.Descriptor)) {
	eds := fd.Enums()
	for i := eds.Len() - 1; i >= 0; i-- {
		f(eds.Get(i))
		vds := eds.Get(i).Values()
		for i := vds.Len() - 1; i >= 0; i-- {
			f(vds.Get(i))
		}
	}
	mds := fd.Messages()
	for i := mds.Len() - 1; i >= 0; i-- {
		f(mds.Get(i))
	}
	xds := fd.Extensions()
	for i := xds.Len() - 1; i >= 0; i-- {
		f(xds.Get(i))
	}
	sds := fd.Services()
	for i := sds.Len() - 1; i >= 0; i-- {
		f(sds.Get(i))
	}
}

// MessageTypeResolver is an interface for looking up messages.
//
// A compliant implementation must deterministically return the same type
// if no error is encountered.
//
// The [Types] type implements this interface.
type MessageTypeResolver interface {
	// FindMessageByName looks up a message by its full name.
	// E.g., "google.protobuf.Any"
	//
	// This return (nil, NotFound) if not found.
	FindMessageByName(message protoreflect.FullName) (protoreflect.MessageType, error)

	// FindMessageByURL looks up a message by a URL identifier.
	// See documentation on google.protobuf.Any.type_url for the URL format.
	//
	// This returns (nil, NotFound) if not found.
	FindMessageByURL(url string) (protoreflect.MessageType, error)
}

// ExtensionTypeResolver is an interface for looking up extensions.
//
// A compliant implementation must deterministically return the same type
// if no error is encountered.
//
// The [Types] type implements this interface.
type ExtensionTypeResolver interface {
	// FindExtensionByName looks up a extension field by the field's full name.
	// Note that this is the full name of the field as determined by
	// where the extension is declared and is unrelated to the full name of the
	// message being extended.
	//
	// This returns (nil, NotFound) if not found.
	FindExtensionByName(field protoreflect.FullName) (protoreflect.ExtensionType, error)

	// FindExtensionByNumber looks up a extension field by the field number
	// within some parent message, identified by full name.
	//
	// This returns (nil, NotFound) if not found.
	FindExtensionByNumber(message protoreflect.FullName, field protoreflect.FieldNumber) (protoreflect.ExtensionType, error)
}

var (
	_ MessageTypeResolver   = (*Types)(nil)
	_ ExtensionTypeResolver = (*Types)(nil)
)

// Types is a registry for looking up or iterating over descriptor types.
// The Find and Range methods are safe for concurrent use.
type Types struct {
	typesByName         typesByName
	extensionsByMessage extensionsByMessage

	numEnums      int
	numMessages   int
	numExtensions int
}

type (
	typesByName         map[protoreflect.FullName]*typeEntry
	extensionsByMessage map[protoreflect.FullName]extensionsByNumber
	extensionsByNumber  map[protoreflect.FieldNumber]*extensionEntry
)

// typeEntry pairs a registered type with the source of its first
// registration. The source is retained even if a warn/ignore conflict policy
// later overwrites typ, which is what makes the startup self-check possible.
type typeEntry struct {
	typ    any
	source registrationSource
}

// extensionEntry pairs a registered extension with the source of its first
// registration, retaining the original source even after an overwrite.
type extensionEntry struct {
	typ    protoreflect.ExtensionType
	source registrationSource
}

// RegisterMessage registers the provided message type.
//
// If a naming conflict occurs, the type is not registered and an error is returned.
func (r *Types) RegisterMessage(mt protoreflect.MessageType) error {
	// Under rare circumstances getting the descriptor might recursively
	// examine the registry, so fetch it before locking.
	md := mt.Descriptor()

	if r == GlobalTypes {
		globalMutex.Lock()
		defer globalMutex.Unlock()
	}

	if err := r.register("message", md, mt); err != nil {
		return err
	}
	r.numMessages++
	return nil
}

// RegisterEnum registers the provided enum type.
//
// If a naming conflict occurs, the type is not registered and an error is returned.
func (r *Types) RegisterEnum(et protoreflect.EnumType) error {
	// Under rare circumstances getting the descriptor might recursively
	// examine the registry, so fetch it before locking.
	ed := et.Descriptor()

	if r == GlobalTypes {
		globalMutex.Lock()
		defer globalMutex.Unlock()
	}

	if err := r.register("enum", ed, et); err != nil {
		return err
	}
	r.numEnums++
	return nil
}

// RegisterExtension registers the provided extension type.
//
// If a naming conflict occurs, the type is not registered and an error is returned.
func (r *Types) RegisterExtension(xt protoreflect.ExtensionType) error {
	// Under rare circumstances getting the descriptor might recursively
	// examine the registry, so fetch it before locking.
	//
	// A known case where this can happen: Fetching the TypeDescriptor for a
	// legacy ExtensionDesc can consult the global registry.
	xd := xt.TypeDescriptor()

	if r == GlobalTypes {
		globalMutex.Lock()
		defer globalMutex.Unlock()
	}

	field := xd.Number()
	message := xd.ContainingMessage().FullName()
	if prev := r.extensionsByMessage[message][field]; prev != nil {
		err := errors.New("extension number %d is already registered on message %v", field, message)
		err = formatRegistrationConflict(err, prev.typ, xt)
		if !(r == GlobalTypes && ignoreConflict(xd, err)) {
			return err
		}
	}

	if err := r.register("extension", xd, xt); err != nil {
		return err
	}
	if r.extensionsByMessage == nil {
		r.extensionsByMessage = make(extensionsByMessage)
	}
	if r.extensionsByMessage[message] == nil {
		r.extensionsByMessage[message] = make(extensionsByNumber)
	}
	if entry := r.extensionsByMessage[message][field]; entry != nil {
		entry.typ = xt // warn/ignore policy: overwrite but keep the original source
	} else {
		r.extensionsByMessage[message][field] = &extensionEntry{typ: xt, source: sourceOf(xt)}
	}
	r.numExtensions++
	return nil
}

func (r *Types) register(kind string, desc protoreflect.Descriptor, typ any) error {
	name := desc.FullName()
	if entry := r.typesByName[name]; entry != nil {
		err := errors.New("%v %v is already registered", kind, name)
		err = formatRegistrationConflict(err, entry.typ, typ)
		if !(r == GlobalTypes && ignoreConflict(desc, err)) {
			return err
		}
	}
	if r.typesByName == nil {
		r.typesByName = make(typesByName)
	}
	if entry := r.typesByName[name]; entry != nil {
		entry.typ = typ // warn/ignore policy: overwrite but keep the original source
	} else {
		r.typesByName[name] = &typeEntry{typ: typ, source: sourceOf(typ)}
	}
	return nil
}

// FindEnumByName looks up an enum by its full name.
// E.g., "google.protobuf.Field.Kind".
//
// This returns (nil, [NotFound]) if not found.
func (r *Types) FindEnumByName(enum protoreflect.FullName) (protoreflect.EnumType, error) {
	if r == nil {
		return nil, NotFound
	}
	if r == GlobalTypes {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	if entry := r.typesByName[enum]; entry != nil {
		v := entry.typ
		if et, _ := v.(protoreflect.EnumType); et != nil {
			return et, nil
		}
		return nil, errors.New("found wrong type: got %v, want enum", typeName(v))
	}
	return nil, NotFound
}

// FindMessageByName looks up a message by its full name,
// e.g. "google.protobuf.Any".
//
// This returns (nil, [NotFound]) if not found.
func (r *Types) FindMessageByName(message protoreflect.FullName) (protoreflect.MessageType, error) {
	if r == nil {
		return nil, NotFound
	}
	if r == GlobalTypes {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	if entry := r.typesByName[message]; entry != nil {
		v := entry.typ
		if mt, _ := v.(protoreflect.MessageType); mt != nil {
			return mt, nil
		}
		return nil, errors.New("found wrong type: got %v, want message", typeName(v))
	}
	return nil, NotFound
}

// FindMessageByURL looks up a message by a URL identifier.
// See documentation on google.protobuf.Any.type_url for the URL format.
//
// This returns (nil, [NotFound]) if not found.
func (r *Types) FindMessageByURL(url string) (protoreflect.MessageType, error) {
	// This function is similar to FindMessageByName but
	// truncates anything before and including '/' in the URL.
	if r == nil {
		return nil, NotFound
	}
	if r == GlobalTypes {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	message := protoreflect.FullName(url)
	if i := strings.LastIndexByte(url, '/'); i >= 0 {
		message = message[i+len("/"):]
	}

	if entry := r.typesByName[message]; entry != nil {
		v := entry.typ
		if mt, _ := v.(protoreflect.MessageType); mt != nil {
			return mt, nil
		}
		return nil, errors.New("found wrong type: got %v, want message", typeName(v))
	}
	return nil, NotFound
}

// FindExtensionByName looks up a extension field by the field's full name.
// Note that this is the full name of the field as determined by
// where the extension is declared and is unrelated to the full name of the
// message being extended.
//
// This returns (nil, [NotFound]) if not found.
func (r *Types) FindExtensionByName(field protoreflect.FullName) (protoreflect.ExtensionType, error) {
	if r == nil {
		return nil, NotFound
	}
	if r == GlobalTypes {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	if entry := r.typesByName[field]; entry != nil {
		v := entry.typ
		if xt, _ := v.(protoreflect.ExtensionType); xt != nil {
			return xt, nil
		}

		// MessageSet extensions are special in that the name of the extension
		// is the name of the message type used to extend the MessageSet.
		// This naming scheme is used by text and JSON serialization.
		//
		// This feature is protected by the ProtoLegacy flag since MessageSets
		// are a proto1 feature that is long deprecated.
		if flags.ProtoLegacy {
			if _, ok := v.(protoreflect.MessageType); ok {
				field := field.Append(messageset.ExtensionName)
				if entry := r.typesByName[field]; entry != nil {
					if xt, _ := entry.typ.(protoreflect.ExtensionType); xt != nil {
						if messageset.IsMessageSetExtension(xt.TypeDescriptor()) {
							return xt, nil
						}
					}
				}
			}
		}

		return nil, errors.New("found wrong type: got %v, want extension", typeName(v))
	}
	return nil, NotFound
}

// FindExtensionByNumber looks up a extension field by the field number
// within some parent message, identified by full name.
//
// This returns (nil, [NotFound]) if not found.
func (r *Types) FindExtensionByNumber(message protoreflect.FullName, field protoreflect.FieldNumber) (protoreflect.ExtensionType, error) {
	if r == nil {
		return nil, NotFound
	}
	if r == GlobalTypes {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	if entry, ok := r.extensionsByMessage[message][field]; ok {
		return entry.typ, nil
	}
	return nil, NotFound
}

// NumEnums reports the number of registered enums.
func (r *Types) NumEnums() int {
	if r == nil {
		return 0
	}
	if r == GlobalTypes {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	return r.numEnums
}

// RangeEnums iterates over all registered enums while f returns true.
// Iteration order is undefined.
func (r *Types) RangeEnums(f func(protoreflect.EnumType) bool) {
	if r == nil {
		return
	}
	if r == GlobalTypes {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	for _, entry := range r.typesByName {
		if et, ok := entry.typ.(protoreflect.EnumType); ok {
			if !f(et) {
				return
			}
		}
	}
}

// NumMessages reports the number of registered messages.
func (r *Types) NumMessages() int {
	if r == nil {
		return 0
	}
	if r == GlobalTypes {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	return r.numMessages
}

// RangeMessages iterates over all registered messages while f returns true.
// Iteration order is undefined.
func (r *Types) RangeMessages(f func(protoreflect.MessageType) bool) {
	if r == nil {
		return
	}
	if r == GlobalTypes {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	for _, entry := range r.typesByName {
		if mt, ok := entry.typ.(protoreflect.MessageType); ok {
			if !f(mt) {
				return
			}
		}
	}
}

// NumExtensions reports the number of registered extensions.
func (r *Types) NumExtensions() int {
	if r == nil {
		return 0
	}
	if r == GlobalTypes {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	return r.numExtensions
}

// RangeExtensions iterates over all registered extensions while f returns true.
// Iteration order is undefined.
func (r *Types) RangeExtensions(f func(protoreflect.ExtensionType) bool) {
	if r == nil {
		return
	}
	if r == GlobalTypes {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	for _, entry := range r.typesByName {
		if xt, ok := entry.typ.(protoreflect.ExtensionType); ok {
			if !f(xt) {
				return
			}
		}
	}
}

// NumExtensionsByMessage reports the number of registered extensions for
// a given message type.
func (r *Types) NumExtensionsByMessage(message protoreflect.FullName) int {
	if r == nil {
		return 0
	}
	if r == GlobalTypes {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	return len(r.extensionsByMessage[message])
}

// RangeExtensionsByMessage iterates over all registered extensions filtered
// by a given message type while f returns true. Iteration order is undefined.
func (r *Types) RangeExtensionsByMessage(message protoreflect.FullName, f func(protoreflect.ExtensionType) bool) {
	if r == nil {
		return
	}
	if r == GlobalTypes {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	for _, entry := range r.extensionsByMessage[message] {
		if !f(entry.typ) {
			return
		}
	}
}

// RegistrationConflict describes a single registration conflict detected by
// a registration self-check, identifying the two registration sources.
type RegistrationConflict struct {
	// Name is the fully-qualified name of the conflicting descriptor, or the
	// full name of the containing message for extension number conflicts.
	Name protoreflect.FullName
	// Kind identifies what is registered: "message", "enum", "extension",
	// or "file".
	Kind string
	// Field is the conflicting extension field number.
	// It is zero for all non-extension conflicts.
	Field protoreflect.FieldNumber
	// Previous is the source of the first registration.
	Previous registrationSource
	// Current is the source of the later registration that overwrote it.
	Current registrationSource
}

func (c RegistrationConflict) Error() string {
	if c.Kind == "extension" && c.Field != 0 {
		return fmt.Sprintf("proto: duplicate %s registration: %v field %d\n\tpreviously from: %q\n\tcurrently from:  %q",
			c.Kind, c.Name, c.Field, c.Previous, c.Current)
	}
	return fmt.Sprintf("proto: duplicate %s registration: %v\n\tpreviously from: %q\n\tcurrently from:  %q",
		c.Kind, c.Name, c.Previous, c.Current)
}

// CheckRegistrationConflicts performs a registration self-check over every
// type recorded in the registry and returns all detected conflicts, joined
// with errors.Join. It returns nil when no conflicts are present.
//
// Conflicts can only accumulate when the global conflict policy is set to
// "warn" or "ignore" (see GOLANG_PROTOBUF_REGISTRATION_CONFLICT), since the
// default "panic" policy rejects the second registration immediately.
// Callers can invoke this at startup and refuse to start when it reports an
// error, preventing a silently overwritten type from reaching production.
func (r *Types) CheckRegistrationConflicts() error {
	if r == nil {
		return nil
	}
	if r == GlobalTypes {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	return r.checkRegistrationConflictsLocked()
}

func (r *Types) checkRegistrationConflictsLocked() error {
	var conflicts []error
	for name, entry := range r.typesByName {
		if currSrc := sourceOf(entry.typ); currSrc != entry.source {
			conflicts = append(conflicts, RegistrationConflict{
				Name:     name,
				Kind:     typeName(entry.typ),
				Previous: entry.source,
				Current:  currSrc,
			})
		}
	}
	for message, byNumber := range r.extensionsByMessage {
		for field, entry := range byNumber {
			if currSrc := sourceOf(entry.typ); currSrc != entry.source {
				conflicts = append(conflicts, RegistrationConflict{
					Name:     message,
					Kind:     "extension",
					Field:    field,
					Previous: entry.source,
					Current:  currSrc,
				})
			}
		}
	}
	return stderrors.Join(conflicts...)
}

// CheckRegistrationConflicts performs a registration self-check over every
// file descriptor recorded in the registry and returns all detected
// conflicts, joined with errors.Join. It returns nil when no conflicts are
// present.
func (r *Files) CheckRegistrationConflicts() error {
	if r == nil {
		return nil
	}
	if r == GlobalFiles {
		globalMutex.RLock()
		defer globalMutex.RUnlock()
	}
	return r.checkRegistrationConflictsLocked()
}

func (r *Files) checkRegistrationConflictsLocked() error {
	var conflicts []error
	for path, files := range r.filesByPath {
		if len(files) < 2 {
			continue
		}
		prevSrc := sourceOf(files[0])
		for _, file := range files[1:] {
			currSrc := sourceOf(file)
			conflicts = append(conflicts, RegistrationConflict{
				Name:     protoreflect.FullName(path),
				Kind:     "file",
				Previous: prevSrc,
				Current:  currSrc,
			})
			prevSrc = currSrc
		}
	}
	return stderrors.Join(conflicts...)
}

// CheckGlobalRegistrationConflicts performs a startup registration self-check
// across the global file and type registries and returns every detected
// conflict, joined with errors.Join. A non-nil result means duplicate
// registrations have overwritten each other and the process should refuse to
// start; nil means the registries are consistent.
func CheckGlobalRegistrationConflicts() error {
	globalMutex.RLock()
	defer globalMutex.RUnlock()
	return stderrors.Join(
		GlobalFiles.checkRegistrationConflictsLocked(),
		GlobalTypes.checkRegistrationConflictsLocked(),
	)
}

func typeName(t any) string {
	switch t.(type) {
	case protoreflect.EnumType:
		return "enum"
	case protoreflect.MessageType:
		return "message"
	case protoreflect.ExtensionType:
		return "extension"
	default:
		return fmt.Sprintf("%T", t)
	}
}
