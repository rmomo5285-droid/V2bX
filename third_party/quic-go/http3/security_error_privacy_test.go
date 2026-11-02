package http3

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/quic-go/qpack"
)

func TestMalformedHeaderErrorsDoNotExposeValues(t *testing.T) {
	const secret = "private-auth-value"
	req, err := http.NewRequest(http.MethodGet, "https://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", secret+"\r\n")
	_, err = new(requestWriter).encodeHeaders(req, false, "", 0, false)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("request error exposes header value: %v", err)
	}
	field := qpack.HeaderField{Name: "authorization", Value: secret + "\r\n"}
	err = validateHeaderFieldNameAndValue(field)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("field error exposes header value: %v", err)
	}
	first := true
	_, err = parseHeaders(func() (qpack.HeaderField, error) {
		if first {
			first = false
			return field, nil
		}
		return qpack.HeaderField{}, io.EOF
	}, false, 4096, nil)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("response error exposes header value: %v", err)
	}
}
