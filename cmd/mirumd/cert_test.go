// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestPair(t *testing.T, certPath, keyPath string) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}

	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: serial,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}, &x509.Certificate{SerialNumber: serial}, pub, priv)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(certPath,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		0o644); err != nil {
		t.Fatal(err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath,
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCertReloader_Cached(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	writeTestPair(t, certPath, keyPath)

	r := newCertReloader(certPath, keyPath)

	first, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("expected same pointer on cache hit")
	}
}

func TestCertReloader_ReloadsOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	writeTestPair(t, certPath, keyPath)

	r := newCertReloader(certPath, keyPath)
	first, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}

	writeTestPair(t, certPath, keyPath)
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(certPath, future, future); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(keyPath, future, future); err != nil {
		t.Fatal(err)
	}

	second, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("expected new pointer after mtime change")
	}
}

func TestCertReloader_FallbackOnReloadError(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	writeTestPair(t, certPath, keyPath)

	r := newCertReloader(certPath, keyPath)
	good, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(certPath, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(certPath, future, future); err != nil {
		t.Fatal(err)
	}

	fallback, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatalf("expected last-good fallback, got error: %v", err)
	}
	if fallback != good {
		t.Fatal("expected cached cert on corrupted file")
	}
}

func TestCertReloader_ErrorOnFirstLoad(t *testing.T) {
	r := newCertReloader("/nonexistent/cert.pem", "/nonexistent/key.pem")
	if _, err := r.GetCertificate(nil); err == nil {
		t.Fatal("expected error on missing files")
	}
}
