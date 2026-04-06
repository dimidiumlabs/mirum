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
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"

	"dimidiumlabs/mirum/internal/config"
	"dimidiumlabs/mirum/internal/forges"
	"dimidiumlabs/mirum/internal/protocol/pb"
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
	r.Use(h.recoverer)
	r.Use(middleware.Compress(5))
	r.Use(middleware.Heartbeat("/ping"))
	r.Use(middleware.Timeout(config.WebRequestTimeout))
	r.Use(middleware.RequestSize(config.WebMaxBodyBytes))
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
		r.Use(httprate.LimitByIP(config.AuthRateLimit, config.AuthRateWindow))
		r.Use(middleware.RequestSize(config.AuthMaxBodyBytes))

		r.Get("/login", h.loginPage)
		r.Post("/login", h.login)
		r.Post("/logout", h.logout)
	})

	r.With(middleware.NoCache, httprate.LimitByIP(config.APIRateLimit, config.APIRateWindow)).
		Mount("/api/v1", http.StripPrefix("/api/v1", adminHandler))

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		h.renderError(w, r, http.StatusNotFound)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		h.renderError(w, r, http.StatusMethodNotAllowed)
	})

	var tlsCfg *tls.Config
	if srv.cfg.WebTls != nil {
		certs := newCertReloader(srv.cfg.WebTls.Cert, srv.cfg.WebTls.Key)
		tlsCfg = &tls.Config{
			MinVersion:     tls.VersionTLS13,
			GetCertificate: certs.GetCertificate,
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

type actorKey struct{}

type webHandler struct {
	srv    *server
	assets *assetResolver
}

// ActorFromContext returns the authenticated actor, or AnonActor if none.
func ActorFromContext(ctx context.Context) Actor {
	if v, ok := ctx.Value(actorKey{}).(Actor); ok {
		return v
	}
	return AnonActor()
}

// SessionMiddleware resolves the session cookie and puts the Actor in context.
func (h *webHandler) SessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil {
			if actor, err := h.srv.db.UserSessionGet(r.Context(), SystemActor(), c.Value); err == nil {
				ctx := context.WithValue(r.Context(), actorKey{}, actor)
				r = r.WithContext(ctx)
			}
		}
		next.ServeHTTP(w, r)
	})
}

type authedHandler func(w http.ResponseWriter, r *http.Request, actor Actor)

func authonly(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor := ActorFromContext(r.Context())
		if actor.Kind() == KindAnon {
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		next(w, r, actor)
	}
}

func (h *webHandler) index(w http.ResponseWriter, r *http.Request, actor Actor) {
	h.assets.renderPage(w, "dashboard", http.StatusOK, map[string]any{
		"user": map[string]string{"email": actor.Email()},
		"csrf": csrfToken(w, r),
	})
}

// renderError serves the error page with the given HTTP status.
func (h *webHandler) renderError(w http.ResponseWriter, r *http.Request, status int) {
	h.assets.renderPage(w, "error", status, map[string]any{"status": status})
}

// recoverer catches panics, logs them, and renders the 500 page so the
// client sees something more useful than chi's plaintext default. The
// http.ErrAbortHandler sentinel is re-raised so net/http's server
// machinery can recognise an intentional handler abort.
func (h *webHandler) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rvr := recover()
			if rvr == nil {
				return
			}
			if rvr == http.ErrAbortHandler {
				panic(rvr)
			}
			slog.Error("panic",
				"err", rvr,
				"path", r.URL.Path,
				"stack", string(debug.Stack()),
			)
			h.renderError(w, r, http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
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
	h.renderLogin(w, r, http.StatusOK, pb.ErrorReason_ERROR_REASON_UNSPECIFIED)
}

// renderLogin is the single entry point for every login-flow outcome that
// lands back on the login page. Reason == UNSPECIFIED means no error banner.
// No caller writes error text itself — the client maps reason → copy.
func (h *webHandler) renderLogin(w http.ResponseWriter, r *http.Request, status int, reason pb.ErrorReason) {
	data := map[string]any{"csrf": csrfToken(w, r)}
	if reason != pb.ErrorReason_ERROR_REASON_UNSPECIFIED {
		data["errorReason"] = int32(reason)
	}
	h.assets.renderPage(w, "login", status, data)
}

func (h *webHandler) login(w http.ResponseWriter, r *http.Request) {
	if !csrfOK(r) {
		clearCookie(w, csrfCookie)
		h.renderLogin(w, r, http.StatusForbidden, pb.ErrorReason_ERROR_REASON_INVALID_CSRF)
		return
	}

	email := r.FormValue("email")
	password := r.FormValue("password")

	userID, err := h.srv.db.UserVerifyPassword(r.Context(), SystemActor(), email, password, []byte(h.srv.cfg.Pepper))
	if err != nil {
		h.renderLogin(w, r, http.StatusUnauthorized, pb.ErrorReason_ERROR_REASON_INVALID_CREDENTIALS)
		return
	}

	token, err := h.srv.db.UserSessionCreate(r.Context(), SystemActor(), userID)
	if err != nil {
		slog.Error("create session failed", "err", err)
		h.renderLogin(w, r, http.StatusInternalServerError, pb.ErrorReason_ERROR_REASON_INTERNAL)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(config.SessionTTL.Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *webHandler) logout(w http.ResponseWriter, r *http.Request) {
	if !csrfOK(r) {
		// Forged logout attempt — ignore silently. Session stays valid,
		// user ends up wherever / takes them.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		h.srv.db.UserSessionDelete(r.Context(), SystemActor(), c.Value)
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
		MaxAge:   int(config.SessionTTL.Seconds()),
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
