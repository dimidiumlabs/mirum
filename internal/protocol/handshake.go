// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"time"
)

var (
	ErrKeyNotPEM     = errors.New("key file does not contain a PEM block")
	ErrKeyNotPKCS8   = errors.New("key file does not contain a PKCS8 private key")
	ErrKeyNotPKIX    = errors.New("key file does not contain a PKIX public key")
	ErrKeyNotEd25519 = errors.New("key file does not contain an ed25519 key")

	ErrClockSkew = errors.New("clock skew too large")
)

// WorkerMeta describes the worker for embedding in a self-signed X.509
// certificate as a URI SAN (mirum:worker?name=...&version=...&...).
type WorkerMeta struct {
	Name    string
	Version string
	Os      string
	Arch    string
	Runtime string
}

// URI encodes worker metadata as a mirum: URI.
func (m *WorkerMeta) URI() *url.URL {
	return &url.URL{
		Scheme: "mirum",
		Opaque: "worker",
		RawQuery: url.Values{
			"name":    {m.Name},
			"version": {m.Version},
			"os":      {m.Os},
			"arch":    {m.Arch},
			"runtime": {m.Runtime},
		}.Encode(),
	}
}

// ParseWorkerMeta extracts WorkerMeta from a certificate's URI SANs.
// Returns nil if no mirum:worker URI is found.
func ParseWorkerMeta(cert *x509.Certificate) *WorkerMeta {
	for _, u := range cert.URIs {
		if u.Scheme == "mirum" && u.Opaque == "worker" {
			q := u.Query()
			return &WorkerMeta{
				Name:    q.Get("name"),
				Version: q.Get("version"),
				Os:      q.Get("os"),
				Arch:    q.Get("arch"),
				Runtime: q.Get("runtime"),
			}
		}
	}
	return nil
}

// LoadPrivateKey reads a PEM-encoded PKCS8 ed25519 private key from path.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, ErrKeyNotPEM
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKeyNotPKCS8, err)
	}

	edKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, ErrKeyNotEd25519
	}

	return edKey, nil
}

// SelfSignedCert generates a self-signed X.509 certificate from an ed25519
// private key with worker metadata encoded as a URI SAN. The server extracts
// the public key for authentication, metadata from the URI, and uses NotBefore
// for clock skew detection.
func SelfSignedCert(key ed25519.PrivateKey, meta *WorkerMeta) (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		NotBefore:    now,
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{meta.URI()},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create certificate: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
	}, nil
}
