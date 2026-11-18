package main

import "google.golang.org/protobuf/proto"

func marshalStats() map[string]uint64 { return proto.MarshalStats() }
