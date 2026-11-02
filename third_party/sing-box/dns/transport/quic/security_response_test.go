package quic

import (
	"bytes"
	"math"
	"testing"
)

func TestHTTP3DNSResponseSizeBound(t *testing.T) {
	for _, length := range []int64{65536, math.MaxInt64} {
		if _, err := readHTTP3DNSResponse(bytes.NewReader(nil), length); err == nil {
			t.Fatalf("oversized length accepted: %d", length)
		}
	}
	for _, length := range []int64{-1, 0, 12} {
		body := bytes.NewReader(make([]byte, 128<<10))
		if _, err := readHTTP3DNSResponse(body, length); err == nil {
			t.Fatal("oversized body accepted")
		}
		if body.Len() != (128<<10)-65536 {
			t.Fatal("read beyond the bounded response budget")
		}
	}
	wire := []byte{0, 1, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	data, err := readHTTP3DNSResponse(bytes.NewReader(wire), int64(len(wire)))
	if err != nil || !bytes.Equal(data, wire) {
		t.Fatalf("valid wire message rejected: %v", err)
	}
}
