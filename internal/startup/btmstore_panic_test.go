package startup

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
)

// A UTF-16 string whose declared length doubles past 2^64 wraps to zero and
// makes the plist parser panic; the decode must report an error instead.
func TestUnmarshalBTMRecoversDecoderPanic(t *testing.T) {
	var b []byte
	b = append(b, "bplist00"...)
	obj := len(b)
	b = append(b, 0x6F, 0x13) // UTF-16 string, extended length as an 8-byte int
	lenBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(lenBytes, 0x8000000000000000)
	b = append(b, lenBytes...)
	table := len(b)
	b = append(b, byte(obj))
	trailer := make([]byte, 32)
	trailer[6] = 1 // offset int size
	trailer[7] = 1 // object ref size
	binary.BigEndian.PutUint64(trailer[8:], 1)
	binary.BigEndian.PutUint64(trailer[16:], 0)
	binary.BigEndian.PutUint64(trailer[24:], uint64(table))
	b = append(b, trailer...)

	_, err := unmarshalBTM(context.Background(), b)
	if err == nil {
		t.Fatal("want an error for a hostile string length")
	}
	if strings.Contains(err.Error(), "abandoned") {
		t.Fatalf("decode should fail fast, got %v", err)
	}
}
