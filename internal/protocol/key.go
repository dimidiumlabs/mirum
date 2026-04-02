// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

var (
	errKeyNotPEM     = errors.New("key file does not contain a PEM block")
	errKeyNotPKCS8   = errors.New("key file does not contain a PKCS8 private key")
	errKeyNotPKIX    = errors.New("key file does not contain a PKIX public key")
	errKeyNotEd25519 = errors.New("key file does not contain an ed25519 key")
)

// LoadPrivateKey reads a PEM-encoded PKCS8 ed25519 private key from path.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errKeyNotPEM
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errKeyNotPKCS8, err)
	}

	edKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errKeyNotEd25519
	}

	return edKey, nil
}

// LoadPublicKey reads a PEM-encoded PKIX ed25519 public key from path.
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errKeyNotPEM
	}

	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errKeyNotPKIX, err)
	}

	edKey, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, errKeyNotEd25519
	}

	return edKey, nil
}
