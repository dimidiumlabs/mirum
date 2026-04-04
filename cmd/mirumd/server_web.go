// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/base64"
	"errors"
	"html/template"
	"io"
	"net"
	"net/http"

	"dimidiumlabs/mirum/internal/database"
	"dimidiumlabs/mirum/internal/forges"
)

//go:embed templates/*.html
var templateFS embed.FS

var (
	indexTmpl = template.Must(template.ParseFS(templateFS, "templates/layout.html", "templates/index.html"))
	loginTmpl = template.Must(template.ParseFS(templateFS, "templates/layout.html", "templates/login.html"))
)

func NewWebServer(ctx context.Context, srv *server) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var data struct{ Email, CSRF string }
		if c, err := r.Cookie("session"); err == nil {
			if sess, err := srv.db.UserGetSession(r.Context(), c.Value); err == nil {
				data.Email = sess.Email
				data.CSRF = csrfToken(w, r)
			}
		}
		indexTmpl.ExecuteTemplate(w, "layout", data)
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

	mux.HandleFunc("GET /auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		loginTmpl.ExecuteTemplate(w, "layout", map[string]string{"CSRF": csrfToken(w, r)})
	})

	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if !csrfOK(r) {
			clearCookie(w, "csrf")
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}

		email := r.FormValue("email")
		password := r.FormValue("password")

		userID, err := srv.db.UserVerifyPassword(r.Context(), email, password, []byte(srv.cfg.Pepper))
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			loginTmpl.ExecuteTemplate(w, "layout", map[string]string{
				"Error": "Invalid credentials",
				"CSRF":  csrfToken(w, r),
			})
			return
		}

		token, err := srv.db.UserCreateSession(r.Context(), userID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(database.SessionTTL.Seconds()),
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if !csrfOK(r) {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		if c, err := r.Cookie("session"); err == nil {
			srv.db.UserDeleteSession(r.Context(), c.Value)
		}
		clearCookie(w, "session")
		clearCookie(w, "csrf")
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
	})

	var tls_config *tls.Config = nil
	if srv.cfg.WebTls != nil {
		tls_config = &tls.Config{
			MinVersion: tls.VersionTLS13,
			GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
				cert, err := tls.LoadX509KeyPair(srv.cfg.WebTls.Cert, srv.cfg.WebTls.Key)
				return &cert, err
			},
		}
	}

	return &http.Server{
		Handler:   mux,
		TLSConfig: tls_config,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}
}

// csrfToken returns the current CSRF token, setting a cookie if absent.
func csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("csrf"); err == nil && c.Value != "" {
		return c.Value
	}
	b := make([]byte, 32)
	rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
	return token
}

// csrfOK checks that the form field matches the cookie (double-submit).
func csrfOK(r *http.Request) bool {
	cookie, err := r.Cookie("csrf")
	if err != nil || cookie.Value == "" {
		return false
	}
	field := r.FormValue("csrf")
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(field)) == 1
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		MaxAge:   -1,
	})
}
