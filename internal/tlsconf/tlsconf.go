// Package tlsconf produces the certificate material needed to serve Together
// over HTTPS without an external terminator. The self-signed option makes an
// isolated installation encrypted from the first start; a real certificate is
// configured with tls_cert and tls_key and is preferred in production.
package tlsconf

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	// CertFile and KeyFile are the generated file names inside the data dir.
	CertFile = "tls-cert.pem"
	KeyFile  = "tls-key.pem"
	certDays = 730
)

// EnsureSelfSigned reuses a valid generated certificate or creates a new one.
// Browsers show a warning for a self-signed certificate, but the traffic is
// encrypted, which is the point: passwords and cookies no longer travel in
// clear text.
func EnsureSelfSigned(dir string) (certPath, keyPath string, err error) {
	certPath = filepath.Join(dir, CertFile)
	keyPath = filepath.Join(dir, KeyFile)
	if validPair(certPath, keyPath) {
		return certPath, keyPath, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("create certificate directory: %w", err)
	}
	certPEM, keyPEM, err := generate()
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return "", "", fmt.Errorf("write certificate: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return "", "", fmt.Errorf("write certificate key: %w", err)
	}
	return certPath, keyPath, nil
}

// validPair reports whether both files exist, match each other and are still
// valid for a while.
func validPair(certPath, keyPath string) bool {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil || len(pair.Certificate) == 0 {
		return false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return false
	}
	// Regenerate a month before expiry so a long-running installation rotates
	// its certificate without an outage.
	return time.Now().Add(30 * 24 * time.Hour).Before(leaf.NotAfter)
}

func generate() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate certificate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "together"
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host, Organization: []string{"Together"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(0, 0, certDays),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host, "localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("encode certificate key: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}
