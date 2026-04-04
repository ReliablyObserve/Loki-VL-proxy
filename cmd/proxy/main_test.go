package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildServerTLSConfig_RequiresCAWhenClientCertsRequired(t *testing.T) {
	cfg, err := buildServerTLSConfig("", true)
	if err == nil {
		t.Fatal("expected error when client cert auth is required without a CA file")
	}
	if cfg != nil {
		t.Fatal("expected nil TLS config on error")
	}
}

func TestBuildServerTLSConfig_LoadsClientCAPool(t *testing.T) {
	caPath := writeTestCA(t)

	cfg, err := buildServerTLSConfig(caPath, true)
	if err != nil {
		t.Fatalf("expected TLS config, got error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected TLS config to be returned")
	}
	if cfg.ClientCAs == nil {
		t.Fatal("expected client CA pool to be configured")
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("expected RequireAndVerifyClientCert, got %v", cfg.ClientAuth)
	}
}

func writeTestCA(t *testing.T) string {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate test CA key: %v", err)
	}

	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "loki-vl-proxy-test-ca",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed to create test CA certificate: %v", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	path := filepath.Join(t.TempDir(), "client-ca.pem")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("failed to write CA PEM: %v", err)
	}
	return path
}
