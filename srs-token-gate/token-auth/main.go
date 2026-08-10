package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/alexedwards/scs/v2"
	"github.com/fsnotify/fsnotify"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/crypto/bcrypt"
)

type Key struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Token      string `json:"token"`
	TokenHash  string `json:"token_hash,omitempty"`
	Scope      string `json:"scope"`
	Stream     string `json:"stream,omitempty"`
	ExpiresAt  int64  `json:"expires_at"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
}
type Session struct {
	ClientID   string `json:"client_id"`
	KeyID      int64  `json:"key_id"`
	UserName   string `json:"user_name"`
	Action     string `json:"action"`
	IP         string `json:"ip"`
	Vhost      string `json:"vhost"`
	App        string `json:"app"`
	Stream     string `json:"stream"`
	StartedAt  int64  `json:"started_at"`
	Protocol   string `json:"protocol,omitempty"`
	LastSeenAt int64  `json:"last_seen_at"`
}
type StoreData struct {
	NextID   int64     `json:"next_id"`
	Keys     []Key     `json:"keys"`
	Sessions []Session `json:"sessions"`
}
type Store struct {
	mu   sync.Mutex
	path string
	data StoreData
}

type srsClient struct {
	ID string `json:"id"`
}

type srsClientsResponse struct {
	Code    int         `json:"code"`
	Clients []srsClient `json:"clients"`
}

var cfg Config
var cfgMu sync.RWMutex
var configReloadMu sync.Mutex
var configPath = "/workspace/config.yaml"
var configTemplatePath = "/workspace/config.example.yaml"
var srsRenderedPath = "/generated/token-auth.conf"
var srsTemplatePath = "/workspace/conf/srs/token-auth.conf"
var store *Store
var srsHTTPClient = &http.Client{Timeout: 3 * time.Second}
var sessionManager *scs.SessionManager
var srsHTTPProxy http.Handler
var srsAPIProxy http.Handler
var srsMediaProxy http.Handler
var srsRTCProxy http.Handler

type aggregatedSession struct {
	UserName    string   `json:"user_name"`
	Action      string   `json:"action"`
	IP          string   `json:"ip"`
	App         string   `json:"app"`
	Stream      string   `json:"stream"`
	Protocols   []string `json:"protocols"`
	ClientCount int      `json:"client_count"`
	StartedAt   int64    `json:"started_at"`
	LastSeenAt  int64    `json:"last_seen_at"`
}

func configSnapshot() Config    { cfgMu.RLock(); defer cfgMu.RUnlock(); return cfg }
func replaceConfig(next Config) { cfgMu.Lock(); cfg = next; cfgMu.Unlock() }

const uiPath = "/app/ui/index.html"

func now() int64                { return time.Now().Unix() }
func tokenHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func newToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func passwordMatches(password string) bool {
	current := configSnapshot()
	if current.Admin.PasswordHash != "" {
		return bcrypt.CompareHashAndPassword([]byte(current.Admin.PasswordHash), []byte(password)) == nil
	}
	return subtle.ConstantTimeCompare([]byte(password), []byte(current.Admin.Password)) == 1
}
func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		s.data = StoreData{NextID: 1, Keys: []Key{}, Sessions: []Session{}}
		return s.saveLocked()
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return err
	}
	if s.data.NextID < 1 {
		s.data.NextID = 1
	}
	changed := false
	for i := range s.data.Keys {
		if s.data.Keys[i].Token == "" || s.data.Keys[i].TokenHash == "" {
			token, err := newToken()
			if err != nil {
				return err
			}
			s.data.Keys[i].Token = token
			s.data.Keys[i].TokenHash = tokenHash(token)
			changed = true
		}
	}
	if changed {
		return s.saveLocked()
	}
	return nil
}
func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
func (s *Store) save() error { s.mu.Lock(); defer s.mu.Unlock(); return s.saveLocked() }

func basicAuth(r *http.Request) bool {
	u, p, ok := r.BasicAuth()
	if !ok {
		return false
	}
	current := configSnapshot()
	if subtle.ConstantTimeCompare([]byte(u), []byte(current.Admin.Username)) != 1 {
		return false
	}
	return passwordMatches(p)
}

func sessionAuth(r *http.Request) bool {
	return sessionManager != nil && sessionManager.GetString(r.Context(), "username") != ""
}

func authenticated(r *http.Request) bool { return basicAuth(r) || sessionAuth(r) }

func requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if authenticated(r) {
		return true
	}
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
	return false
}

func requestIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
		return forwarded
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func normalizeStreamName(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", true
	}
	if len([]byte(v)) > 128 || strings.ContainsAny(v, "/?#&") || strings.IndexFunc(v, unicode.IsSpace) >= 0 {
		return "", false
	}
	return v, true
}

func streamAllowed(allowedStream, stream string) bool {
	return allowedStream == "" || allowedStream == stream
}
func jsonOut(w http.ResponseWriter, code int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}
func readJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func cleanToken(v string) string {
	v = strings.TrimSpace(v)
	if decoded, err := url.QueryUnescape(v); err == nil {
		v = decoded
	}
	if i := strings.IndexAny(v, "&?#/"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

func tokenFromRaw(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil {
		if v := cleanToken(u.Query().Get("token")); v != "" {
			return v
		}
	}
	if q, err := url.ParseQuery(strings.TrimPrefix(raw, "?")); err == nil {
		if v := cleanToken(q.Get("token")); v != "" {
			return v
		}
	}
	for offset := 0; offset < len(raw); {
		i := strings.Index(raw[offset:], "token=")
		if i < 0 {
			break
		}
		start := offset + i + len("token=")
		if v := cleanToken(raw[start:]); v != "" {
			return v
		}
		offset = start
	}
	return ""
}

func extractToken(p map[string]any) string {
	if v, _ := p["token"].(string); v != "" {
		if token := cleanToken(v); token != "" {
			return token
		}
	}
	for _, k := range []string{"param", "query", "tcUrl", "tc_url", "stream_url"} {
		if raw, ok := p[k].(string); ok {
			if token := tokenFromRaw(raw); token != "" {
				return token
			}
		}
	}
	return ""
}

func hookSecretMatches(r *http.Request) bool {
	expected := strings.TrimSpace(configSnapshot().HookSecret)
	provided := strings.TrimSpace(r.URL.Query().Get("secret"))
	if expected == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func allowed(scope, action string) bool {
	return scope == "both" || (action == "on_publish" && scope == "publish") || (action == "on_play" && scope == "play")
}

func protocolFromPayload(p map[string]any, action string) string {
	for _, key := range []string{"protocol", "type"} {
		if value, ok := p[key].(string); ok {
			value = strings.ToLower(value)
			if strings.Contains(value, "hls") {
				return "hls"
			}
			if strings.Contains(value, "flv") {
				return "http-flv"
			}
			if strings.Contains(value, "rtc") || strings.Contains(value, "webrtc") {
				return "webrtc"
			}
			if strings.Contains(value, "rtmp") {
				return "rtmp"
			}
		}
	}
	for _, key := range []string{"url", "stream_url", "tcUrl", "tc_url"} {
		if value, ok := p[key].(string); ok {
			value = strings.ToLower(value)
			if strings.Contains(value, ".m3u8") || strings.Contains(value, ".ts") {
				return "hls"
			}
			if strings.Contains(value, ".flv") {
				return "http-flv"
			}
			if strings.HasPrefix(value, "webrtc") || strings.Contains(value, "/rtc/") {
				return "webrtc"
			}
			if strings.HasPrefix(value, "rtmp") {
				return "rtmp"
			}
		}
	}
	if action == "on_publish" {
		return "rtmp"
	}
	return "unknown"
}

func updateSession(s Session) {
	if s.LastSeenAt == 0 {
		s.LastSeenAt = now()
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for i := range store.data.Sessions {
		if store.data.Sessions[i].ClientID == s.ClientID {
			if store.data.Sessions[i].StartedAt > 0 {
				s.StartedAt = store.data.Sessions[i].StartedAt
			}
			store.data.Sessions[i] = s
			_ = store.saveLocked()
			return
		}
	}
	store.data.Sessions = append(store.data.Sessions, s)
	_ = store.saveLocked()
}

func removeSession(clientID string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	out := store.data.Sessions[:0]
	for _, s := range store.data.Sessions {
		if s.ClientID != clientID {
			out = append(out, s)
		}
	}
	if len(out) != len(store.data.Sessions) {
		store.data.Sessions = out
		_ = store.saveLocked()
	}
}

func syntheticSessionID(keyID int64, action, ip, app, stream, protocol string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s|%s|%s|%s", keyID, action, ip, app, stream, protocol)))
	return "gateway-" + hex.EncodeToString(sum[:8])
}

func recordGatewaySession(k Key, action, ip, app, stream, protocol string) string {
	id := syntheticSessionID(k.ID, action, ip, app, stream, protocol)
	updateSession(Session{ClientID: id, KeyID: k.ID, UserName: k.Name, Action: action, IP: ip, App: app, Stream: stream, StartedAt: now(), LastSeenAt: now(), Protocol: protocol})
	return id
}

func aggregateSessions(raw []Session) []aggregatedSession {
	type aggregate struct {
		item      aggregatedSession
		clients   map[string]bool
		protocols map[string]bool
	}
	groups := make(map[string]*aggregate)
	for _, s := range raw {
		key := fmt.Sprintf("%d|%s|%s|%s|%s", s.KeyID, s.Action, s.IP, s.App, s.Stream)
		g := groups[key]
		if g == nil {
			g = &aggregate{item: aggregatedSession{UserName: s.UserName, Action: s.Action, IP: s.IP, App: s.App, Stream: s.Stream, StartedAt: s.StartedAt, LastSeenAt: s.LastSeenAt}, clients: map[string]bool{}, protocols: map[string]bool{}}
			groups[key] = g
		}
		if s.ClientID != "" {
			g.clients[s.ClientID] = true
		}
		if s.Protocol != "" {
			g.protocols[s.Protocol] = true
		}
		if s.StartedAt > 0 && (g.item.StartedAt == 0 || s.StartedAt < g.item.StartedAt) {
			g.item.StartedAt = s.StartedAt
		}
		if s.LastSeenAt > g.item.LastSeenAt {
			g.item.LastSeenAt = s.LastSeenAt
		}
	}
	out := make([]aggregatedSession, 0, len(groups))
	for _, g := range groups {
		g.item.ClientCount = len(g.clients)
		for protocol := range g.protocols {
			g.item.Protocols = append(g.item.Protocols, protocol)
		}
		sort.Strings(g.item.Protocols)
		out = append(out, g.item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeenAt > out[j].LastSeenAt })
	return out
}

func validate(token, action string) (Key, bool) {
	if token == "" {
		return Key{}, false
	}
	h := tokenHash(token)
	store.mu.Lock()
	defer store.mu.Unlock()
	for i := range store.data.Keys {
		k := &store.data.Keys[i]
		if k.TokenHash == h && k.Enabled && (k.ExpiresAt == 0 || k.ExpiresAt >= now()) && allowed(k.Scope, action) {
			k.LastUsedAt = now()
			_ = store.saveLocked()
			return *k, true
		}
	}
	return Key{}, false
}
func hook(w http.ResponseWriter, r *http.Request) {
	hookReply := func(code int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"code":%d}`, code)
	}
	if !hookSecretMatches(r) {
		slog.Warn("srs hook rejected", "ip", requestIP(r), "reason", "invalid_hook_secret")
		hookReply(1)
		return
	}
	var p map[string]any
	if err := readJSON(r, &p); err != nil {
		hookReply(1)
		return
	}
	action, _ := p["action"].(string)
	cid, _ := p["client_id"].(string)
	if action == "on_publish" || action == "on_play" {
		token := extractToken(p)
		k, ok := validate(token, action)
		rawStream, _ := p["stream"].(string)
		stream := normalizePlaybackStream(rawStream)
		if ok && !streamAllowed(k.Stream, stream) {
			ok = false
		}
		if !ok {
			reason := "invalid_disabled_expired_or_scope"
			if token == "" {
				reason = "missing_token"
			} else if k.Stream != "" && !streamAllowed(k.Stream, stream) {
				reason = "stream_mismatch"
			}
			slog.Error("token auth rejected", "action", action, "client_id", cid, "ip", p["ip"], "app", p["app"], "stream", p["stream"], "reason", reason)
			hookReply(1)
			return
		}
		s := Session{ClientID: cid, KeyID: k.ID, UserName: k.Name, Action: action, StartedAt: now(), LastSeenAt: now(), Protocol: protocolFromPayload(p, action)}
		s.IP, _ = p["ip"].(string)
		s.Vhost, _ = p["vhost"].(string)
		s.App, _ = p["app"].(string)
		s.Stream = stream
		updateSession(s)
	} else if action == "on_unpublish" || action == "on_stop" || action == "on_close" {
		removeSession(cid)
	}
	hookReply(0)
}

var hlsSegmentPattern = regexp.MustCompile(`^(.+)-[0-9]+\.ts$`)

func mediaStreamName(path string) (string, bool) {
	name := strings.TrimPrefix(path, "/live/")
	if name == path || name == "" || strings.Contains(name, "/") {
		return "", false
	}
	switch {
	case strings.HasSuffix(name, ".flv"):
		return strings.TrimSuffix(name, ".flv"), true
	case strings.HasSuffix(name, ".m3u8"):
		return strings.TrimSuffix(name, ".m3u8"), true
	case strings.HasSuffix(name, ".ts"):
		match := hlsSegmentPattern.FindStringSubmatch(name)
		if len(match) == 2 {
			return match[1], true
		}
	}
	return "", false
}

func normalizePlaybackStream(stream string) string {
	stream = strings.TrimSpace(stream)
	if strings.HasSuffix(stream, ".flv") {
		return strings.TrimSuffix(stream, ".flv")
	}
	if strings.HasSuffix(stream, ".m3u8") {
		return strings.TrimSuffix(stream, ".m3u8")
	}
	if strings.HasSuffix(stream, ".ts") {
		if match := hlsSegmentPattern.FindStringSubmatch(stream); len(match) == 2 {
			return match[1]
		}
	}
	return stream
}

func rewriteHLSPlaylist(body []byte, token string) []byte {
	if token == "" {
		return body
	}
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		u, err := url.Parse(trimmed)
		if err != nil {
			continue
		}
		q := u.Query()
		if q.Get("token") == "" {
			q.Set("token", token)
			u.RawQuery = q.Encode()
			lines[i] = u.String()
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

func mediaProxyHandler(w http.ResponseWriter, r *http.Request) {
	stream, ok := mediaStreamName(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	token := cleanToken(r.URL.Query().Get("token"))
	k, valid := validate(token, "on_play")
	if !valid || !streamAllowed(k.Stream, stream) {
		reason := "invalid_disabled_expired_or_scope"
		if token == "" {
			reason = "missing_token"
		} else if valid && k.Stream != "" && !streamAllowed(k.Stream, stream) {
			reason = "stream_mismatch"
		}
		slog.Warn("media auth rejected", "path", r.URL.Path, "stream", stream, "reason", reason)
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	protocol := "http-flv"
	if strings.HasSuffix(r.URL.Path, ".m3u8") || strings.HasSuffix(r.URL.Path, ".ts") {
		protocol = "hls"
	}
	app := r.URL.Query().Get("app")
	if app == "" {
		app = "live"
	}
	clientID := recordGatewaySession(k, "on_play", requestIP(r), app, stream, protocol)
	if protocol != "hls" {
		defer removeSession(clientID)
	}
	srsMediaProxy.ServeHTTP(w, r)
}

func rtcAuth(w http.ResponseWriter, r *http.Request, action string) bool {
	token := cleanToken(r.URL.Query().Get("token"))
	stream, streamOK := normalizeStreamName(r.URL.Query().Get("stream"))
	if token == "" {
		slog.Warn("webrtc auth rejected", "path", r.URL.Path, "reason", "missing_token", "ip", requestIP(r))
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return false
	}
	if !streamOK || stream == "" {
		slog.Warn("webrtc auth rejected", "path", r.URL.Path, "reason", "invalid_stream", "ip", requestIP(r))
		http.Error(w, "invalid stream", http.StatusBadRequest)
		return false
	}
	k, valid := validate(token, action)
	if !valid {
		slog.Warn("webrtc auth rejected", "path", r.URL.Path, "stream", stream, "reason", "invalid_disabled_expired_or_scope", "ip", requestIP(r))
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return false
	}
	if !streamAllowed(k.Stream, stream) {
		slog.Warn("webrtc auth rejected", "path", r.URL.Path, "stream", stream, "reason", "stream_mismatch", "ip", requestIP(r))
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return false
	}
	return true
}

func rtcProxyHandler(action string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			srsRTCProxy.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !rtcAuth(w, r, action) {
			return
		}
		srsRTCProxy.ServeHTTP(w, r)
	})
}

func listKeys(w http.ResponseWriter, r *http.Request) {
	if !requireAuth(w, r) {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	out := make([]Key, len(store.data.Keys))
	copy(out, store.data.Keys)
	for i := range out {
		out[i].TokenHash = ""
	}
	jsonOut(w, http.StatusOK, map[string]any{"keys": out})
}

func adminLogin(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &p); err != nil {
		slog.Warn("admin login failed", "ip", requestIP(r), "username", p.Username, "reason", "invalid_json")
		jsonOut(w, http.StatusUnauthorized, map[string]string{"error": "账号或密码错误"})
		return
	}
	current := configSnapshot()
	if subtle.ConstantTimeCompare([]byte(p.Username), []byte(current.Admin.Username)) != 1 || !passwordMatches(p.Password) {
		slog.Warn("admin login failed", "ip", requestIP(r), "username", p.Username, "reason", "invalid_credentials")
		jsonOut(w, http.StatusUnauthorized, map[string]string{"error": "账号或密码错误"})
		return
	}
	if err := sessionManager.RenewToken(r.Context()); err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"error": "会话创建失败"})
		return
	}
	sessionManager.Put(r.Context(), "username", current.Admin.Username)
	jsonOut(w, http.StatusOK, map[string]bool{"ok": true})
}

func adminMe(w http.ResponseWriter, r *http.Request) {
	if !authenticated(r) {
		jsonOut(w, http.StatusUnauthorized, map[string]bool{"ok": false})
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"ok": true, "username": configSnapshot().Admin.Username})
}

func adminLogout(w http.ResponseWriter, r *http.Request) {
	if sessionManager != nil {
		_ = sessionManager.Destroy(r.Context())
	}
	jsonOut(w, http.StatusOK, map[string]bool{"ok": true})
}
func listSessions(w http.ResponseWriter, r *http.Request) {
	if !requireAuth(w, r) {
		return
	}
	reconcileSessions()
	store.mu.Lock()
	defer store.mu.Unlock()
	raw := append([]Session(nil), store.data.Sessions...)
	aggregated := aggregateSessions(raw)
	publish, play := 0, 0
	for _, session := range aggregated {
		if session.Action == "on_publish" {
			publish++
		}
		if session.Action == "on_play" {
			play++
		}
	}
	jsonOut(w, http.StatusOK, map[string]any{"counts": map[string]int{"publish": publish, "play": play}, "sessions": aggregated})
}
func createKey(w http.ResponseWriter, r *http.Request) {
	if !requireAuth(w, r) {
		return
	}
	var p struct {
		Name      string `json:"name"`
		Scope     string `json:"scope"`
		Stream    string `json:"stream"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := readJSON(r, &p); err != nil || strings.TrimSpace(p.Name) == "" || !map[string]bool{"publish": true, "play": true, "both": true}[p.Scope] {
		jsonOut(w, 400, map[string]string{"error": "name and valid scope are required"})
		return
	}
	stream, validStream := normalizeStreamName(p.Stream)
	if !validStream {
		jsonOut(w, http.StatusBadRequest, map[string]string{"error": "stream must be at most 128 characters and cannot contain / ? # & or whitespace"})
		return
	}
	token, err := newToken()
	if err != nil {
		jsonOut(w, 500, map[string]string{"error": err.Error()})
		return
	}
	store.mu.Lock()
	id := store.data.NextID
	store.data.NextID++
	k := Key{ID: id, Name: strings.TrimSpace(p.Name), Token: token, TokenHash: tokenHash(token), Scope: p.Scope, Stream: stream, ExpiresAt: p.ExpiresAt, Enabled: true, CreatedAt: now()}
	store.data.Keys = append(store.data.Keys, k)
	err = store.saveLocked()
	store.mu.Unlock()
	if err != nil {
		jsonOut(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonOut(w, 201, map[string]any{"id": id, "name": k.Name, "scope": k.Scope, "stream": k.Stream, "expires_at": k.ExpiresAt, "token": token})
}
func toggleKey(w http.ResponseWriter, r *http.Request) {
	if !requireAuth(w, r) {
		return
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/token/keys/"), 10, 64)
	var p struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &p); err != nil {
		jsonOut(w, 400, map[string]string{"error": err.Error()})
		return
	}
	var kick []string
	store.mu.Lock()
	for i := range store.data.Keys {
		if store.data.Keys[i].ID == id {
			store.data.Keys[i].Enabled = p.Enabled
		}
	}
	if !p.Enabled {
		for _, s := range store.data.Sessions {
			for _, k := range store.data.Keys {
				if s.KeyID == id && k.ID == id {
					kick = append(kick, s.ClientID)
				}
			}
		}
	}
	_ = store.saveLocked()
	store.mu.Unlock()
	for _, clientID := range kick {
		kickClient(clientID)
	}
	jsonOut(w, 200, map[string]bool{"ok": true})
}

func kickClient(clientID string) {
	base := strings.TrimRight(configSnapshot().SRSAPI, "/")
	if base == "" {
		base = "http://srs:1985"
	}
	req, err := http.NewRequest(http.MethodGet, base+"/api/v1/clients/"+url.PathEscape(clientID)+"?method=DELETE", nil)
	if err != nil {
		return
	}
	resp, err := srsHTTPClient.Do(req)
	if err != nil {
		slog.Error("srs api request failed", "operation", "kick_client", "client_id", clientID, "error", err)
		return
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func activeSRSClients() (map[string]bool, error) {
	base := strings.TrimRight(configSnapshot().SRSAPI, "/")
	if base == "" {
		base = "http://srs:1985"
	}
	req, err := http.NewRequest(http.MethodGet, base+"/api/v1/clients/", nil)
	if err != nil {
		return nil, err
	}
	resp, err := srsHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("srs clients api http status %d", resp.StatusCode)
	}
	var result srsClientsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.Code != 0 {
		return nil, fmt.Errorf("srs clients api returned code %d", result.Code)
	}
	active := make(map[string]bool, len(result.Clients))
	for _, client := range result.Clients {
		if client.ID != "" {
			active[client.ID] = true
		}
	}
	return active, nil
}

func reconcileSessions() {
	active, err := activeSRSClients()
	if err != nil {
		slog.Warn("session reconciliation skipped", "error", err)
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current := configSnapshot()
	cutoff := now() - int64(current.Statistics.ActiveTTLSeconds)
	kept := store.data.Sessions[:0]
	removed := 0
	for _, session := range store.data.Sessions {
		lastSeen := session.LastSeenAt
		if lastSeen == 0 {
			lastSeen = session.StartedAt
		}
		if active[session.ClientID] || (session.Protocol == "hls" && lastSeen >= cutoff) {
			kept = append(kept, session)
		} else {
			removed++
		}
	}
	if removed == 0 {
		return
	}
	store.data.Sessions = kept
	if err := store.saveLocked(); err != nil {
		slog.Error("session reconciliation save failed", "error", err)
		return
	}
	slog.Info("session reconciliation removed stale sessions", "count", removed)
}

type configReloadError struct {
	RestartRequired []string
	Err             error
}

func (e *configReloadError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "configuration restart required"
}

func changedRestartFields(old, next Config) []string {
	fields := make([]string, 0)
	if old.Version != next.Version {
		fields = append(fields, "version")
	}
	if old.Admin != next.Admin {
		fields = append(fields, "admin")
	}
	if old.Session != next.Session {
		fields = append(fields, "session")
	}
	if old.SRS.HookSecret != next.SRS.HookSecret {
		fields = append(fields, "srs.hook_secret")
	}
	if old.RTMPPublicPort != next.RTMPPublicPort {
		fields = append(fields, "RTMP_PORT")
	}
	return fields
}

func changedFields(old, next Config) []string {
	fields := make([]string, 0)
	if old.PublicAddress != next.PublicAddress {
		fields = append(fields, "public.address")
	}
	if old.SRS.Candidate != next.SRS.Candidate {
		fields = append(fields, "srs.candidate")
	}
	if old.SRS.MaxConnections != next.SRS.MaxConnections {
		fields = append(fields, "srs.max_connections")
	}
	if old.SRS.LogLevel != next.SRS.LogLevel {
		fields = append(fields, "srs.log_level")
	}
	if old.Statistics != next.Statistics {
		fields = append(fields, "statistics")
	}
	return fields
}

func reloadUnifiedConfig() ([]string, []string, error) {
	configReloadMu.Lock()
	defer configReloadMu.Unlock()
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, nil, err
	}
	next, err := parseConfig(data)
	if err != nil {
		return nil, nil, err
	}
	old := configSnapshot()
	restartRequired := changedRestartFields(old, next)
	if len(restartRequired) > 0 {
		return nil, restartRequired, &configReloadError{RestartRequired: restartRequired}
	}
	reloaded := changedFields(old, next)
	srsChanged := old.SRS.Candidate != next.SRS.Candidate || old.SRS.MaxConnections != next.SRS.MaxConnections || old.SRS.LogLevel != next.SRS.LogLevel
	if srsChanged {
		previous, readErr := os.ReadFile(srsRenderedPath)
		if readErr != nil && !os.IsNotExist(readErr) {
			return nil, nil, readErr
		}
		if err := renderSRSConfig(srsTemplatePath, srsRenderedPath, next); err != nil {
			return nil, nil, err
		}
		if err := reloadSRS(); err != nil {
			if len(previous) > 0 {
				_ = writeFileAtomic(srsRenderedPath, previous, 0600)
			}
			return nil, nil, err
		}
	}
	replaceConfig(next)
	if old.Statistics.ReconcileIntervalSeconds != next.Statistics.ReconcileIntervalSeconds {
		select {
		case reconcileConfigChanged <- struct{}{}:
		default:
		}
	}
	return reloaded, nil, nil
}

func configReloadHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAuth(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	reloaded, restartRequired, err := reloadUnifiedConfig()
	if err != nil {
		status := http.StatusBadGateway
		if len(restartRequired) > 0 {
			status = http.StatusConflict
		}
		jsonOut(w, status, map[string]any{"ok": false, "error": err.Error(), "restart_required": restartRequired})
		return
	}
	jsonOut(w, http.StatusOK, map[string]any{"ok": true, "reloaded": reloaded, "restart_required": []string{}})
}

var reconcileConfigChanged = make(chan struct{}, 1)

func watchConfig() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("config watcher unavailable", "error", err)
		return
	}
	defer watcher.Close()
	directory := filepath.Dir(configPath)
	if err := watcher.Add(directory); err != nil {
		slog.Error("config watcher setup failed", "error", err)
		return
	}
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Clean(event.Name) != filepath.Clean(configPath) {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(500 * time.Millisecond)
			timerC = timer.C
		case <-timerC:
			timerC = nil
			if reloaded, restartRequired, err := reloadUnifiedConfig(); err != nil {
				slog.Warn("config hot reload rejected", "error", err, "restart_required", restartRequired)
			} else {
				slog.Info("config hot reload complete", "reloaded", reloaded)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			slog.Warn("config watcher error", "error", err)
		}
	}
}

func reloadSRS() error {
	base := strings.TrimRight(configSnapshot().SRSAPI, "/")
	if base == "" {
		base = "http://srs:1985"
	}
	req, err := http.NewRequest(http.MethodGet, base+"/api/v1/raw?rpc=reload", nil)
	if err != nil {
		return err
	}
	resp, err := srsHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("srs api http status %d", resp.StatusCode)
	}
	var result struct {
		Code int `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if result.Code != 0 {
		return fmt.Errorf("srs reload rejected with code %d", result.Code)
	}
	return nil
}

func reloadSRSHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAuth(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := reloadSRS(); err != nil {
		slog.Error("srs reload failed", "error", err)
		jsonOut(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "SRS 配置重载失败"})
		return
	}
	jsonOut(w, http.StatusOK, map[string]bool{"ok": true})
}

func deleteKey(w http.ResponseWriter, r *http.Request) {
	if !requireAuth(w, r) {
		return
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/token/keys/"), 10, 64)
	var kick []string
	store.mu.Lock()
	kept := store.data.Keys[:0]
	for _, k := range store.data.Keys {
		if k.ID != id {
			kept = append(kept, k)
		}
	}
	store.data.Keys = kept
	active := store.data.Sessions[:0]
	for _, s := range store.data.Sessions {
		if s.KeyID != id {
			active = append(active, s)
		} else {
			kick = append(kick, s.ClientID)
		}
	}
	store.data.Sessions = active
	_ = store.saveLocked()
	store.mu.Unlock()
	for _, clientID := range kick {
		kickClient(clientID)
	}
	jsonOut(w, http.StatusOK, map[string]bool{"ok": true})
}

func newSRSProxy(rawTarget string) (http.Handler, error) {
	return newSRSProxyWithModifier(rawTarget, nil)
}

func newSRSProxyWithModifier(rawTarget string, modify func(*http.Response) error) (http.Handler, error) {
	target, err := url.Parse(rawTarget)
	if err != nil {
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		if modify != nil {
			// HLS playlists must remain uncompressed so ModifyResponse can safely
			// append the playback token to each segment URL.
			req.Header.Set("Accept-Encoding", "identity")
		}
		if forwarded := req.Header.Get("X-Forwarded-For"); forwarded == "" {
			req.Header.Set("X-Forwarded-For", requestIP(req))
		}
		if realIP := req.Header.Get("X-Real-IP"); realIP == "" {
			req.Header.Set("X-Real-IP", requestIP(req))
		}
		if proto := req.Header.Get("X-Forwarded-Proto"); proto == "" {
			proto = "http"
			if req.TLS != nil {
				proto = "https"
			}
			req.Header.Set("X-Forwarded-Proto", proto)
		}
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		slog.Error("srs proxy failed", "method", req.Method, "path", req.URL.Path, "error", err)
		status := http.StatusBadGateway
		if strings.Contains(strings.ToLower(err.Error()), "timeout") {
			status = http.StatusGatewayTimeout
		}
		http.Error(w, http.StatusText(status), status)
	}
	proxy.ModifyResponse = modify
	return proxy, nil
}

func modifyHLSResponse(resp *http.Response) error {
	if resp == nil || resp.Request == nil || !strings.HasSuffix(resp.Request.URL.Path, ".m3u8") {
		return nil
	}
	token := cleanToken(resp.Request.URL.Query().Get("token"))
	if token == "" || resp.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	body = rewriteHLSPlaylist(body, token)
	resp.Body = io.NopCloser(strings.NewReader(string(body)))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	return nil
}

func publicConfig(w http.ResponseWriter, r *http.Request) {
	current := configSnapshot()
	jsonOut(w, http.StatusOK, map[string]any{
		"public_address":   current.PublicAddress,
		"rtmp_public_port": current.RTMPPublicPort,
	})
}

func requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authenticated(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requirePageSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authenticated(r) {
			http.Redirect(w, r, "/token/", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func serve() {
	dataPath := "/data/tokens.json"
	generatedPassword, preparedConfig, err := initializeConfig(configPath, configTemplatePath)
	if err != nil {
		slog.Error("initialize runtime configuration failed", "error", err)
		os.Exit(1)
	}
	if err := os.MkdirAll("/workspace/data/hls", 0755); err != nil {
		slog.Error("initialize data directory failed", "error", err)
		os.Exit(1)
	}
	if err := renderSRSConfig(srsTemplatePath, srsRenderedPath, preparedConfig); err != nil {
		slog.Error("render SRS configuration failed", "error", err)
		os.Exit(1)
	}
	if generatedPassword {
		fmt.Printf("SRS Token Gate initial admin username: %s\n", preparedConfig.Admin.Username)
		fmt.Printf("SRS Token Gate initial admin password: %s\n", preparedConfig.Admin.Password)
	}
	if err := loadConfig(configPath); err != nil {
		slog.Error("load config failed", "error", err)
		os.Exit(1)
	}
	current := configSnapshot()
	if current.Server.Listen == "" {
		current.Server.Listen = ":8085"
		replaceConfig(current)
	}
	current = configSnapshot()
	if current.Session.MaxAge <= 0 {
		current.Session.MaxAge = 86400
		replaceConfig(current)
	}
	sessionManager = scs.New()
	current = configSnapshot()
	sessionManager.Lifetime = time.Duration(current.Session.MaxAge) * time.Second
	sessionManager.Cookie.Name = "token_admin_session"
	sessionManager.Cookie.HttpOnly = true
	sessionManager.Cookie.Secure = current.Session.CookieSecure
	sessionManager.Cookie.SameSite = http.SameSiteLaxMode
	sessionManager.Cookie.Path = "/"
	current = configSnapshot()
	srsHTTPProxy, err = newSRSProxy(current.SRSHTTP)
	if err != nil {
		slog.Error("invalid SRS HTTP proxy target", "error", err)
		os.Exit(1)
	}
	srsAPIProxy, err = newSRSProxy(current.SRSAPI)
	if err != nil {
		slog.Error("invalid SRS API proxy target", "error", err)
		os.Exit(1)
	}
	srsMediaProxy, err = newSRSProxyWithModifier(current.SRSHTTP, modifyHLSResponse)
	if err != nil {
		slog.Error("invalid SRS media proxy target", "error", err)
		os.Exit(1)
	}
	srsRTCProxy, err = newSRSProxy(current.SRSAPI)
	if err != nil {
		slog.Error("invalid SRS WebRTC proxy target", "error", err)
		os.Exit(1)
	}
	store = &Store{path: dataPath}
	if err := store.load(); err != nil {
		slog.Error("load token store failed", "error", err)
		os.Exit(1)
	}
	go func() {
		for {
			interval := time.Duration(configSnapshot().Statistics.ReconcileIntervalSeconds) * time.Second
			if interval <= 0 {
				interval = 15 * time.Second
			}
			timer := time.NewTimer(interval)
			select {
			case <-timer.C:
				reconcileSessions()
			case <-reconcileConfigChanged:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
		}
	}()
	go watchConfig()
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(sessionManager.LoadAndSave)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, map[string]bool{"ok": true}) })
	r.Get("/api/token/public-config", publicConfig)
	r.Get("/internal/auth/check", func(w http.ResponseWriter, r *http.Request) {
		if authenticated(r) {
			w.WriteHeader(204)
		} else {
			w.WriteHeader(401)
		}
	})
	r.Post("/internal/srs/hook", hook)
	r.Get("/token/*", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, uiPath)
	})
	r.Get("/console/token/*", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/token/", http.StatusFound)
	})
	r.Post("/api/token/login", adminLogin)
	r.Get("/api/token/me", adminMe)
	r.Post("/api/token/logout", adminLogout)
	r.HandleFunc("/api/token/keys", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			listKeys(w, r)
		} else if r.Method == "POST" {
			createKey(w, r)
		} else {
			http.NotFound(w, r)
		}
	})
	r.Get("/api/token/sessions", listSessions)
	r.Post("/api/token/config/reload", configReloadHandler)
	r.Post("/api/token/srs/reload", reloadSRSHandler)
	r.HandleFunc("/api/token/keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			toggleKey(w, r)
		} else if r.Method == "DELETE" {
			deleteKey(w, r)
		} else {
			http.NotFound(w, r)
		}
	})
	r.Group(func(r chi.Router) {
		r.Use(requireSession)
		r.Handle("/api/v1/*", srsAPIProxy)
	})
	r.Handle("/rtc/v1/whip/*", rtcProxyHandler("on_publish"))
	r.Handle("/rtc/v1/whep/*", rtcProxyHandler("on_play"))
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		if !authenticated(r) {
			http.Redirect(w, r, "/token/", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/console/ng_index.html#/summaries", http.StatusFound)
	})
	r.Handle("/console/*", requirePageSession(srsHTTPProxy))
	r.Handle("/players/*", requirePageSession(srsHTTPProxy))
	r.Handle("/live/*", http.HandlerFunc(mediaProxyHandler))
	r.Handle("/*", requirePageSession(srsHTTPProxy))
	listen := configSnapshot().Server.Listen
	slog.Info("token-auth listening", "address", listen)
	if err := http.ListenAndServe(listen, r); err != nil {
		slog.Error("token-auth server stopped", "error", err)
		os.Exit(1)
	}
}

func healthcheck() error {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://127.0.0.1:8085/healthz")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			if err := runInit(os.Args[2:]); err != nil {
				slog.Error("initialize failed", "error", err)
				os.Exit(1)
			}
			return
		case "show-admin-password":
			if err := showAdminPassword(os.Args[2:]); err != nil {
				slog.Error("show administrator password failed", "error", err)
				os.Exit(1)
			}
			return
		case "serve":
			// Explicit service command; fall through to the HTTP server.
		case "healthcheck":
			if err := healthcheck(); err != nil {
				os.Exit(1)
			}
			return
		default:
			slog.Error("unknown command", "command", os.Args[1])
			os.Exit(2)
		}
	}
	serve()
}
