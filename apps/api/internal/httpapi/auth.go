package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"infrascope/api/internal/auth"
)

const sessionCookieName = "infrascope_session"

type sessionKey struct{}

// session wraps a handler that needs a logged-in user. A missing, invalid or
// expired session is a 401 (and clears the cookie); a database outage is a
// 503, never a 401, so an outage does not log anyone out.
func (a *api) session(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !safeMethod(r.Method) && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "forbidden", "Cross-origin requests may not change state.")
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Log in to use the API.")
			return
		}
		s, err := a.auth.Authenticate(r.Context(), cookie.Value)
		switch {
		case errors.Is(err, auth.ErrInvalidSession):
			http.SetCookie(w, a.expiredCookie())
			writeError(w, http.StatusUnauthorized, "unauthenticated", "Your session is invalid or has expired. Log in again.")
		case err != nil:
			a.fail(w, r, err, database)
		default:
			next(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, s)))
		}
	})
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *api) login(w http.ResponseWriter, r *http.Request) {
	client := clientIP(r)
	if ok, wait := a.limiter.Allow(client); !ok {
		a.logger.WarnContext(r.Context(), "login rate limited", "client", client)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many login attempts. Try again later.")
		return
	}
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "forbidden", "Cross-origin requests may not change state.")
		return
	}
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
		a.fail(w, r, invalid(FieldError{Field: "body", Message: "must be JSON (Content-Type: application/json)"}), database)
		return
	}
	var c credentials
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		a.fail(w, r, invalid(FieldError{Field: "body", Message: "must be a JSON object with username and password"}), database)
		return
	}
	var problems []FieldError
	if c.Username == "" || !validText(c.Username, 128) {
		problems = append(problems, FieldError{Field: "username", Message: "is required and must be printable text of at most 128 bytes"})
	}
	if c.Password == "" || len(c.Password) > auth.MaxPasswordBytes {
		problems = append(problems, FieldError{Field: "password", Message: "is required and must be at most 72 bytes"})
	}
	if len(problems) > 0 {
		a.fail(w, r, invalid(problems...), database)
		return
	}

	token, expires, err := a.auth.Login(r.Context(), c.Username, c.Password)
	switch {
	case errors.Is(err, auth.ErrInvalidCredentials):
		a.logger.WarnContext(r.Context(), "login failed", "client", client)
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Invalid username or password.")
		return
	case err != nil:
		a.fail(w, r, err, database)
		return
	}
	a.logger.InfoContext(r.Context(), "login succeeded", "client", client, "user", c.Username)
	http.SetCookie(w, a.sessionCookie(token, expires))
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) logout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(sessionCookieName)
	if err := a.auth.Logout(r.Context(), cookie.Value); err != nil {
		a.fail(w, r, err, database)
		return
	}
	http.SetCookie(w, a.expiredCookie())
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) me(w http.ResponseWriter, r *http.Request) {
	s := r.Context().Value(sessionKey{}).(auth.Session)
	writeJSON(w, http.StatusOK, map[string]string{
		"username":   s.Username,
		"expires_at": s.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (a *api) sessionCookie(token string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   a.settings.CookieSecure,
		SameSite: http.SameSiteStrictMode,
	}
}

func (a *api) expiredCookie() *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.settings.CookieSecure,
		SameSite: http.SameSiteStrictMode,
	}
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sameOrigin is a CSRF check on top of the SameSite=Strict cookie. Browsers
// send Origin on every state-changing request, so a request from another
// site, or another port of the same host, is rejected. Requests without
// Origin come from non-browser clients, which hold no victim's cookie.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// clientIP returns the address login attempts are counted against. Behind
// the dashboard's nginx the last X-Forwarded-For entry is the one nginx
// appended, the peer it actually saw; earlier entries come from the client
// and could be forged. The API is reachable only through nginx (or, with
// the debug override, from localhost).
func clientIP(r *http.Request) string {
	if values := r.Header.Values("X-Forwarded-For"); len(values) > 0 {
		hops := strings.Split(values[len(values)-1], ",")
		if ip := strings.TrimSpace(hops[len(hops)-1]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
