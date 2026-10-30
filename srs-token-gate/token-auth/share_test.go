package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func shareFixture(t *testing.T) (http.Handler, *time.Time) {
	t.Helper()
	oldStore, oldShares, oldCfg, oldProxy := store, shares, cfg, srsMediaProxy
	clock := time.Unix(1800000000, 0)
	shares = newShareState()
	shares.clock = func() time.Time { return clock }
	store = &Store{path: filepath.Join(t.TempDir(), "tokens.json"), data: StoreData{
		NextID: 2, NextRoomID: 2,
		Rooms: []Room{{ID: 1, Name: "huiyi-01", KeyID: 1}},
		Keys:  []Key{{ID: 1, Token: "private-publish-token", TokenHash: tokenHash("private-publish-token"), Scope: "both", Stream: "huiyi-01", Enabled: true}},
	}}
	cfg.Admin.Username, cfg.Admin.Password, cfg.Admin.PasswordHash = "admin", "test-password", ""
	cfg.HookSecret = "hook-test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/streams/":
			fmt.Fprint(w, `{"code":0,"server":"srs","service":"service-a","streams":[{"name":"huiyi-01","app":"live","publish":{"active":true,"cid":"publisher-1"}}]}`)
		case "/live/huiyi-01.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:2,\nhuiyi-01-1.ts?hls_ctx=abc\n")
		default:
			fmt.Fprint(w, "segment")
		}
	}))
	cfg.SRSAPI = server.URL
	var err error
	srsMediaProxy, err = newSRSProxyWithModifier(server.URL, modifyHLSResponse)
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	registerShareRoutes(r)
	r.HandleFunc("/live/*", mediaProxyHandler)
	r.Post("/internal/srs/hook", hook)
	r.Patch("/api/token/rooms/{id}", toggleRoom)
	r.Patch("/api/token/keys/{id}", toggleKey)
	r.Handle("/console/*", requirePageSession(http.NotFoundHandler()))
	r.Handle("/players/*", requirePageSession(http.NotFoundHandler()))
	r.Handle("/api/v1/*", requireSession(http.NotFoundHandler()))
	t.Cleanup(func() { server.Close(); store, shares, cfg, srsMediaProxy = oldStore, oldShares, oldCfg, oldProxy })
	return r, &clock
}

func shareRequest(r http.Handler, method, path, body string, admin bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if admin {
		req.SetBasicAuth("admin", "test-password")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func createTestShare(t *testing.T, r http.Handler) (string, string) {
	t.Helper()
	w := shareRequest(r, "POST", "/api/token/rooms/1/share", "", true)
	if w.Code != 200 {
		t.Fatalf("create status=%d: %s", w.Code, w.Body.String())
	}
	var view struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(view.Path, "/watch/huiyi-01/share_") {
		t.Fatalf("unexpected share path: %s", view.Path)
	}
	return view.Path, strings.TrimPrefix(view.Path, "/watch/huiyi-01/")
}

func TestSharePermissionAndMedia(t *testing.T) {
	r, _ := shareFixture(t)
	for _, method := range []string{"GET", "POST", "PATCH"} {
		w := shareRequest(r, method, "/api/token/rooms/1/share", `{"enabled":true}`, false)
		if w.Code != 401 {
			t.Fatalf("unauthorized %s = %d", method, w.Code)
		}
	}
	path, token := createTestShare(t, r)
	path2, _ := createTestShare(t, r)
	if path2 != path {
		t.Fatal("repeated generation changed the link")
	}
	for _, route := range []string{path, "/watch-assets/watch.js", "/watch-assets/watch.css", "/watch-assets/hls.min.js"} {
		if w := shareRequest(r, "GET", route, "", false); w.Code != 200 {
			t.Fatalf("public page %s = %d", route, w.Code)
		}
	}
	status := shareRequest(r, "GET", path+"/status", "", false)
	if status.Code != 200 || !strings.Contains(status.Body.String(), "media_url") || strings.Contains(status.Body.String(), "private-publish-token") {
		t.Fatalf("unsafe/invalid share status: %s", status.Body.String())
	}
	playlist := shareRequest(r, "GET", "/live/huiyi-01.m3u8?token="+token, "", false)
	if playlist.Code != 200 || !strings.Contains(playlist.Body.String(), "token="+token) || playlist.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("playlist auth/propagation failed: %d %s", playlist.Code, playlist.Body.String())
	}
	if w := shareRequest(r, "GET", "/live/huiyi-01-1.ts?hls_ctx=abc&token="+token, "", false); w.Code != 200 {
		t.Fatalf("segment status=%d", w.Code)
	}
	for _, url := range []string{"/live/huiyi-01.m3u8", "/live/huiyi-01-1.ts?hls_ctx=abc", "/live/huiyi-01.m3u8?token=bad", "/live/other.m3u8?token=" + token, "/live/other-1.ts?token=" + token, "/live/huiyi-01.flv?token=" + token, "/live/huiyi-01.m3u8?app=other&token=" + token, "/live/huiyi-01.m3u8?vhost=other&token=" + token} {
		if w := shareRequest(r, "GET", url, "", false); w.Code != 403 {
			t.Fatalf("bypass %s = %d", url, w.Code)
		}
	}
	for _, url := range []string{"/console/ng_index.html", "/players/whep.html"} {
		if w := shareRequest(r, "GET", url, "", false); w.Code != 302 {
			t.Fatalf("protected page %s = %d", url, w.Code)
		}
	}
	if w := shareRequest(r, "GET", "/api/v1/streams/", "", false); w.Code != 401 {
		t.Fatal("management API exposed")
	}
	if _, ok := validateStream(token, "on_publish", "live", "huiyi-01"); ok {
		t.Fatal("share allowed publishing")
	}
	if _, ok := validateStream(token, "on_play", "other-app", "huiyi-01"); ok {
		t.Fatal("share allowed another app")
	}
	for _, action := range []string{"on_play", "on_publish"} {
		body := fmt.Sprintf(`{"action":%q,"client_id":"test-viewer","app":"live","vhost":"__defaultVhost__","stream":"huiyi-01","param":"?token=%s"}`, action, token)
		w := shareRequest(r, "POST", "/internal/srs/hook?secret=hook-test-secret", body, false)
		want := `{"code":0}`
		if action == "on_publish" {
			want = `{"code":1}`
		}
		if w.Body.String() != want {
			t.Fatalf("hook %s = %s", action, w.Body.String())
		}
	}
	if len(aggregateSessionsWithRooms(store.data.Sessions, store.data.Rooms)) == 0 {
		t.Fatal("share viewers missing from room statistics")
	}
	for _, key := range store.data.Keys {
		if key.Token == token {
			t.Fatal("temporary credential persisted as permanent key")
		}
	}
}

func TestShareCloseEnableAndInvalidation(t *testing.T) {
	r, _ := shareFixture(t)
	path, token := createTestShare(t, r)
	for _, enabled := range []bool{false, true} {
		w := shareRequest(r, "PATCH", "/api/token/rooms/1/share", fmt.Sprintf(`{"enabled":%t}`, enabled), true)
		if w.Code != 200 {
			t.Fatalf("toggle = %d", w.Code)
		}
		_, allowed := validateStream(token, "on_play", "live", "huiyi-01")
		if allowed != enabled {
			t.Fatal("share toggle did not control playback")
		}
		if !strings.Contains(w.Body.String(), path) {
			t.Fatal("toggle changed share link")
		}
	}
	req := httptest.NewRequest("PATCH", "/api/token/rooms/1/share", strings.NewReader(`{"enabled":false}`))
	req.SetBasicAuth("admin", "test-password")
	req.Header.Set("Origin", "https://foreign.example")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("cross origin mutation accepted")
	}
	if w := shareRequest(r, "PATCH", "/api/token/rooms/1/share", `{}`, true); w.Code != 400 {
		t.Fatal("missing enabled accepted")
	}
	shareRequest(r, "PATCH", "/api/token/keys/1", `{"enabled":false}`, true)
	shareRequest(r, "PATCH", "/api/token/keys/1", `{"enabled":true}`, true)
	if w := shareRequest(r, "GET", path+"/status", "", false); w.Code != 410 {
		t.Fatal("room token re-enable revived expired share")
	}
	path, token = createTestShare(t, r)
	store.data.Keys[0].ExpiresAt = now() - 1
	if _, ok := validateStream(token, "on_play", "live", "huiyi-01"); ok {
		t.Fatal("expired room token accepted")
	}
	store.data.Keys[0].ExpiresAt = 0
	if w := shareRequest(r, "GET", path+"/status", "", false); w.Code != 410 {
		t.Fatal("credential expiry revived share")
	}
	path, _ = createTestShare(t, r)
	shares = newShareState()
	if w := shareRequest(r, "GET", path+"/status", "", false); w.Code != 410 {
		t.Fatal("restart retained share")
	}
}

func TestShareReconnectGraceAndOutOfOrderCallbacks(t *testing.T) {
	r, clock := shareFixture(t)
	path, token := createTestShare(t, r)
	shares.stop("publisher-1")
	disconnected := shares.rooms[1].Disconnected
	*clock = clock.Add(29 * time.Second)
	shareRequest(r, "PATCH", "/api/token/rooms/1/share", `{"enabled":false}`, true)
	shareRequest(r, "PATCH", "/api/token/rooms/1/share", `{"enabled":true}`, true)
	if shares.rooms[1].Disconnected != disconnected {
		t.Fatal("share toggle reset grace timer")
	}
	p := publisher{ClientID: "publisher-2", App: "live", Stream: "huiyi-01"}
	shares.publish(p)
	shares.stop("publisher-1")
	shares.applyCheck(map[string]publisher{p.Stream: p}, "srs/service-a", shares.revision, nil)
	if shares.rooms[1].ID != token || shares.statusLocked(shares.rooms[1]) != "playing" {
		t.Fatal("grace reconnect or late stop failed")
	}
	oldRevision := shares.revision
	shares.publish(publisher{ClientID: "publisher-3", App: "live", Stream: p.Stream})
	shares.applyCheck(map[string]publisher{}, "srs/service-a", oldRevision, nil)
	if !shares.rooms[1].Disconnected.IsZero() {
		t.Fatal("stale API result stopped new publisher")
	}
	shares.stop("publisher-3")
	*clock = clock.Add(30 * time.Second)
	shares.publish(publisher{ClientID: "publisher-4", App: "live", Stream: p.Stream})
	if w := shareRequest(r, "GET", path+"/status", "", false); w.Code != 410 {
		t.Fatal("late reconnect revived expired link")
	}
	if _, ok := validateStream(token, "on_play", "live", p.Stream); ok {
		t.Fatal("expired share token accepted")
	}
}

func TestShareReconciliationFailsClosed(t *testing.T) {
	r, clock := shareFixture(t)
	_, token := createTestShare(t, r)
	shares.applyCheck(nil, "", shares.revision, errors.New("timeout"))
	if _, ok := validateStream(token, "on_play", "live", "huiyi-01"); ok {
		t.Fatal("API error allowed playback")
	}
	p := publisher{ClientID: "publisher-1", App: "live", Stream: "huiyi-01"}
	shares.applyCheck(map[string]publisher{p.Stream: p}, "srs/service-a", shares.revision, nil)
	*clock = clock.Add(10 * time.Second)
	if _, ok := validateStream(token, "on_play", "live", p.Stream); ok {
		t.Fatal("stale API snapshot allowed playback")
	}
	shares.applyCheck(map[string]publisher{}, "srs/service-a", shares.revision, nil)
	if shares.statusLocked(shares.rooms[1]) != "reconnecting" {
		t.Fatal("missing unpublish was not detected")
	}
	*clock = clock.Add(30 * time.Second)
	shares.applyCheck(map[string]publisher{p.Stream: p}, "srs/service-a", shares.revision, nil)
	if len(shares.rooms) != 0 {
		t.Fatal("missing callback grace never expired")
	}
	_, token = createTestShare(t, r)
	shares.applyCheck(map[string]publisher{p.Stream: p}, "srs/service-b", shares.revision, nil)
	if _, ok := validateStream(token, "on_play", "live", p.Stream); ok {
		t.Fatal("SRS restart retained share")
	}
}

func TestShareNeedsActivePublisherAndRoom(t *testing.T) {
	r, _ := shareFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"code":0,"streams":[]}`) }))
	defer server.Close()
	cfg.SRSAPI = server.URL
	if w := shareRequest(r, "POST", "/api/token/rooms/1/share", "", true); w.Code != 409 {
		t.Fatalf("offline share status=%d", w.Code)
	}
	if w := shareRequest(r, "POST", "/api/token/rooms/9/share", "", true); w.Code != 409 {
		t.Fatal("missing room accepted")
	}
}

func TestShareMissedCallbacksAndLongOutage(t *testing.T) {
	r, clock := shareFixture(t)
	path, token := createTestShare(t, r)
	shares.applyCheck(map[string]publisher{}, "srs/service-a", shares.revision, nil)
	*clock = clock.Add(20 * time.Second)
	p := publisher{ClientID: "reconnected", App: "live", Stream: "huiyi-01"}
	shares.applyCheck(map[string]publisher{p.Stream: p}, "srs/service-a", shares.revision, nil)
	if shares.rooms[1].ID != token || shares.statusLocked(shares.rooms[1]) != "playing" {
		t.Fatal("missed reconnect callback did not recover within grace")
	}
	shares.applyCheck(nil, "", shares.revision, errors.New("unavailable"))
	*clock = clock.Add(31 * time.Second)
	shares.applyCheck(map[string]publisher{p.Stream: p}, "srs/service-a", shares.revision, nil)
	if w := shareRequest(r, "GET", path+"/status", "", false); w.Code != 410 {
		t.Fatal("long monitoring outage revived old share")
	}
}

func TestActivePublishersPaginationAndAmbiguity(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		streams := []map[string]any{}
		if r.URL.Query().Get("start") == "0" {
			for i := 0; i < 100; i++ {
				streams = append(streams, map[string]any{"name": fmt.Sprintf("room-%d", i), "app": "live", "publish": map[string]any{"active": true, "cid": fmt.Sprintf("cid-%d", i)}})
			}
		} else {
			streams = append(streams, map[string]any{"name": "room-0", "app": "live", "publish": map[string]any{"active": true, "cid": "other-vhost"}})
			streams = append(streams, map[string]any{"name": "last-room", "app": "live", "publish": map[string]any{"active": true, "cid": "last-cid"}})
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "server": "server", "service": "service", "streams": streams})
	}))
	defer server.Close()
	cfg.SRSAPI = server.URL
	items, _, err := activePublishers()
	if err != nil || pages != 2 || len(items) != 100 || items["last-room"].ClientID != "last-cid" {
		t.Fatalf("pagination failed: pages=%d count=%d error=%v", pages, len(items), err)
	}
	if _, ok := items["room-0"]; ok {
		t.Fatal("ambiguous vhost stream accepted")
	}
}
