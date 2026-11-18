package proto

import (
	"sync"
	"sync/atomic"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// marshalStats counts top-level marshal operations keyed by the full name
// of the message type. It exists to quantify how often each message type is
// serialized, e.g. to assess the blast radius of wire output changes on
// caches that are keyed by serialized bytes.
var marshalStats sync.Map // map[protoreflect.FullName]*atomic.Uint64

// recordMarshal increments the marshal counter for the given message type.
// It is called once per top-level marshal operation; nested submessages are
// not counted separately.
func recordMarshal(m protoreflect.Message) {
	name := m.Descriptor().FullName()
	v, ok := marshalStats.Load(name)
	if !ok {
		v, _ = marshalStats.LoadOrStore(name, new(atomic.Uint64))
	}
	v.(*atomic.Uint64).Add(1)
}

// MarshalStats returns a snapshot of the number of top-level marshal
// operations performed per message type, keyed by the full message name
// (e.g. "google.protobuf.Duration").
//
// The counters are process-global and cumulative since process start or
// since the last call to ResetMarshalStats.
func MarshalStats() map[string]uint64 {
	stats := make(map[string]uint64)
	marshalStats.Range(func(k, v any) bool {
		stats[string(k.(protoreflect.FullName))] = v.(*atomic.Uint64).Load()
		return true
	})
	return stats
}

// ResetMarshalStats clears all per-message-type marshal counters.
func ResetMarshalStats() {
	marshalStats.Range(func(k, v any) bool {
		marshalStats.Delete(k)
		return true
	})
}
