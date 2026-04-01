// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

func TestGenerateNonce(t *testing.T) {
	nonce, err := generateNonce()
	if err != nil {
		t.Fatal(err)
	}
	if len(nonce) != NonceSize {
		t.Fatalf("len = %d, want %d", len(nonce), NonceSize)
	}

	nonce2, _ := generateNonce()
	if bytes.Equal(nonce, nonce2) {
		t.Fatal("two nonces are identical")
	}
}

func TestComputeProof(t *testing.T) {
	secret := []byte("secret")
	first := []byte("first")
	second := []byte("second")

	proof := computeProof(secret, first, second)

	// Golden value: HMAC-SHA256("secret", "first" || "second")
	mac := hmac.New(sha256.New, secret)
	mac.Write(first)
	mac.Write(second)
	want := mac.Sum(nil)

	if !bytes.Equal(proof, want) {
		t.Fatalf("proof mismatch")
	}

	// Different secret → different proof
	other := computeProof([]byte("other"), first, second)
	if bytes.Equal(proof, other) {
		t.Fatal("different secrets produced same proof")
	}

	// Order matters: (first, second) != (second, first)
	reversed := computeProof(secret, second, first)
	if bytes.Equal(proof, reversed) {
		t.Fatal("argument order did not affect proof")
	}
}

func TestVerifyProof(t *testing.T) {
	secret := []byte("secret")
	first := []byte("first")
	second := []byte("second")
	proof := computeProof(secret, first, second)

	tests := []struct {
		name   string
		secret []byte
		first  []byte
		second []byte
		proof  []byte
		want   bool
	}{
		{"valid", secret, first, second, proof, true},
		{"wrong proof", secret, first, second, []byte("wrong"), false},
		{"wrong secret", []byte("wrong"), first, second, proof, false},
		{"swapped args", secret, second, first, proof, false},
		{"empty proof", secret, first, second, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := verifyProof(tt.secret, tt.first, tt.second, tt.proof)
			if got != tt.want {
				t.Fatalf("VerifyProof = %v, want %v", got, tt.want)
			}
		})
	}
}

// Full handshake: client and server with matching secrets.
func TestHandshake_Success(t *testing.T) {
	secret := []byte("shared-secret")
	server := NewServerHandshake(secret)
	client := NewClientHandshake(secret)

	// Step 1: client generates nonce
	workerNonce, err := client.Challenge()
	if err != nil {
		t.Fatal(err)
	}

	// Step 2: server responds with its nonce + proof
	serverNonce, serverProof, err := server.Challenge(workerNonce)
	if err != nil {
		t.Fatal(err)
	}

	// Step 3: client verifies server, produces worker proof
	workerProof, err := client.Verify(serverNonce, serverProof)
	if err != nil {
		t.Fatal(err)
	}

	// Step 4: server verifies worker
	if err := server.Verify(workerProof, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestHandshake_WrongSecret(t *testing.T) {
	server := NewServerHandshake([]byte("server-secret"))
	client := NewClientHandshake([]byte("wrong-secret"))

	workerNonce, _ := client.Challenge()
	_, serverProof, _ := server.Challenge(workerNonce)

	// Client cannot verify server proof
	_, err := client.Verify(server.serverNonce, serverProof)
	if !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("err = %v, want ErrInvalidProof", err)
	}
}

func TestHandshake_BadNonce(t *testing.T) {
	server := NewServerHandshake([]byte("secret"))

	_, _, err := server.Challenge([]byte("short"))
	if !errors.Is(err, ErrInvalidNonce) {
		t.Fatalf("err = %v, want ErrInvalidNonce", err)
	}
}

func TestHandshake_ClockSkew(t *testing.T) {
	secret := []byte("secret")
	server := NewServerHandshake(secret)
	client := NewClientHandshake(secret)

	workerNonce, _ := client.Challenge()
	serverNonce, serverProof, _ := server.Challenge(workerNonce)
	workerProof, _ := client.Verify(serverNonce, serverProof)

	err := server.Verify(workerProof, time.Now().Add(-2*time.Minute))
	if !errors.Is(err, ErrClockSkew) {
		t.Fatalf("err = %v, want ErrClockSkew", err)
	}
}

func TestHandshake_TamperedProof(t *testing.T) {
	secret := []byte("secret")
	server := NewServerHandshake(secret)
	client := NewClientHandshake(secret)

	workerNonce, _ := client.Challenge()
	serverNonce, serverProof, _ := server.Challenge(workerNonce)
	workerProof, _ := client.Verify(serverNonce, serverProof)

	// Flip a byte
	workerProof[0] ^= 0xff

	err := server.Verify(workerProof, time.Now())
	if !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("err = %v, want ErrInvalidProof", err)
	}
}
