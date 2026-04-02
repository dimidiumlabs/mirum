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
	if err := os.WriteFile(privPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}), 0600); err != nil {
		t.Fatal(err)
	}

	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pubPath = filepath.Join(dir, "test.pub")
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0644); err != nil {
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

func TestLoadPublicKey(t *testing.T) {
	_, pubPath, wantPub := writeTestKeyPair(t)

	key, err := LoadPublicKey(pubPath)
	if err != nil {
		t.Fatal(err)
	}

	if !key.Equal(wantPub) {
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
	os.WriteFile(path, []byte("not pem"), 0600)

	_, err := LoadPrivateKey(path)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadPrivateKey_WrongKeyType(t *testing.T) {
	// Write a PEM block with garbage DER
	data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("garbage")})
	path := filepath.Join(t.TempDir(), "bad.key")
	os.WriteFile(path, data, 0600)

	_, err := LoadPrivateKey(path)
	if err == nil {
		t.Fatal("expected error")
	}
}
