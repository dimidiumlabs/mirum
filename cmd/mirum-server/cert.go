// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"crypto/tls"
	"log/slog"
	"os"
	"sync"
	"time"
)

// certReloader serves a TLS cert/key pair and reloads it when either file
// changes on disk (e.g. after a letsencrypt renewal).
type certReloader struct {
	certFile string
	keyFile  string

	mu      sync.Mutex
	cert    *tls.Certificate
	certMod time.Time
	keyMod  time.Time
}

func newCertReloader(certFile, keyFile string) *certReloader {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	if _, err := r.GetCertificate(nil); err != nil {
		slog.Warn("initial cert load failed", "cert", certFile, "err", err)
	}
	return r
}

// GetCertificate plugs into tls.Config.GetCertificate. On a transient
// reload error it returns the last good pair so a mid-renewal race
// (cert swapped but key still being written) doesn't break handshakes.
func (r *certReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	cs, cerr := os.Stat(r.certFile)
	ks, kerr := os.Stat(r.keyFile)

	r.mu.Lock()
	defer r.mu.Unlock()

	if cerr == nil && kerr == nil && r.cert != nil &&
		cs.ModTime().Equal(r.certMod) && ks.ModTime().Equal(r.keyMod) {
		return r.cert, nil
	}

	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		if r.cert != nil {
			slog.Warn("cert reload failed, serving cached", "cert", r.certFile, "err", err)
			return r.cert, nil
		}
		return nil, err
	}

	r.cert = &cert
	if cerr == nil {
		r.certMod = cs.ModTime()
	}
	if kerr == nil {
		r.keyMod = ks.ModTime()
	}
	slog.Info("cert loaded", "cert", r.certFile)
	return r.cert, nil
}
