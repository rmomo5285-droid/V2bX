package http3

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/quic-go/qpack"
)

func TestSecurityTrailerDecodedSizeLimit(t *testing.T) {
	// The encoded block fits the wire limit; its decoded field section does not.
	var encoded bytes.Buffer
	encoder := qpack.NewEncoder(&encoded)
	for i := 0; i < 3; i++ {
		if err := encoder.WriteField(qpack.HeaderField{Name: "x-test", Value: "value"}); err != nil {
			t.Fatal(err)
		}
	}
	if encoded.Len() >= 64 {
		t.Fatal("test must exercise decoded size, not the encoded size limit")
	}
	_, err := decodeTrailers(&encoded, &headersFrame{Length: uint64(encoded.Len())}, 64, qpack.NewDecoder(), nil, 0)
	if !errors.Is(err, errHeaderTooLarge) {
		t.Fatalf("decoded trailer expansion was not rejected: %v", err)
	}
}

func TestSecurityTrailerBudgetStopsIncrementalDecode(t *testing.T) {
	calls := 0
	_, err := parseTrailers(func() (qpack.HeaderField, error) {
		calls++
		return qpack.HeaderField{Name: "x", Value: "y"}, nil
	}, 34, nil)
	if !errors.Is(err, errHeaderTooLarge) || calls != 2 {
		t.Fatalf("decoder did not stop at the size limit: calls=%d err=%v", calls, err)
	}
}

func TestSecurityTrailerFieldValidation(t *testing.T) {
	for _, field := range []qpack.HeaderField{
		{Name: "Upper", Value: "value"},
		{Name: "x-test", Value: "bad\r\nvalue"},
		{Name: "content-length", Value: "5"},
		{Name: "connection", Value: "close"},
		{Name: ":status", Value: "200"},
	} {
		t.Run(field.Name, func(t *testing.T) {
			sent := false
			_, err := parseTrailers(func() (qpack.HeaderField, error) {
				if sent {
					return qpack.HeaderField{}, io.EOF
				}
				sent = true
				return field, nil
			}, 1024, nil)
			if err == nil {
				t.Fatal("invalid trailer field was accepted")
			}
		})
	}
}

func TestSecurityValidTrailers(t *testing.T) {
	var encoded bytes.Buffer
	encoder := qpack.NewEncoder(&encoded)
	if err := encoder.WriteField(qpack.HeaderField{Name: "x-checksum", Value: "ok"}); err != nil {
		t.Fatal(err)
	}
	headers, err := decodeTrailers(&encoded, &headersFrame{Length: uint64(encoded.Len())}, 128, qpack.NewDecoder(), nil, 0)
	if err != nil || headers.Get("X-Checksum") != "ok" {
		t.Fatalf("valid trailer failed: headers=%v err=%v", headers, err)
	}
}
