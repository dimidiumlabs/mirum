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
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"

	"dimidiumlabs/mirum/internal/database"
	"dimidiumlabs/mirum/internal/forges"
)

//go:embed templates/*.html
var templateFS embed.FS

var (
	indexTmpl = template.Must(template.ParseFS(templateFS, "templates/layout.html", "templates/index.html"))
	loginTmpl = template.Must(template.ParseFS(templateFS, "templates/layout.html", "templates/login.html"))
)

func NewWebServer(ctx context.Context, srv *server, adminPath string, adminHandler http.Handler) *http.Server {
	h := &webHandler{srv: srv}

	r := chi.NewRouter()

	r.Use(middleware.CleanPath)
	r.Use(middleware.StripSlashes)
	r.Use(middleware.RequestID)
	r.Use(trustedProxyMiddleware(srv.cfg.TrustedProxies))
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))
	r.Use(middleware.Heartbeat("/ping"))
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(middleware.RequestSize(64 << 20)) // 64 MiB global body limit

	// The authorization session sets the user to ctx
	r.Use(h.SessionMiddleware)

	r.Get("/", h.index)
	r.Post("/webhook", h.webhook)

	r.Route("/auth", func(r chi.Router) {
		r.Use(middleware.NoCache)
		r.Use(httprate.LimitByIP(10, time.Minute))
		r.Use(middleware.RequestSize(4096))

		r.Get("/login", h.loginPage)
		r.Post("/login", h.login)
		r.Post("/logout", h.logout)
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(middleware.NoCache)
		r.Use(httprate.LimitByIP(300, time.Minute))
		r.Mount(adminPath, adminHandler)
	})

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
	srv *server
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
		if c, err := r.Cookie("session"); err == nil {
			if sess, err := h.srv.db.UserGetSession(r.Context(), uuid.Nil, c.Value); err == nil {
				caller := &callerInfo{
					UserID: sess.UserID,
					Email:  sess.Email,
				}
				ctx := context.WithValue(r.Context(), callerKey{}, caller)
				r = r.WithContext(ctx)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (h *webHandler) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var data struct{ Email, CSRF string }
	if caller := CallerFromContext(r.Context()); caller != nil {
		data.Email = caller.Email
		data.CSRF = csrfToken(w, r)
	}
	indexTmpl.ExecuteTemplate(w, "layout", data)
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	loginTmpl.ExecuteTemplate(w, "layout", map[string]string{"CSRF": csrfToken(w, r)})
}

func (h *webHandler) login(w http.ResponseWriter, r *http.Request) {
	if !csrfOK(r) {
		clearCookie(w, "csrf")
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}

	email := r.FormValue("email")
	password := r.FormValue("password")

	userID, err := h.srv.db.UserVerifyPassword(r.Context(), uuid.Nil, email, password, []byte(h.srv.cfg.Pepper))
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		loginTmpl.ExecuteTemplate(w, "layout", map[string]string{
			"Error": "Invalid credentials",
			"CSRF":  csrfToken(w, r),
		})
		return
	}

	token, err := h.srv.db.UserCreateSession(r.Context(), uuid.Nil, userID)
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
}

func (h *webHandler) logout(w http.ResponseWriter, r *http.Request) {
	if !csrfOK(r) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if c, err := r.Cookie("session"); err == nil {
		h.srv.db.UserDeleteSession(r.Context(), uuid.Nil, c.Value)
	}
	clearCookie(w, "session")
	clearCookie(w, "csrf")
	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
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
