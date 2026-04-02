// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"dimidiumlabs/mirum/internal/forges"
)

func NewWwwServer(ctx context.Context, srv *server) *http.Server {
	return &http.Server{
		Handler: wwwRoutes(srv),
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}
}

func wwwRoutes(srv *server) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Mirum</title></head><body><h1>Mirum</h1><p>CI server is running.</p></body></html>`)
	})

	mux.HandleFunc("POST /webhook", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}

		ev, err := srv.forge.Webhook(r, body)
		if errors.Is(err, forges.ErrInvalidSignature) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if ev == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		srv.enqueue(ev)
		w.WriteHeader(http.StatusAccepted)
	})

	return mux
}
