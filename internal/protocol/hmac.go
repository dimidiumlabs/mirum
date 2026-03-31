// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
)

const NonceSize = 32

func GenerateNonce() ([]byte, error) {
	nonce := make([]byte, NonceSize)
	_, err := rand.Read(nonce)
	return nonce, err
}

// ComputeProof returns HMAC-SHA256(secret, first || second).
func ComputeProof(secret, first, second []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(first)
	mac.Write(second)
	return mac.Sum(nil)
}

func VerifyProof(secret, first, second, proof []byte) bool {
	expected := ComputeProof(secret, first, second)
	return hmac.Equal(expected, proof)
}
