package node

import (
	"os"
	"testing"

	"github.com/InazumaV/V2bX/conf"
)

func integrationLego(t *testing.T) *Lego {
	t.Helper()
	if os.Getenv("V2BX_ACME_INTEGRATION") != "1" {
		t.Skip("set V2BX_ACME_INTEGRATION=1 to run live ACME tests")
	}
	l, err := NewLego(&conf.CertConfig{
		CertMode:   "dns",
		Email:      "test@test.com",
		CertDomain: "test.test.com",
		Provider:   "cloudflare",
		DNSEnv: map[string]string{
			"CF_DNS_API_TOKEN": "123",
		},
		CertFile: "./cert/1.pem",
		KeyFile:  "./cert/1.key",
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLego_CreateCertByDns(t *testing.T) {
	if err := integrationLego(t).CreateCert(); err != nil {
		t.Fatal(err)
	}
}

func TestLego_RenewCert(t *testing.T) {
	if err := integrationLego(t).RenewCert(); err != nil {
		t.Fatal(err)
	}
}
