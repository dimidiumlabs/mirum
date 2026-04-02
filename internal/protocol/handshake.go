// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"slices"
	"time"
)

var (
	ErrInvalidPublicKey = errors.New("invalid public key size")
	ErrInvalidNonce     = errors.New("invalid nonce size")
	ErrInvalidSignature = errors.New("invalid signature")
	ErrClockSkew        = errors.New("clock skew too large")
	ErrServerRejected   = errors.New("server rejected handshake")
)

const NonceSize = 32

const EKMLabel = "mirum-handshake"
const EKMLength = 32

func generateNonce() ([]byte, error) {
	nonce := make([]byte, NonceSize)
	_, err := rand.Read(nonce)
	return nonce, err
}

// ServerHandshake holds state for the server side of the handshake protocol.
type ServerHandshake struct {
	publicKey   ed25519.PublicKey
	serverNonce []byte
	ekm         []byte // TLS Exported Keying Material, nil if not bound
}

func NewServerHandshake() *ServerHandshake {
	return &ServerHandshake{}
}

// Challenge validates the worker public key and returns a random nonce.
// If ekm is non-nil, channel binding is enabled and the EKM will be
// included in the signed data during verification.
func (h *ServerHandshake) Challenge(publicKey, ekm []byte) (serverNonce []byte, err error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return nil, ErrInvalidPublicKey
	}

	h.publicKey = publicKey
	h.ekm = ekm
	h.serverNonce, err = generateNonce()
	if err != nil {
		return nil, err
	}

	return h.serverNonce, nil
}

// Verify checks the worker's ed25519 signature and clock skew.
// The signed data is nonce (or nonce || ekm if channel binding is enabled).
func (h *ServerHandshake) Verify(signature []byte, workerTime time.Time) error {
	signed := slices.Concat(h.serverNonce, h.ekm)
	if !ed25519.Verify(h.publicKey, signed, signature) {
		return ErrInvalidSignature
	}

	skew := time.Since(workerTime).Abs()
	if skew > time.Minute {
		return ErrClockSkew
	}

	return nil
}

// ClientHandshake holds state for the client side of the handshake protocol.
type ClientHandshake struct {
	privateKey ed25519.PrivateKey
}

func NewClientHandshake(privateKey ed25519.PrivateKey) *ClientHandshake {
	return &ClientHandshake{privateKey: privateKey}
}

// PublicKey returns the 32-byte ed25519 public key.
func (h *ClientHandshake) PublicKey() []byte {
	return h.privateKey.Public().(ed25519.PublicKey)
}

// Sign signs the server nonce (and optional EKM) with the worker's private key.
// If ekm is non-nil, the signed data is nonce || ekm.
func (h *ClientHandshake) Sign(serverNonce, ekm []byte) ([]byte, error) {
	if len(serverNonce) != NonceSize {
		return nil, ErrInvalidNonce
	}
	return ed25519.Sign(h.privateKey, slices.Concat(serverNonce, ekm)), nil
}
