package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const validYAMLConfig = `version: 1
public:
  address: example.com
admin:
  username: admin
  password: test-password
  password_hash: ""
session:
  cookie_secure: true
  max_age_seconds: 86400
srs:
  candidate: 203.0.113.10
  hook_secret: 0123456789abcdef
  max_connections: 1000
  log_level: warn
`

func TestParseYAMLConfigStrictAndRuntimeDefaults(t *testing.T) {
	t.Setenv("RTMP_PORT", "11935")
	config, err := parseConfig([]byte(validYAMLConfig))
	if err != nil {
		t.Fatal(err)
	}
	if config.PublicAddress != "example.com" || config.RTMPPublicPort != 11935 {
		t.Fatalf("unexpected public config: address=%q port=%d", config.PublicAddress, config.RTMPPublicPort)
	}
	if config.SRSAPI != "http://srs:1985" || config.SRSHTTP != "http://srs:8080" || config.Server.Listen != ":8085" {
		t.Fatalf("unexpected internal defaults: %#v", config)
	}
}

func TestParseYAMLConfigRejectsUnknownAndInvalidValues(t *testing.T) {
	t.Setenv("RTMP_PORT", "1935")
	if _, err := parseConfig([]byte(validYAMLConfig + "obsolete_option: true\n")); err == nil || !strings.Contains(err.Error(), "field obsolete_option") {
		t.Fatalf("unknown field was not rejected: %v", err)
	}
	invalid := strings.Replace(validYAMLConfig, "candidate: 203.0.113.10", "candidate: 203.0.113.10;daemon on", 1)
	if _, err := parseConfig([]byte(invalid)); err == nil {
		t.Fatal("unsafe SRS candidate was accepted")
	}
	t.Setenv("RTMP_PORT", "70000")
	if _, err := parseConfig([]byte(validYAMLConfig)); err == nil {
		t.Fatal("invalid RTMP_PORT was accepted")
	}
}

func TestLegacyBusinessEnvironmentDoesNotOverrideYAML(t *testing.T) {
	t.Setenv("RTMP_PORT", "1935")
	t.Setenv("PUBLIC_ADDRESS", "legacy.invalid")
	t.Setenv("ADMIN_USERNAME", "legacy-admin")
	t.Setenv("SESSION_COOKIE_SECURE", "false")
	t.Setenv("SRS_HOOK_SECRET", "deadbeef")
	config, err := parseConfig([]byte(validYAMLConfig))
	if err != nil {
		t.Fatal(err)
	}
	if config.PublicAddress != "example.com" || config.Admin.Username != "admin" || !config.Session.CookieSecure || config.HookSecret != "0123456789abcdef" {
		t.Fatalf("legacy environment unexpectedly overrode YAML: %#v", config)
	}
}

func TestYAMLPasswordHashAuthentication(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("bcrypt-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	oldCfg := cfg
	cfg.Admin.Password = ""
	cfg.Admin.PasswordHash = string(hash)
	t.Cleanup(func() { cfg = oldCfg })
	if !passwordMatches("bcrypt-password") {
		t.Fatal("bcrypt password was rejected")
	}
	if passwordMatches("wrong-password") {
		t.Fatal("wrong bcrypt password was accepted")
	}
}

func TestInitializeConfigGeneratesSecretsOnceAndPreservesComments(t *testing.T) {
	t.Setenv("RTMP_PORT", "1935")
	temp := t.TempDir()
	templatePath := filepath.Join(temp, "config.example.yaml")
	configPath := filepath.Join(temp, "config.yaml")
	template := strings.Replace(validYAMLConfig, "password: test-password", "# 自动生成管理员密码。\n  password: \"\"", 1)
	template = strings.Replace(template, "hook_secret: 0123456789abcdef", "# 自动生成 Hook Secret。\n  hook_secret: \"\"", 1)
	if err := os.WriteFile(templatePath, []byte(template), 0644); err != nil {
		t.Fatal(err)
	}
	generated, first, err := initializeConfig(configPath, templatePath)
	if err != nil {
		t.Fatal(err)
	}
	if !generated || len(first.Admin.Password) != 36 || len(first.SRS.HookSecret) != 64 {
		t.Fatalf("secrets were not generated correctly: generated=%v password=%d hook=%d", generated, len(first.Admin.Password), len(first.SRS.HookSecret))
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(before, []byte("自动生成管理员密码")) || !bytes.Contains(before, []byte("自动生成 Hook Secret")) {
		t.Fatalf("YAML comments were not preserved:\n%s", before)
	}
	generated, second, err := initializeConfig(configPath, templatePath)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if generated || first.Admin.Password != second.Admin.Password || first.SRS.HookSecret != second.SRS.HookSecret || !bytes.Equal(before, after) {
		t.Fatal("second initialization changed persisted configuration")
	}
}

func TestRenderSRSConfig(t *testing.T) {
	t.Setenv("RTMP_PORT", "1935")
	config, err := parseConfig([]byte(validYAMLConfig))
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	templatePath := filepath.Join(temp, "srs.conf")
	outputPath := filepath.Join(temp, "rendered.conf")
	if err := os.WriteFile(templatePath, []byte("candidate __SRS_CANDIDATE__;\nmax __SRS_MAX_CONNECTIONS__;\nlevel __SRS_LOG_LEVEL__;\nhook __SRS_HOOK_SECRET__;\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := renderSRSConfig(templatePath, outputPath, config); err != nil {
		t.Fatal(err)
	}
	rendered, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(rendered)
	if !strings.Contains(text, "candidate 203.0.113.10;") || !strings.Contains(text, "max 1000;") || !strings.Contains(text, "level warn;") || !strings.Contains(text, "hook 0123456789abcdef;") {
		t.Fatalf("unexpected rendered SRS config:\n%s", text)
	}
}

func TestExtractToken(t *testing.T) {
	const token = "abcdefghijklmnopqrstuvwxyzABCDEFG012345678"
	tests := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{name: "normal param", payload: map[string]any{"param": "?token=" + token}, want: token},
		{name: "full rtmp url", payload: map[string]any{"tcUrl": "rtmp://example.com/live/stream?token=" + token}, want: token},
		{name: "duplicated token", payload: map[string]any{"param": "?token=" + token + "?token=" + token}, want: token},
		{name: "url encoded", payload: map[string]any{"query": "token=" + token + "%3Ftoken%3Dignored"}, want: token},
		{name: "direct token", payload: map[string]any{"token": token}, want: token},
		{name: "stream url", payload: map[string]any{"stream_url": "/live/stream?token=" + token}, want: token},
		{name: "missing", payload: map[string]any{"param": "?foo=bar"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractToken(tt.payload); got != tt.want {
				t.Fatalf("extractToken() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateRejectsInvalidTokenAndScope(t *testing.T) {
	const validToken = "valid-token"
	oldStore := store
	store = &Store{
		path: filepath.Join(t.TempDir(), "tokens.json"),
		data: StoreData{Keys: []Key{{
			ID:        1,
			Name:      "publisher",
			Token:     validToken,
			TokenHash: tokenHash(validToken),
			Scope:     "publish",
			Enabled:   true,
		}}},
	}
	t.Cleanup(func() { store = oldStore })

	if _, ok := validate("wrong-token", "on_publish"); ok {
		t.Fatal("wrong token was accepted")
	}
	if _, ok := validate(validToken, "on_play"); ok {
		t.Fatal("publish-only token was accepted for playback")
	}
	if _, ok := validate(validToken, "on_publish"); !ok {
		t.Fatal("valid publish token was rejected")
	}
	store.data.Keys[0].Enabled = false
	if _, ok := validate(validToken, "on_publish"); ok {
		t.Fatal("disabled token was accepted")
	}
}

func TestHookSecretRequired(t *testing.T) {
	oldCfg, oldStore := cfg, store
	cfg.HookSecret = "hook-secret"
	store = &Store{path: filepath.Join(t.TempDir(), "tokens.json"), data: StoreData{NextID: 1}}
	t.Cleanup(func() { cfg, store = oldCfg, oldStore })

	for _, tc := range []struct {
		name   string
		secret string
		want   string
	}{
		{name: "missing", want: `{"code":1}`},
		{name: "wrong", secret: "wrong-secret", want: `{"code":1}`},
		{name: "valid", secret: "hook-secret", want: `{"code":0}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/internal/srs/hook", strings.NewReader(`{"action":"on_stop","client_id":"hook-test"}`))
			if tc.secret != "" {
				r.URL.RawQuery = "secret=" + tc.secret
			}
			w := httptest.NewRecorder()
			hook(w, r)
			if got := strings.TrimSpace(w.Body.String()); got != tc.want {
				t.Fatalf("hook response = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMediaStreamName(t *testing.T) {
	tests := []struct {
		path string
		want string
		ok   bool
	}{
		{path: "/live/camera01.flv", want: "camera01", ok: true},
		{path: "/live/camera01.m3u8", want: "camera01", ok: true},
		{path: "/live/camera01-27.ts", want: "camera01", ok: true},
		{path: "/live/nested/camera.flv", ok: false},
		{path: "/live/camera", ok: false},
	}
	for _, tt := range tests {
		got, ok := mediaStreamName(tt.path)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("mediaStreamName(%q) = %q, %v; want %q, %v", tt.path, got, ok, tt.want, tt.ok)
		}
	}
}

func TestNormalizePlaybackStream(t *testing.T) {
	for input, want := range map[string]string{
		"stream":       "stream",
		"stream.flv":   "stream",
		"stream.m3u8":  "stream",
		"stream-27.ts": "stream",
	} {
		if got := normalizePlaybackStream(input); got != want {
			t.Fatalf("normalizePlaybackStream(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRewriteHLSPlaylist(t *testing.T) {
	playlist := []byte("#EXTM3U\nstream-1.ts?hls_ctx=abc\n/live/camera.m3u8?hls_ctx=abc&token=old\n")
	got := string(rewriteHLSPlaylist(playlist, "new-token"))
	if !strings.Contains(got, "stream-1.ts?hls_ctx=abc&token=new-token") {
		t.Fatalf("segment token was not added: %s", got)
	}
	if !strings.Contains(got, "/live/camera.m3u8?hls_ctx=abc&token=old") {
		t.Fatalf("existing token was overwritten: %s", got)
	}
}

func TestMediaProxyRequiresTokenAndStream(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("media"))
	}))
	defer backend.Close()
	proxy, err := newSRSProxy(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	oldStore, oldProxy := store, srsMediaProxy
	validToken := "media-token"
	store = &Store{
		path: filepath.Join(t.TempDir(), "tokens.json"),
		data: StoreData{Keys: []Key{{ID: 1, TokenHash: tokenHash(validToken), Token: validToken, Scope: "both", Stream: "camera01", Enabled: true}}},
	}
	srsMediaProxy = proxy
	t.Cleanup(func() { store, srsMediaProxy = oldStore, oldProxy })

	for _, tc := range []struct {
		name string
		url  string
		code int
	}{
		{name: "missing token", url: "/live/camera01.flv", code: http.StatusForbidden},
		{name: "wrong token", url: "/live/camera01.flv?token=bad", code: http.StatusForbidden},
		{name: "wrong stream", url: "/live/camera02.flv?token=" + validToken, code: http.StatusForbidden},
		{name: "valid", url: "/live/camera01.flv?token=" + validToken, code: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.url, nil)
			w := httptest.NewRecorder()
			mediaProxyHandler(w, r)
			if w.Code != tc.code {
				t.Fatalf("media status = %d, want %d; body=%s", w.Code, tc.code, w.Body.String())
			}
		})
	}
}

func TestRTCAuthRequiresTokenScopeAndStream(t *testing.T) {
	oldStore := store
	validToken := "rtc-token"
	store = &Store{
		path: filepath.Join(t.TempDir(), "tokens.json"),
		data: StoreData{Keys: []Key{{ID: 1, TokenHash: tokenHash(validToken), Token: validToken, Scope: "both", Stream: "camera01", Enabled: true}}},
	}
	t.Cleanup(func() { store = oldStore })

	for _, tc := range []struct {
		name   string
		path   string
		action string
		code   int
	}{
		{name: "missing token", path: "/rtc/v1/whep/?app=live&stream=camera01", action: "on_play", code: http.StatusForbidden},
		{name: "wrong token", path: "/rtc/v1/whep/?app=live&stream=camera01&token=bad", action: "on_play", code: http.StatusForbidden},
		{name: "wrong stream", path: "/rtc/v1/whep/?app=live&stream=camera02&token=" + validToken, action: "on_play", code: http.StatusForbidden},
		{name: "valid playback", path: "/rtc/v1/whep/?app=live&stream=camera01&token=" + validToken, action: "on_play", code: 0},
		{name: "valid publish", path: "/rtc/v1/whip/?app=live&stream=camera01&token=" + validToken, action: "on_publish", code: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.path, nil)
			w := httptest.NewRecorder()
			if ok := rtcAuth(w, r, tc.action); tc.code == 0 {
				if !ok {
					t.Fatalf("rtcAuth rejected valid request: %s", w.Body.String())
				}
			} else if ok || w.Code != tc.code {
				t.Fatalf("rtcAuth result = %v status=%d, want rejected status=%d", ok, w.Code, tc.code)
			}
		})
	}
}

func TestRTCProxyForwardsAuthenticatedRequest(t *testing.T) {
	validToken := "rtc-proxy-token"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rtc/v1/whep/" || r.URL.Query().Get("stream") != "camera01" {
			t.Fatalf("unexpected WebRTC proxy request: %s", r.URL.String())
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer backend.Close()
	proxy, err := newSRSProxy(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	oldStore, oldProxy := store, srsRTCProxy
	store = &Store{
		path: filepath.Join(t.TempDir(), "tokens.json"),
		data: StoreData{Keys: []Key{{ID: 1, TokenHash: tokenHash(validToken), Token: validToken, Scope: "play", Stream: "camera01", Enabled: true}}},
	}
	srsRTCProxy = proxy
	t.Cleanup(func() { store, srsRTCProxy = oldStore, oldProxy })

	r := httptest.NewRequest(http.MethodPost, "/rtc/v1/whep/?app=live&stream=camera01&token="+validToken, strings.NewReader("offer"))
	w := httptest.NewRecorder()
	rtcProxyHandler("on_play").ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("WebRTC proxy status = %d, want %d", w.Code, http.StatusCreated)
	}
}

func TestRequirePageSessionRedirectsUnauthenticatedUsers(t *testing.T) {
	oldCfg, oldSessionManager := cfg, sessionManager
	cfg.Admin.Username = "admin"
	cfg.Admin.Password = "page-password"
	cfg.Admin.PasswordHash = ""
	sessionManager = nil
	t.Cleanup(func() { cfg, sessionManager = oldCfg, oldSessionManager })

	handler := requirePageSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	unauthenticated := httptest.NewRequest(http.MethodGet, "/console/ng_index.html", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusFound || unauthenticatedResponse.Header().Get("Location") != "/token/" {
		t.Fatalf("unauthenticated page response = %d location=%q", unauthenticatedResponse.Code, unauthenticatedResponse.Header().Get("Location"))
	}

	authenticatedRequest := httptest.NewRequest(http.MethodGet, "/console/ng_index.html", nil)
	authenticatedRequest.SetBasicAuth("admin", "page-password")
	authenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(authenticatedResponse, authenticatedRequest)
	if authenticatedResponse.Code != http.StatusNoContent {
		t.Fatalf("authenticated page response = %d, want %d", authenticatedResponse.Code, http.StatusNoContent)
	}
}

func TestReconcileSessions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/clients/" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"clients":[{"id":"active-client"}]}`))
	}))
	defer server.Close()

	oldCfg, oldStore := cfg, store
	cfg.SRSAPI = server.URL
	store = &Store{
		path: filepath.Join(t.TempDir(), "tokens.json"),
		data: StoreData{Sessions: []Session{{ClientID: "active-client"}, {ClientID: "stale-client"}}},
	}
	t.Cleanup(func() { cfg, store = oldCfg, oldStore })

	reconcileSessions()
	if len(store.data.Sessions) != 1 || store.data.Sessions[0].ClientID != "active-client" {
		t.Fatalf("stale sessions were not removed: %#v", store.data.Sessions)
	}
}

func TestStreamRestriction(t *testing.T) {
	if !streamAllowed("", "anything") {
		t.Fatal("empty stream restriction should allow any stream")
	}
	if !streamAllowed("camera01", "camera01") {
		t.Fatal("matching stream restriction was rejected")
	}
	if streamAllowed("camera01", "camera02") {
		t.Fatal("mismatched stream restriction was accepted")
	}
	if _, ok := normalizeStreamName("camera/01"); ok {
		t.Fatal("unsafe stream name was accepted")
	}
	if _, ok := normalizeStreamName(strings.Repeat("a", 129)); ok {
		t.Fatal("overlong stream name was accepted")
	}
}

func TestAggregateSessionsMergesProtocolsAndCounts(t *testing.T) {
	nowValue := now()
	raw := []Session{
		{ClientID: "rtmp-1", KeyID: 7, UserName: "viewer", Action: "on_play", IP: "203.0.113.10", App: "live", Stream: "camera01", Protocol: "http-flv", StartedAt: nowValue - 20, LastSeenAt: nowValue - 1},
		{ClientID: "hls-1", KeyID: 7, UserName: "viewer", Action: "on_play", IP: "203.0.113.10", App: "live", Stream: "camera01", Protocol: "hls", StartedAt: nowValue - 10, LastSeenAt: nowValue},
		{ClientID: "other-ip", KeyID: 7, UserName: "viewer", Action: "on_play", IP: "203.0.113.11", App: "live", Stream: "camera01", Protocol: "hls", StartedAt: nowValue, LastSeenAt: nowValue},
		{ClientID: "pub-1", KeyID: 7, UserName: "publisher", Action: "on_publish", IP: "203.0.113.10", App: "live", Stream: "camera01", Protocol: "rtmp", StartedAt: nowValue, LastSeenAt: nowValue},
	}
	groups := aggregateSessions(raw)
	if len(groups) != 3 {
		t.Fatalf("got %d aggregate groups, want 3: %#v", len(groups), groups)
	}
	for _, group := range groups {
		if group.Action == "on_play" && group.IP == "203.0.113.10" {
			if group.ClientCount != 2 || len(group.Protocols) != 2 {
				t.Fatalf("merged group = %#v", group)
			}
		}
	}
}

func TestChangedRestartFields(t *testing.T) {
	old, next := Config{}, Config{}
	old.Admin.Username = "admin"
	next.Admin.Username = "new-admin"
	old.SRS.HookSecret = "old"
	next.SRS.HookSecret = "new"
	fields := changedRestartFields(old, next)
	if len(fields) != 2 || fields[0] != "admin" || fields[1] != "srs.hook_secret" {
		t.Fatalf("restart fields = %#v", fields)
	}
}

func TestSRSAPITimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"code":0,"clients":[]}`))
	}))
	defer server.Close()

	oldCfg, oldClient := cfg, srsHTTPClient
	cfg.SRSAPI = server.URL
	srsHTTPClient = &http.Client{Timeout: 10 * time.Millisecond}
	t.Cleanup(func() { cfg, srsHTTPClient = oldCfg, oldClient })

	if _, err := activeSRSClients(); err == nil {
		t.Fatal("expected SRS API timeout")
	}
}

func TestAdminLoginFailureLogDoesNotContainPassword(t *testing.T) {
	oldCfg, oldWriter := cfg, log.Writer()
	cfg.Admin.Username = "admin"
	cfg.Admin.Password = "correct-password"
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { cfg = oldCfg; log.SetOutput(oldWriter) })

	r := httptest.NewRequest(http.MethodPost, "/api/token/login", strings.NewReader(`{"username":"admin","password":"wrong-password"}`))
	r.RemoteAddr = "203.0.113.10:4567"
	w := httptest.NewRecorder()
	adminLogin(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("login status = %d, want 401", w.Code)
	}
	if !strings.Contains(logs.String(), "admin login failed ip=203.0.113.10") {
		t.Fatalf("missing login failure log: %s", logs.String())
	}
	if strings.Contains(logs.String(), "wrong-password") || strings.Contains(logs.String(), "correct-password") {
		t.Fatalf("password appeared in login log: %s", logs.String())
	}
}
