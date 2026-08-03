package node

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/conf"
)

type testCertificateIssuer func() error

func (f testCertificateIssuer) CreateCert() error {
	return f()
}

func TestGenerateSelfSSLCertificate(t *testing.T) {
	dir := t.TempDir()
	if err := generateSelfSslCertificate(
		"domain.com",
		filepath.Join(dir, "fullchain.cer"),
		filepath.Join(dir, "cert.key"),
	); err != nil {
		t.Fatalf("generate certificate: %v", err)
	}
}

func TestCertificateMatchesDomain(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "fullchain.cer")
	keyPath := filepath.Join(dir, "cert.key")
	if err := generateSelfSslCertificate("node.example.com", certPath, keyPath); err != nil {
		t.Fatalf("generate certificate: %v", err)
	}
	matches, err := certificateMatchesDomain(certPath, keyPath, "node.example.com")
	if err != nil || !matches {
		t.Fatalf("matching domain = %v, err=%v", matches, err)
	}
	matches, err = certificateMatchesDomain(certPath, keyPath, "other.example.com")
	if err != nil {
		t.Fatalf("check non-matching domain: %v", err)
	}
	if matches {
		t.Fatal("certificate unexpectedly covered a different domain")
	}
}

func TestWriteCertificatePairRollsBackPrivateKey(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "fullchain.cer")
	keyPath := filepath.Join(dir, "cert.key")
	oldCert, oldKey := testCertificatePair(t, "old.example.com")
	newCert, newKey := testCertificatePair(t, "new.example.com")
	if err := os.WriteFile(certPath, oldCert, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, oldKey, 0600); err != nil {
		t.Fatal(err)
	}

	replacements := 0
	err := writeCertificatePairWithReplace(
		certPath,
		keyPath,
		newCert,
		newKey,
		func(oldPath, newPath string) error {
			replacements++
			if replacements == 2 {
				return errors.New("injected certificate replacement failure")
			}
			return os.Rename(oldPath, newPath)
		},
	)
	if err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	if replacements != 3 {
		t.Fatalf("replacement calls = %d, want 3 including rollback", replacements)
	}
	gotCert, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	gotKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotCert, oldCert) || !bytes.Equal(gotKey, oldKey) {
		t.Fatal("certificate pair was not fully rolled back")
	}
	if _, err := LoadCertificatePair(certPath, keyPath); err != nil {
		t.Fatalf("rolled-back pair is invalid: %v", err)
	}
}

func TestLoadCertificatePairDuringConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "fullchain.cer")
	keyPath := filepath.Join(dir, "cert.key")
	certA, keyA := testCertificatePair(t, "a.example.com")
	certB, keyB := testCertificatePair(t, "b.example.com")
	if err := writeCertificatePair(certPath, keyPath, certA, keyA); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	readErr := make(chan error, 1)
	var reads atomic.Int64
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := LoadCertificatePair(certPath, keyPath); err != nil {
				select {
				case readErr <- err:
				default:
				}
				return
			}
			reads.Add(1)
		}
	}()

	slowReplace := func(oldPath, newPath string) error {
		if err := os.Rename(oldPath, newPath); err != nil {
			return err
		}
		if filepath.Clean(newPath) == filepath.Clean(keyPath) {
			time.Sleep(time.Millisecond)
		}
		return nil
	}
	var writers sync.WaitGroup
	for writer := 0; writer < 4; writer++ {
		writers.Add(1)
		go func(writer int) {
			defer writers.Done()
			for iteration := 0; iteration < 10; iteration++ {
				certPEM, keyPEM := certA, keyA
				if (writer+iteration)%2 == 1 {
					certPEM, keyPEM = certB, keyB
				}
				if err := writeCertificatePairWithReplace(
					certPath, keyPath, certPEM, keyPEM, slowReplace,
				); err != nil {
					select {
					case readErr <- fmt.Errorf("writer: %w", err):
					default:
					}
					return
				}
			}
		}(writer)
	}
	writers.Wait()
	close(stop)
	reader.Wait()
	select {
	case err := <-readErr:
		t.Fatal(err)
	default:
	}
	if reads.Load() == 0 {
		t.Fatal("reader did not load a certificate")
	}
}

func TestRequestCertAutoModesReplaceUnusablePair(t *testing.T) {
	for _, mode := range []string{"dns", "http"} {
		for _, invalid := range []string{"corrupt", "wrong-domain"} {
			t.Run(mode+"/"+invalid, func(t *testing.T) {
				dir := t.TempDir()
				certPath := filepath.Join(dir, "fullchain.cer")
				keyPath := filepath.Join(dir, "cert.key")
				switch invalid {
				case "corrupt":
					if err := os.WriteFile(certPath, []byte("not a certificate"), 0644); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(keyPath, []byte("not a private key"), 0600); err != nil {
						t.Fatal(err)
					}
				case "wrong-domain":
					certPEM, keyPEM := testCertificatePair(t, "wrong.example.com")
					if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
						t.Fatal(err)
					}
				}

				const domain = "node.example.com"
				controller := testController(mode, domain, certPath, keyPath)
				calls := 0
				err := controller.requestCertWithIssuer(func() (certificateIssuer, error) {
					calls++
					return testCertificateIssuer(func() error {
						return generateSelfSslCertificate(domain, certPath, keyPath)
					}), nil
				})
				if err != nil {
					t.Fatalf("request certificate: %v", err)
				}
				if calls != 1 {
					t.Fatalf("issuer calls = %d, want 1", calls)
				}
				matches, err := certificateMatchesDomain(certPath, keyPath, domain)
				if err != nil || !matches {
					t.Fatalf("replacement matches = %v, err=%v", matches, err)
				}
			})
		}
	}
}

func TestRequestCertSelfReplacesCorruptPair(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "fullchain.cer")
	keyPath := filepath.Join(dir, "cert.key")
	if err := os.WriteFile(certPath, []byte("broken"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	controller := testController("self", "node.example.com", certPath, keyPath)
	if err := controller.requestCert(); err != nil {
		t.Fatalf("request self-signed certificate: %v", err)
	}
	if _, err := LoadCertificatePair(certPath, keyPath); err != nil {
		t.Fatalf("replacement pair is invalid: %v", err)
	}
}

func TestRequestCertFileModeDoesNotTouchInvalidPair(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "fullchain.cer")
	keyPath := filepath.Join(dir, "cert.key")
	wantCert := []byte("managed externally: certificate")
	wantKey := []byte("managed externally: private key")
	if err := os.WriteFile(certPath, wantCert, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, wantKey, 0600); err != nil {
		t.Fatal(err)
	}
	controller := testController("file", "node.example.com", certPath, keyPath)
	err := controller.requestCertWithIssuer(func() (certificateIssuer, error) {
		return nil, errors.New("file mode invoked issuer")
	})
	if err != nil {
		t.Fatal(err)
	}
	gotCert, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	gotKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotCert, wantCert) || !bytes.Equal(gotKey, wantKey) {
		t.Fatal("file mode changed externally managed certificate files")
	}
}

func testController(mode, domain, certPath, keyPath string) *Controller {
	return &Controller{Options: &conf.Options{CertConfig: &conf.CertConfig{
		CertMode:   mode,
		CertDomain: domain,
		CertFile:   certPath,
		KeyFile:    keyPath,
	}}}
}

func testCertificatePair(t *testing.T, domain string) ([]byte, []byte) {
	t.Helper()
	dir := t.TempDir()
	certPath := filepath.Join(dir, "fullchain.cer")
	keyPath := filepath.Join(dir, "cert.key")
	if err := generateSelfSslCertificate(domain, certPath, keyPath); err != nil {
		t.Fatalf("generate %s certificate: %v", domain, err)
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, keyPEM
}
