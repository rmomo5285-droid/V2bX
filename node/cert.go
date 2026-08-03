package node

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/InazumaV/V2bX/common/file"
	log "github.com/sirupsen/logrus"
)

var certificatePairMu sync.RWMutex

type certificateIssuer interface {
	CreateCert() error
}

func (c *Controller) renewCertTask() error {
	l, err := NewLego(c.CertConfig)
	if err != nil {
		log.WithField("tag", c.tag).Info("new lego error: ", err)
		return nil
	}
	err = l.RenewCert()
	if err != nil {
		log.WithField("tag", c.tag).Info("renew cert error: ", err)
		return nil
	}
	return nil
}

func (c *Controller) requestCert() error {
	return c.requestCertWithIssuer(func() (certificateIssuer, error) {
		return NewLego(c.CertConfig)
	})
}

func (c *Controller) requestCertWithIssuer(newIssuer func() (certificateIssuer, error)) error {
	switch c.CertConfig.CertMode {
	case "none", "":
	case "file":
		if c.CertConfig.CertFile == "" || c.CertConfig.KeyFile == "" {
			return fmt.Errorf("cert file path or key file path not exist")
		}
	case "dns", "http":
		if c.CertConfig.CertFile == "" || c.CertConfig.KeyFile == "" {
			return fmt.Errorf("cert file path or key file path not exist")
		}
		if file.IsExist(c.CertConfig.CertFile) && file.IsExist(c.CertConfig.KeyFile) {
			matches, err := certificateMatchesDomain(
				c.CertConfig.CertFile,
				c.CertConfig.KeyFile,
				c.CertConfig.CertDomain,
			)
			if err != nil {
				log.WithFields(log.Fields{
					"tag":    c.tag,
					"domain": c.CertConfig.CertDomain,
					"err":    err,
				}).Warn("Existing certificate is unusable; requesting a replacement")
			}
			if err == nil && matches {
				return nil
			}
			if err == nil {
				log.WithFields(log.Fields{
					"tag":    c.tag,
					"domain": c.CertConfig.CertDomain,
				}).Info("Certificate does not cover configured domain; requesting a replacement")
			}
		}
		l, err := newIssuer()
		if err != nil {
			return fmt.Errorf("create lego object error: %s", err)
		}
		err = l.CreateCert()
		if err != nil {
			return fmt.Errorf("create lego cert error: %s", err)
		}
	case "self":
		if c.CertConfig.CertFile == "" || c.CertConfig.KeyFile == "" {
			return fmt.Errorf("cert file path or key file path not exist")
		}
		if file.IsExist(c.CertConfig.CertFile) && file.IsExist(c.CertConfig.KeyFile) {
			matches, err := certificateMatchesDomain(
				c.CertConfig.CertFile,
				c.CertConfig.KeyFile,
				c.CertConfig.CertDomain,
			)
			if err != nil {
				log.WithFields(log.Fields{
					"tag":    c.tag,
					"domain": c.CertConfig.CertDomain,
					"err":    err,
				}).Warn("Existing self-signed certificate is unusable; generating a replacement")
			}
			if err == nil && matches {
				return nil
			}
		}
		err := generateSelfSslCertificate(
			c.CertConfig.CertDomain,
			c.CertConfig.CertFile,
			c.CertConfig.KeyFile)
		if err != nil {
			return fmt.Errorf("generate self cert error: %s", err)
		}
	default:
		return fmt.Errorf("unsupported certmode: %s", c.CertConfig.CertMode)
	}
	return nil
}

func certificateMatchesDomain(certPath, keyPath, domain string) (bool, error) {
	if domain == "" {
		return false, fmt.Errorf("certificate domain is empty")
	}
	pair, err := LoadCertificatePair(certPath, keyPath)
	if err != nil {
		return false, err
	}
	return certificatePairMatchesDomain(pair, domain)
}

func certificatePairMatchesDomain(pair tls.Certificate, domain string) (bool, error) {
	if domain == "" {
		return false, fmt.Errorf("certificate domain is empty")
	}
	if len(pair.Certificate) == 0 {
		return false, fmt.Errorf("certificate chain is empty")
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return false, err
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
		return false, nil
	}
	if err := cert.VerifyHostname(domain); err != nil {
		return false, nil
	}
	return true, nil
}

// LoadCertificatePair prevents readers from observing a certificate update
// between the private-key and certificate replacements.
func LoadCertificatePair(certPath, keyPath string) (tls.Certificate, error) {
	certificatePairMu.RLock()
	defer certificatePairMu.RUnlock()
	return tls.LoadX509KeyPair(certPath, keyPath)
}

func readCertificatePair(certPath, keyPath string) (tls.Certificate, []byte, []byte, error) {
	certificatePairMu.RLock()
	defer certificatePairMu.RUnlock()
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return tls.Certificate{}, nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return tls.Certificate{}, nil, nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, nil, err
	}
	return pair, certPEM, keyPEM, nil
}

func generateSelfSslCertificate(domain, certPath, keyPath string) error {
	if domain == "" {
		return fmt.Errorf("certificate domain is empty")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		Version:      3,
		SerialNumber: big.NewInt(time.Now().Unix()),
		Subject: pkix.Name{
			CommonName: domain,
		},
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(30, 0, 0),
	}
	if ip := net.ParseIP(domain); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{domain}
	}
	cert, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return err
	}
	var certPEM bytes.Buffer
	err = pem.Encode(&certPEM, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert,
	})
	if err != nil {
		return err
	}
	var keyPEM bytes.Buffer
	err = pem.Encode(&keyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err != nil {
		return err
	}
	return writeCertificatePair(certPath, keyPath, certPEM.Bytes(), keyPEM.Bytes())
}

func writeCertificatePair(certPath, keyPath string, certPEM, keyPEM []byte) error {
	return writeCertificatePairWithReplace(certPath, keyPath, certPEM, keyPEM, os.Rename)
}

type replaceFileFunc func(oldPath, newPath string) error

func writeCertificatePairWithReplace(
	certPath, keyPath string,
	certPEM, keyPEM []byte,
	replace replaceFileFunc,
) error {
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return fmt.Errorf("certificate and key do not match: %w", err)
	}
	certPath, err := filepath.Abs(certPath)
	if err != nil {
		return fmt.Errorf("resolve certificate path: %w", err)
	}
	keyPath, err = filepath.Abs(keyPath)
	if err != nil {
		return fmt.Errorf("resolve private-key path: %w", err)
	}
	if certPath == keyPath {
		return fmt.Errorf("certificate and private-key paths must be different")
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0755); err != nil {
		return fmt.Errorf("create certificate directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return fmt.Errorf("create private-key directory: %w", err)
	}
	stagedKey, err := stageFile(keyPath, keyPEM, 0600)
	if err != nil {
		return fmt.Errorf("stage private key: %w", err)
	}
	defer os.Remove(stagedKey)
	stagedCert, err := stageFile(certPath, certPEM, 0644)
	if err != nil {
		return fmt.Errorf("stage certificate: %w", err)
	}
	defer os.Remove(stagedCert)

	certificatePairMu.Lock()
	defer certificatePairMu.Unlock()

	oldKey, err := stageExistingFile(keyPath)
	if err != nil {
		return fmt.Errorf("back up private key: %w", err)
	}
	if oldKey != "" {
		defer os.Remove(oldKey)
	}

	if err := replace(stagedKey, keyPath); err != nil {
		return fmt.Errorf("replace private key: %w", err)
	}
	if err := replace(stagedCert, certPath); err != nil {
		rollbackErr := restoreFile(keyPath, oldKey, replace)
		if rollbackErr != nil {
			return fmt.Errorf("replace certificate: %v; rollback private key: %w", err, rollbackErr)
		}
		return fmt.Errorf("replace certificate: %w (private key rolled back)", err)
	}
	return nil
}

func stageExistingFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return stageFile(path, data, info.Mode().Perm())
}

func restoreFile(path, stagedBackup string, replace replaceFileFunc) error {
	if stagedBackup == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return replace(stagedBackup, path)
}

func stageFile(path string, data []byte, mode os.FileMode) (string, error) {
	temp, err := os.CreateTemp(filepath.Dir(path), ".v2bx-cert-*")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		os.Remove(tempPath)
		return "", err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		os.Remove(tempPath)
		return "", err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		os.Remove(tempPath)
		return "", err
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempPath)
		return "", err
	}
	return tempPath, nil
}
