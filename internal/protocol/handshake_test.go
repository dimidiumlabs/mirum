// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestKeyPair(t *testing.T) (privPath, pubPath string, pub ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()

	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	privPath = filepath.Join(dir, "test.key")
	if err := os.WriteFile(privPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}), 0o600); err != nil {
		t.Fatal(err)
	}

	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pubPath = filepath.Join(dir, "test.pub")
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o644); err != nil {
		t.Fatal(err)
	}

	return privPath, pubPath, pub
}

func TestLoadPrivateKey(t *testing.T) {
	privPath, _, wantPub := writeTestKeyPair(t)

	key, err := LoadPrivateKey(privPath)
	if err != nil {
		t.Fatal(err)
	}

	gotPub := key.Public().(ed25519.PublicKey)
	if !gotPub.Equal(wantPub) {
		t.Fatal("public key mismatch")
	}
}

func TestLoadPrivateKey_NotFound(t *testing.T) {
	_, err := LoadPrivateKey("/nonexistent/path")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadPrivateKey_NotPEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.key")
	if err := os.WriteFile(path, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadPrivateKey(path)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadPrivateKey_WrongKeyType(t *testing.T) {
	data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("garbage")})
	path := filepath.Join(t.TempDir(), "bad.key")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadPrivateKey(path)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSelfSignedCert(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	meta := &WorkerMeta{
		Name:    "test-worker",
		Version: "1.2.3",
		Os:      "linux",
		Arch:    "amd64",
		Runtime: "host",
	}

	cert, err := SelfSignedCert(priv, meta)
	if err != nil {
		t.Fatal(err)
	}

	if len(cert.Certificate) != 1 {
		t.Fatalf("expected 1 cert, got %d", len(cert.Certificate))
	}

	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}

	// Public key matches
	pubKey, ok := parsed.PublicKey.(ed25519.PublicKey)
	if !ok {
		t.Fatal("certificate does not contain an ed25519 public key")
	}
	if !pubKey.Equal(priv.Public().(ed25519.PublicKey)) {
		t.Fatal("public key mismatch")
	}

	// Key usage
	if parsed.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Fatal("missing DigitalSignature key usage")
	}
	if len(parsed.ExtKeyUsage) != 1 || parsed.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatal("missing ClientAuth extended key usage")
	}

	// NotBefore is recent (used for clock skew)
	if time.Since(parsed.NotBefore).Abs() > 5*time.Second {
		t.Fatalf("NotBefore too far from now: %v", parsed.NotBefore)
	}
}

func TestSelfSignedCert_WorkerMeta(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	want := &WorkerMeta{
		Name:    "my-worker",
		Version: "0.5.1",
		Os:      "darwin",
		Arch:    "arm64",
		Runtime: "docker",
	}

	cert, err := SelfSignedCert(priv, want)
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}

	got := ParseWorkerMeta(parsed)
	if got == nil {
		t.Fatal("ParseWorkerMeta returned nil")
	}

	if *got != *want {
		t.Fatalf("meta mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestParseWorkerMeta_NoCert(t *testing.T) {
	cert := &x509.Certificate{}
	if meta := ParseWorkerMeta(cert); meta != nil {
		t.Fatalf("expected nil, got %+v", meta)
	}
}
