package proto_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"testing"

	"google.golang.org/protobuf/proto"

	testpb "google.golang.org/protobuf/internal/testprotos/test"
)

// makeDeterminismTestMessage builds a message with several populated map
// fields, which is where unstable wire output used to come from.
func makeDeterminismTestMessage() *testpb.TestAllTypes {
	m := &testpb.TestAllTypes{
		MapStringString:     map[string]string{},
		MapInt32Int32:       map[int32]int32{},
		MapStringBytes:      map[string][]byte{},
		MapStringNestedEnum: map[string]testpb.TestAllTypes_NestedEnum{},
	}
	for i := 0; i < 16; i++ {
		k := fmt.Sprintf("key-%02d", i)
		m.MapStringString[k] = fmt.Sprintf("val-%02d", i)
		m.MapInt32Int32[int32(i*7)] = int32(i)
		m.MapStringBytes[k] = []byte(k)
		m.MapStringNestedEnum[k] = testpb.TestAllTypes_NestedEnum(i % 3)
	}
	return m
}

// TestMarshalStableOutput serializes the same message ten times with the
// default options and requires the bytes to be identical every time.
// Caches keyed on the serialized bytes miss if even one byte differs.
func TestMarshalStableOutput(t *testing.T) {
	m := makeDeterminismTestMessage()
	first, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for i := 0; i < 9; i++ {
		got, err := proto.Marshal(m)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if !bytes.Equal(got, first) {
			t.Fatalf("marshal run %d differs:\ngot:  %x\nwant: %x", i+2, got, first)
		}
	}
}

// goldenDeterministicOutput is the output recorded before the stability fix
// with MarshalOptions{Deterministic: true} (see artifacts/determinism_before.json).
// The default marshal output must match it byte for byte.
const goldenDeterministicOutput = "c2030408001000c2030408071001c20304080e1002c2030408151003c20304081c1004c2030408231005c20304082a10" +
	"06c2030408311007c2030408381008c20304083f1009c203040846100ac20304084d100bc203040854100cc20304085b" +
	"100dc203040862100ec203040869100faa04100a066b65792d3030120676616c2d3030aa04100a066b65792d30311206" +
	"76616c2d3031aa04100a066b65792d3032120676616c2d3032aa04100a066b65792d3033120676616c2d3033aa04100a" +
	"066b65792d3034120676616c2d3034aa04100a066b65792d3035120676616c2d3035aa04100a066b65792d3036120676" +
	"616c2d3036aa04100a066b65792d3037120676616c2d3037aa04100a066b65792d3038120676616c2d3038aa04100a06" +
	"6b65792d3039120676616c2d3039aa04100a066b65792d3130120676616c2d3130aa04100a066b65792d313112067661" +
	"6c2d3131aa04100a066b65792d3132120676616c2d3132aa04100a066b65792d3133120676616c2d3133aa04100a066b" +
	"65792d3134120676616c2d3134aa04100a066b65792d3135120676616c2d3135b204100a066b65792d303012066b6579" +
	"2d3030b204100a066b65792d303112066b65792d3031b204100a066b65792d303212066b65792d3032b204100a066b65" +
	"792d303312066b65792d3033b204100a066b65792d303412066b65792d3034b204100a066b65792d303512066b65792d" +
	"3035b204100a066b65792d303612066b65792d3036b204100a066b65792d303712066b65792d3037b204100a066b6579" +
	"2d303812066b65792d3038b204100a066b65792d303912066b65792d3039b204100a066b65792d313012066b65792d31" +
	"30b204100a066b65792d313112066b65792d3131b204100a066b65792d313212066b65792d3132b204100a066b65792d" +
	"313312066b65792d3133b204100a066b65792d313412066b65792d3134b204100a066b65792d313512066b65792d3135" +
	"ca040a0a066b65792d30301000ca040a0a066b65792d30311001ca040a0a066b65792d30321002ca040a0a066b65792d" +
	"30331000ca040a0a066b65792d30341001ca040a0a066b65792d30351002ca040a0a066b65792d30361000ca040a0a06" +
	"6b65792d30371001ca040a0a066b65792d30381002ca040a0a066b65792d30391000ca040a0a066b65792d31301001ca" +
	"040a0a066b65792d31311002ca040a0a066b65792d31321000ca040a0a066b65792d31331001ca040a0a066b65792d31" +
	"341002ca040a0a066b65792d31351000"

func TestMarshalMatchesPreChangeOutput(t *testing.T) {
	m := makeDeterminismTestMessage()
	want, err := hex.DecodeString(goldenDeterministicOutput)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	got, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("default marshal output does not match pre-change deterministic output:\ngot:  %x\nwant: %x", got, want)
	}
	det, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(got, det) {
		t.Fatalf("default marshal output differs from deterministic output:\ngot:  %x\nwant: %x", got, det)
	}

	// Wire compatibility: the pre-change bytes must still decode to an
	// equal message.
	roundtrip := new(testpb.TestAllTypes)
	if err := proto.Unmarshal(want, roundtrip); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !proto.Equal(m, roundtrip) {
		t.Fatalf("pre-change output does not decode to an equal message")
	}
}

func TestMarshalStats(t *testing.T) {
	proto.ResetMarshalStats()
	m := makeDeterminismTestMessage()
	for i := 0; i < 3; i++ {
		if _, err := proto.Marshal(m); err != nil {
			t.Fatalf("Marshal: %v", err)
		}
	}
	if _, err := proto.Marshal(&testpb.TestAllTypes{}); err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	stats := proto.MarshalStats()
	if got := stats["goproto.proto.test.TestAllTypes"]; got != 4 {
		t.Fatalf("MarshalStats[TestAllTypes] = %d, want 4", got)
	}
	proto.ResetMarshalStats()
	if got := proto.MarshalStats(); len(got) != 0 {
		t.Fatalf("MarshalStats after reset = %v, want empty", got)
	}
}
