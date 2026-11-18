// Command determinismprobe serializes the same message repeatedly and
// reports how many distinct byte outputs are produced. It is a temporary
// measurement tool used to generate the before/after artifacts.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"google.golang.org/protobuf/internal/testprotos/test"
	"google.golang.org/protobuf/proto"
)

func buildMessage() *test.TestAllTypes {
	m := &test.TestAllTypes{
		MapStringString:     map[string]string{},
		MapInt32Int32:       map[int32]int32{},
		MapStringBytes:      map[string][]byte{},
		MapStringNestedEnum: map[string]test.TestAllTypes_NestedEnum{},
	}
	for i := 0; i < 16; i++ {
		k := fmt.Sprintf("key-%02d", i)
		m.MapStringString[k] = fmt.Sprintf("val-%02d", i)
		m.MapInt32Int32[int32(i*7)] = int32(i)
		m.MapStringBytes[k] = []byte(k)
		m.MapStringNestedEnum[k] = test.TestAllTypes_NestedEnum(i % 3)
	}
	return m
}

func main() {
	outPath := os.Args[1]
	const runs = 100
	m := buildMessage()
	seen := map[string]string{} // hash -> hex
	for i := 0; i < runs; i++ {
		b, err := proto.Marshal(m)
		if err != nil {
			panic(err)
		}
		h := sha256.Sum256(b)
		seen[hex.EncodeToString(h[:])] = hex.EncodeToString(b)
	}
	det, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		panic(err)
	}
	detSum := sha256.Sum256(det)
	detHash := hex.EncodeToString(detSum[:])
	_, detInSeen := seen[detHash]

	hashes := make([]string, 0, len(seen))
	for h := range seen {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)
	report := map[string]any{
		"runs":                  runs,
		"distinct_outputs":      len(seen),
		"distinct_hashes":       hashes,
		"deterministic_hash":    detHash,
		"deterministic_hex":     hex.EncodeToString(det),
		"deterministic_in_seen": detInSeen,
	}
	if stats := marshalStats(); stats != nil {
		report["per_type_marshal_counts"] = stats
	}
	f, err := os.Create(outPath)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		panic(err)
	}
	fmt.Printf("distinct=%d/%d deterministic_in_seen=%v\n", len(seen), runs, detInSeen)
}
