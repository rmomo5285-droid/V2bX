package panel

import (
	"strings"
	"testing"
)

func TestRedactSensitiveText(t *testing.T) {
	secret := "secret/value+123"
	input := "Get https://panel.example/api?node_id=1&token=secret%2Fvalue%2B123: token secret/value+123"
	redacted := redactSensitiveText(input, secret)
	if strings.Contains(redacted, secret) || strings.Contains(redacted, "secret%2Fvalue%2B123") {
		t.Fatalf("secret remained in redacted text: %s", redacted)
	}
	if !strings.Contains(redacted, "token=[REDACTED]") {
		t.Fatalf("query token was not visibly redacted: %s", redacted)
	}
}
