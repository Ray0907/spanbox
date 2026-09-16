package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"mime"
	"net/http"
	"strings"
)

const loginFailure = "Invalid token."

func tokenEqual(a, b string) bool {
	aHash := sha256.Sum256([]byte(a))
	bHash := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(aHash[:], bHash[:]) == 1
}

func sessionValue(token string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("spanbox-ui-session"))
	return hex.EncodeToString(mac.Sum(nil))
}

func authenticate(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	session := sessionValue(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/" && r.Method == http.MethodPost {
			provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || !tokenEqual(provided, token) {
				writeStatus(w, responseMediaType(r), http.StatusUnauthorized, 16, "unauthorized")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie("spanbox_session")
		if err != nil || !tokenEqual(cookie.Value, session) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type loginPage struct {
	Error string
}

func (deps Deps) renderLogin(w http.ResponseWriter, status int, errorMessage string) {
	if status != http.StatusOK {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
	}
	deps.render(w, "login", loginPage{Error: errorMessage}, "templates/login.html")
}

func (deps Deps) login(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		deps.renderLogin(w, http.StatusOK, "")
	case http.MethodPost:
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/x-www-form-urlencoded" {
			http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		if err := r.ParseForm(); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
				return
			}
			deps.renderLogin(w, http.StatusUnauthorized, loginFailure)
			return
		}
		if !tokenEqual(r.PostForm.Get("token"), deps.Cfg.AuthToken) {
			deps.renderLogin(w, http.StatusUnauthorized, loginFailure)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: "spanbox_session", Value: sessionValue(deps.Cfg.AuthToken), Path: "/", HttpOnly: true,
			SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		})
		http.Redirect(w, r, "/", http.StatusFound)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
