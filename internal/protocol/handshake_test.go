// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"
)

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
