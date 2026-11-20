package httpapi

import (
	"container/list"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"golang.org/x/text/encoding/unicode"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

const (
	sessionEndpoint = "/api/auth/session"
	sessionCookie   = "humanengine-session"
	sessionMarker   = "X-HumanEngine-Session"
	sessionTTL      = 7 * 24 * time.Hour
	sessionCapacity = 1024
	sessionBodyMax  = 8192
)

type browserSessions struct {
	mu     sync.Mutex
	now    func() time.Time
	byID   map[[sha256.Size]byte]*list.Element
	order  list.List
	secure bool
}

type browserSession struct {
	id, credential [sha256.Size]byte
	expires        int64
}

func newBrowserSessions(secure bool) *browserSessions {
	return &browserSessions{now: time.Now, byID: make(map[[sha256.Size]byte]*list.Element), secure: secure}
}

// WithBrowserSessions binds a process-local browser gateway. Direct bearer API
// clients retain the existing application authentication and recovery path.
func (s *Server) WithBrowserSessions(secure bool) {
	s.browserSessions = newBrowserSessions(secure)
}

func (s *browserSessions) remove(entry *list.Element) {
	delete(s.byID, entry.Value.(browserSession).id)
	s.order.Remove(entry)
}

func (s *browserSessions) revoke(id string) {
	key := sha256.Sum256([]byte(id))
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.byID[key]; entry != nil {
		s.remove(entry)
	}
}

func (s *browserSessions) find(id, token string) (int64, bool) {
	key := sha256.Sum256([]byte(id))
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.byID[key]
	if entry == nil {
		return 0, false
	}
	record := entry.Value.(browserSession)
	if record.expires <= s.now().UnixMilli() || record.credential != sha256.Sum256([]byte(token)) {
		s.remove(entry)
		return 0, false
	}
	return record.expires, true
}

func (s *browserSessions) create(previous, token string) (string, int64) {
	var secret [32]byte
	_, _ = rand.Read(secret[:]) // Go's crypto/rand.Read either succeeds or terminates.
	id := base64.RawURLEncoding.EncodeToString(secret[:])
	key, previousKey := sha256.Sum256([]byte(id)), sha256.Sum256([]byte(previous))
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.byID[previousKey]; entry != nil {
		s.remove(entry)
	}
	now := s.now().UnixMilli()
	for entry := s.order.Front(); entry != nil; {
		next := entry.Next()
		if entry.Value.(browserSession).expires <= now {
			s.remove(entry)
		}
		entry = next
	}
	if len(s.byID) == sessionCapacity {
		s.remove(s.order.Front())
	}
	expires := now + sessionTTL.Milliseconds()
	s.byID[key] = s.order.PushBack(browserSession{key, sha256.Sum256([]byte(token)), expires})
	return id, expires
}

func browserSessionCookie(r *http.Request) string {
	for _, header := range r.Header.Values("Cookie") {
		for part := range strings.SplitSeq(header, ";") {
			if id, ok := strings.CutPrefix(strings.TrimSpace(part), sessionCookie+"="); ok {
				return id
			}
		}
	}
	return ""
}

func browserSessionOrigin(r *http.Request, secure bool) (string, bool) {
	scheme := "http"
	if r.TLS != nil {
		scheme, secure = "https", true
	}
	host := strings.ToLower(r.Host)
	if _, port, err := net.SplitHostPort(host); err == nil &&
		(scheme == "http" && port == "80" || scheme == "https" && port == "443") {
		host = strings.TrimSuffix(host, ":"+port)
	}
	if secure {
		scheme = "https"
	}
	return scheme + "://" + host, secure
}

// admit either returns the existing request, translates a verified cookie once
// onto the bearer gate, or completes a gateway-only response.
func (s *browserSessions) admit(w http.ResponseWriter, r *http.Request, token string) *http.Request {
	path := r.URL.EscapedPath()
	endpoint := path == sessionEndpoint
	if !endpoint {
		if _, bearer := r.Header["Authorization"]; bearer || !strings.HasPrefix(path, "/api/") {
			return r
		}
	}
	origin, secure := browserSessionOrigin(r, s.secure)
	originHeader := strings.Join(r.Header.Values("Origin"), ", ")
	marker := strings.Join(r.Header.Values(sessionMarker), ", ")
	site := strings.Join(r.Header.Values("Sec-Fetch-Site"), ", ")
	browser := marker == "1" && (originHeader == "" || originHeader == origin) && site != "cross-site"
	if endpoint {
		s.serveSession(w, r, token, browser, secure)
		return nil
	}
	if browser && token != "" {
		if _, ok := s.find(browserSessionCookie(r), token); ok {
			forwarded := r.Clone(r.Context())
			forwarded.Header.Set("Authorization", "Bearer "+token)
			return forwarded
		}
	}
	sessionError(w, http.StatusUnauthorized, "Authentication required")
	return nil
}

func (s *browserSessions) serveSession(w http.ResponseWriter, r *http.Request, token string, browser, secure bool) {
	if !browser {
		sessionError(w, http.StatusForbidden, "Session request rejected")
		return
	}
	if r.Method == http.MethodDelete {
		s.revoke(browserSessionCookie(r))
		w.Header().Set("Set-Cookie", sessionSetCookie("", "0", secure))
		sessionReply(w, http.StatusOK, sessionState{})
		return
	}
	if token == "" {
		sessionError(w, http.StatusServiceUnavailable, "Administration token is not configured")
		return
	}
	switch r.Method {
	case http.MethodPost:
		s.login(w, r, token, secure)
	case http.MethodGet:
		if expires, ok := s.find(browserSessionCookie(r), token); ok {
			sessionReply(w, http.StatusOK, sessionState{true, expires})
		} else {
			sessionError(w, http.StatusUnauthorized, "Authentication required")
		}
	default:
		sessionError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *browserSessions) login(w http.ResponseWriter, r *http.Request, token string, secure bool) {
	if r.ContentLength > sessionBodyMax {
		sessionError(w, http.StatusRequestEntityTooLarge, "Invalid request")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, sessionBodyMax+1))
	if len(raw) > sessionBodyMax {
		sessionError(w, http.StatusRequestEntityTooLarge, "Invalid request")
		return
	}
	if err != nil {
		sessionError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	supplied, valid := browserLoginToken(raw)
	if !valid {
		sessionError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) != 1 {
		sessionError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	id, expires := s.create(browserSessionCookie(r), token)
	w.Header().Set("Set-Cookie", sessionSetCookie(id, "604800", secure))
	sessionReply(w, http.StatusOK, sessionState{true, expires})
}

func browserLoginToken(raw []byte) (string, bool) {
	// Buffer.toString('utf8') replaces each invalid Unicode subsequence
	// before JSON.parse; only the unusual malformed request needs decoding.
	if !utf8.Valid(raw) {
		var err error
		raw, err = unicode.UTF8.NewDecoder().Bytes(raw)
		if err != nil {
			return "", false
		}
	}
	value, err := jsjson.ParseBytes(raw)
	input, ok := value.(*jsjson.Object)
	if err != nil || !ok {
		return "", false
	}
	value, _ = input.Get("token")
	supplied, ok := value.(string)
	if !ok {
		return "", false
	}
	length, err := jsjson.UTF16Len(supplied)
	if err != nil || length > 4096 {
		return "", false
	}
	supplied, err = jsjson.UTF8Text(supplied)
	return supplied, err == nil
}

type sessionState struct {
	Authenticated bool  `json:"authenticated"`
	ExpiresAt     int64 `json:"expiresAt,omitempty"`
}

func sessionSetCookie(id, age string, secure bool) string {
	cookie := sessionCookie + "=" + id + "; Path=/api; HttpOnly; SameSite=Strict; Max-Age=" + age
	if secure {
		cookie += "; Secure"
	}
	return cookie
}

func sessionError(w http.ResponseWriter, status int, message string) {
	sessionReply(w, status, struct {
		Error string `json:"error"`
	}{message})
}

func sessionReply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	raw, _ := json.Marshal(value)
	_, _ = w.Write(raw)
}
