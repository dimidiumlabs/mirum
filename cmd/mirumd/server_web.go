// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"

	"dimidiumlabs/mirum/internal/database"
	"dimidiumlabs/mirum/internal/forges"
)

// __Host- prefixed cookies can only be set with Secure, Path=/, and no
// Domain attribute. Browsers silently reject violations, so subdomain and
// network attackers cannot forge them.
const (
	sessionCookie = "__Host-session"
	csrfCookie    = "__Host-csrf"
)

func NewWebServer(ctx context.Context, srv *server, adminPath string, adminHandler http.Handler) *http.Server {
	h := &webHandler{
		srv:    srv,
		assets: newAssetResolver(),
	}

	r := chi.NewRouter()

	r.Use(middleware.CleanPath)
	r.Use(middleware.StripSlashes)
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))
	r.Use(middleware.Heartbeat("/ping"))
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(middleware.RequestSize(64 << 20)) // 64 MiB global body limit
	r.Use(trustedProxyMiddleware(srv.cfg.TrustedProxies))

	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", csp)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
			w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
			w.Header().Set("Permissions-Policy", "accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), payment=(), usb=()")

			if srv.cfg.WebTls != nil {
				w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
			}

			next.ServeHTTP(w, r)
		})
	})

	// The authorization session sets the user to ctx
	r.Use(h.SessionMiddleware)

	r.With(middleware.SetHeader("Cache-Control", "public, max-age=31536000, immutable")).
		Mount("/assets", assetsHandler())

	r.Get("/", authonly(h.index))
	r.Post("/webhook", h.webhook)

	r.Route("/auth", func(r chi.Router) {
		r.Use(middleware.NoCache)
		r.Use(httprate.LimitByIP(10, time.Minute))
		r.Use(middleware.RequestSize(4096))

		r.Get("/login", h.loginPage)
		r.Post("/login", h.login)
		r.Post("/logout", h.logout)
	})

	r.With(middleware.NoCache, httprate.LimitByIP(300, time.Minute)).
		Mount("/api/v1", http.StripPrefix("/api/v1", adminHandler))

	var tlsCfg *tls.Config
	if srv.cfg.WebTls != nil {
		tlsCfg = &tls.Config{
			MinVersion: tls.VersionTLS13,
			GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
				cert, err := tls.LoadX509KeyPair(srv.cfg.WebTls.Cert, srv.cfg.WebTls.Key)
				return &cert, err
			},
		}
	}

	return &http.Server{
		Handler:   r,
		TLSConfig: tlsCfg,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}
}

type callerKey struct{}

type callerInfo struct {
	UserID    uuid.UUID
	Email     string
	Superuser bool
}

type webHandler struct {
	srv    *server
	assets *assetResolver
}

// CallerFromContext returns the authenticated caller, or nil.
func CallerFromContext(ctx context.Context) *callerInfo {
	if v, ok := ctx.Value(callerKey{}).(*callerInfo); ok {
		return v
	}
	return nil
}

// SessionMiddleware resolves the session cookie and puts callerInfo in context.
func (h *webHandler) SessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil {
			if sess, err := h.srv.db.UserGetSession(r.Context(), uuid.Nil, c.Value); err == nil {
				caller := &callerInfo{
					UserID:    sess.UserID,
					Email:     sess.Email,
					Superuser: sess.Superuser,
				}
				ctx := context.WithValue(r.Context(), callerKey{}, caller)
				r = r.WithContext(ctx)
			}
		}
		next.ServeHTTP(w, r)
	})
}

type authedHandler func(w http.ResponseWriter, r *http.Request, caller *callerInfo)

func authonly(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := CallerFromContext(r.Context())
		if caller == nil {
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		next(w, r, caller)
	}
}

func (h *webHandler) index(w http.ResponseWriter, r *http.Request, caller *callerInfo) {
	h.assets.renderPage(w, "dashboard", map[string]any{
		"user": map[string]string{"email": caller.Email},
		"csrf": csrfToken(w, r),
	})
}

func (h *webHandler) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	ev, err := h.srv.forge.Webhook(r, body)
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

	h.srv.enqueue(ev)
	w.WriteHeader(http.StatusAccepted)
}

func (h *webHandler) loginPage(w http.ResponseWriter, r *http.Request) {
	h.assets.renderPage(w, "login", map[string]any{
		"csrf": csrfToken(w, r),
	})
}

func (h *webHandler) login(w http.ResponseWriter, r *http.Request) {
	if !csrfOK(r) {
		clearCookie(w, csrfCookie)
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}

	email := r.FormValue("email")
	password := r.FormValue("password")

	userID, err := h.srv.db.UserVerifyPassword(r.Context(), uuid.Nil, email, password, []byte(h.srv.cfg.Pepper))
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		h.assets.renderPage(w, "login", map[string]any{
			"csrf":  csrfToken(w, r),
			"error": "Wrong email or password. Please try again.",
		})
		return
	}

	token, err := h.srv.db.UserCreateSession(r.Context(), uuid.Nil, userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(database.SessionTTL.Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *webHandler) logout(w http.ResponseWriter, r *http.Request) {
	if !csrfOK(r) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		h.srv.db.UserDeleteSession(r.Context(), uuid.Nil, c.Value)
	}
	clearCookie(w, sessionCookie)
	clearCookie(w, csrfCookie)
	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

// csrfToken returns the current CSRF token, setting a cookie if absent.
func csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil && c.Value != "" {
		return c.Value
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
	return token
}

// csrfOK checks that the form field or X-CSRF-Token header matches the
// cookie (double-submit). Form posts use the hidden "csrf" field; API calls
// from the SPA pass the token via the X-CSRF-Token header.
func csrfOK(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	token := r.FormValue("csrf")
	if token == "" {
		token = r.Header.Get("X-CSRF-Token")
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) == 1
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

// trustedProxyMiddleware resolves the real client IP from X-Forwarded-For,
// walking right-to-left and stopping at the first untrusted hop.
// Empty cidrs = trust RemoteAddr only (safe default).
func trustedProxyMiddleware(cidrs []string) func(http.Handler) http.Handler {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("invalid trusted_proxies CIDR: " + c)
		}
		nets = append(nets, n)
	}

	isTrusted := func(ip net.IP) bool {
		for _, n := range nets {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(nets) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(host)
			if ip == nil || !isTrusted(ip) {
				// RemoteAddr is not a trusted proxy — use as-is.
				next.ServeHTTP(w, r)
				return
			}

			// Walk X-Forwarded-For right to left.
			xff := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
			for i := len(xff) - 1; i >= 0; i-- {
				candidate := strings.TrimSpace(xff[i])
				ip = net.ParseIP(candidate)
				if ip == nil {
					break // garbage — stop, don't trust anything further left
				}
				if !isTrusted(ip) {
					r.RemoteAddr = candidate + ":0"
					break
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}
