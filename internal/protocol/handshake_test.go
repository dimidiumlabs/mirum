// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
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
	os.WriteFile(path, []byte("not pem"), 0o600)

	_, err := LoadPrivateKey(path)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadPrivateKey_WrongKeyType(t *testing.T) {
	// Write a PEM block with garbage DER
	data := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("garbage")})
	path := filepath.Join(t.TempDir(), "bad.key")
	os.WriteFile(path, data, 0o600)

	_, err := LoadPrivateKey(path)
	if err == nil {
		t.Fatal("expected error")
	}
}

func generateTestKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func TestHandshake_Success(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	server := NewServerHandshake()
	client := NewClientHandshake(priv)

	if got := client.PublicKey(); len(got) != ed25519.PublicKeySize {
		t.Fatalf("public key len = %d, want %d", len(got), ed25519.PublicKeySize)
	}

	serverNonce, err := server.Challenge(pub, nil)
	if err != nil {
		t.Fatal(err)
	}

	signature, err := client.Sign(serverNonce, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := server.Verify(signature, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestHandshake_ChannelBinding(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)
	ekm := make([]byte, 32)
	rand.Read(ekm)

	server := NewServerHandshake()
	client := NewClientHandshake(priv)

	serverNonce, err := server.Challenge(pub, ekm)
	if err != nil {
		t.Fatal(err)
	}

	signature, err := client.Sign(serverNonce, ekm)
	if err != nil {
		t.Fatal(err)
	}

	if err := server.Verify(signature, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestHandshake_ChannelBinding_MismatchedEKM(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	serverEKM := make([]byte, 32)
	workerEKM := make([]byte, 32)
	rand.Read(serverEKM)
	rand.Read(workerEKM)

	server := NewServerHandshake()
	client := NewClientHandshake(priv)

	serverNonce, _ := server.Challenge(pub, serverEKM)
	signature, _ := client.Sign(serverNonce, workerEKM)

	err := server.Verify(signature, time.Now())
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestHandshake_WrongKey(t *testing.T) {
	workerKey := generateTestKey(t)
	otherKey := generateTestKey(t)

	server := NewServerHandshake()
	client := NewClientHandshake(workerKey)

	serverNonce, _ := server.Challenge(otherKey.Public().(ed25519.PublicKey), nil)
	signature, _ := client.Sign(serverNonce, nil)

	err := server.Verify(signature, time.Now())
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestHandshake_InvalidPublicKeyLength(t *testing.T) {
	server := NewServerHandshake()

	_, err := server.Challenge([]byte("short"), nil)
	if !errors.Is(err, ErrInvalidPublicKey) {
		t.Fatalf("err = %v, want ErrInvalidPublicKey", err)
	}
}

func TestHandshake_TamperedSignature(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	server := NewServerHandshake()
	client := NewClientHandshake(priv)

	serverNonce, _ := server.Challenge(pub, nil)
	signature, _ := client.Sign(serverNonce, nil)

	signature[0] ^= 0xff

	err := server.Verify(signature, time.Now())
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestHandshake_ClockSkew(t *testing.T) {
	priv := generateTestKey(t)
	pub := priv.Public().(ed25519.PublicKey)

	server := NewServerHandshake()
	client := NewClientHandshake(priv)

	serverNonce, _ := server.Challenge(pub, nil)
	signature, _ := client.Sign(serverNonce, nil)

	err := server.Verify(signature, time.Now().Add(-2*time.Minute))
	if !errors.Is(err, ErrClockSkew) {
		t.Fatalf("err = %v, want ErrClockSkew", err)
	}
}

func TestSign_InvalidNonceLength(t *testing.T) {
	priv := generateTestKey(t)
	client := NewClientHandshake(priv)

	_, err := client.Sign([]byte("short"), nil)
	if !errors.Is(err, ErrInvalidNonce) {
		t.Fatalf("err = %v, want ErrInvalidNonce", err)
	}
}
