// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"time"
)

var (
	ErrInvalidNonce   = errors.New("invalid nonce size")
	ErrInvalidProof   = errors.New("invalid proof")
	ErrClockSkew      = errors.New("clock skew too large")
	ErrServerRejected = errors.New("server rejected handshake")
)

const NonceSize = 32

func generateNonce() ([]byte, error) {
	nonce := make([]byte, NonceSize)
	_, err := rand.Read(nonce)
	return nonce, err
}

// computeProof returns HMAC-SHA256(secret, first || second).
func computeProof(secret, first, second []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(first)
	mac.Write(second)
	return mac.Sum(nil)
}

func verifyProof(secret, first, second, proof []byte) bool {
	expected := computeProof(secret, first, second)
	return hmac.Equal(expected, proof)
}

// ServerHandshake holds state for the server side of the handshake protocol.
type ServerHandshake struct {
	secret      []byte
	workerNonce []byte
	serverNonce []byte
}

func NewServerHandshake(secret []byte) *ServerHandshake {
	return &ServerHandshake{secret: secret}
}

// Challenge validates the worker nonce and returns the server nonce + proof.
func (h *ServerHandshake) Challenge(workerNonce []byte) (serverNonce, proof []byte, err error) {
	if len(workerNonce) != NonceSize {
		return nil, nil, ErrInvalidNonce
	}

	h.workerNonce = workerNonce
	h.serverNonce, err = generateNonce()
	if err != nil {
		return nil, nil, err
	}

	proof = computeProof(h.secret, workerNonce, h.serverNonce)
	return h.serverNonce, proof, nil
}

// Verify checks the worker's proof and clock skew.
func (h *ServerHandshake) Verify(proof []byte, workerTime time.Time) error {
	if !verifyProof(h.secret, h.serverNonce, h.workerNonce, proof) {
		return ErrInvalidProof
	}

	skew := time.Since(workerTime).Abs()
	if skew > time.Minute {
		return ErrClockSkew
	}

	return nil
}

// ClientHandshake holds state for the client side of the handshake protocol.
type ClientHandshake struct {
	secret      []byte
	workerNonce []byte
}

func NewClientHandshake(secret []byte) *ClientHandshake {
	return &ClientHandshake{secret: secret}
}

// Challenge generates the worker nonce.
func (h *ClientHandshake) Challenge() (workerNonce []byte, err error) {
	h.workerNonce, err = generateNonce()
	return h.workerNonce, err
}

// Verify checks the server proof and returns the worker proof.
func (h *ClientHandshake) Verify(serverNonce, serverProof []byte) (proof []byte, err error) {
	if !verifyProof(h.secret, h.workerNonce, serverNonce, serverProof) {
		return nil, ErrInvalidProof
	}
	proof = computeProof(h.secret, serverNonce, h.workerNonce)
	return proof, nil
}
