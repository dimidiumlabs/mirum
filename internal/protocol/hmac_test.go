// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"testing"
)

func TestGenerateNonce(t *testing.T) {
	nonce, err := GenerateNonce()
	if err != nil {
		t.Fatal(err)
	}
	if len(nonce) != NonceSize {
		t.Fatalf("len = %d, want %d", len(nonce), NonceSize)
	}

	nonce2, _ := GenerateNonce()
	if bytes.Equal(nonce, nonce2) {
		t.Fatal("two nonces are identical")
	}
}

func TestComputeProof(t *testing.T) {
	secret := []byte("secret")
	first := []byte("first")
	second := []byte("second")

	proof := ComputeProof(secret, first, second)

	// Golden value: HMAC-SHA256("secret", "first" || "second")
	mac := hmac.New(sha256.New, secret)
	mac.Write(first)
	mac.Write(second)
	want := mac.Sum(nil)

	if !bytes.Equal(proof, want) {
		t.Fatalf("proof mismatch")
	}

	// Different secret → different proof
	other := ComputeProof([]byte("other"), first, second)
	if bytes.Equal(proof, other) {
		t.Fatal("different secrets produced same proof")
	}

	// Order matters: (first, second) != (second, first)
	reversed := ComputeProof(secret, second, first)
	if bytes.Equal(proof, reversed) {
		t.Fatal("argument order did not affect proof")
	}
}

func TestVerifyProof(t *testing.T) {
	secret := []byte("secret")
	first := []byte("first")
	second := []byte("second")
	proof := ComputeProof(secret, first, second)

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
			got := VerifyProof(tt.secret, tt.first, tt.second, tt.proof)
			if got != tt.want {
				t.Fatalf("VerifyProof = %v, want %v", got, tt.want)
			}
		})
	}
}
