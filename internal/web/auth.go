package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

type authGuard struct {
	token string
	mu    sync.Mutex
	fails map[string]failBucket
}

type failBucket struct {
	n    int
	until time.Time
}

func newAuthGuard(token string) *authGuard {
	return &authGuard{token: token, fails: map[string]failBucket{}}
}

func generateToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (a *authGuard) check(r *http.Request) bool {
	tok := bearerToken(r)
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	if tok == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(a.token)) == 1
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const p = "Bearer "
	if strings.HasPrefix(h, p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}

func (a *authGuard) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static assets and health are public; API/WS require auth.
		path := r.URL.Path
		if path == "/api/health" || path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/ws/") {
			ip := clientIP(r)
			if a.rateLimited(ip) {
				http.Error(w, "too many auth failures", http.StatusTooManyRequests)
				return
			}
			if !a.check(r) {
				a.recordFail(ip)
				w.Header().Set("WWW-Authenticate", `Bearer realm="qma"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			a.clearFail(ip)
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	if x := r.Header.Get("X-Forwarded-For"); x != "" {
		return strings.TrimSpace(strings.Split(x, ",")[0])
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[:i]
	}
	return host
}

func (a *authGuard) rateLimited(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.fails[ip]
	if !ok {
		return false
	}
	if time.Now().Before(b.until) && b.n >= 10 {
		return true
	}
	return false
}

func (a *authGuard) recordFail(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.fails[ip]
	if time.Now().After(b.until) {
		b = failBucket{}
	}
	b.n++
	if b.n >= 10 {
		b.until = time.Now().Add(5 * time.Minute)
	}
	a.fails[ip] = b
}

func (a *authGuard) clearFail(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.fails, ip)
}
