package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

const shareGrace = 30 * time.Second
const sharePollInterval = 5 * time.Second

//go:embed ui/watch.html ui/watch.js ui/watch.css ui/hls.min.js ui/hls.LICENSE
var watchFiles embed.FS

type publisher struct {
	ClientID string
	App      string
	Stream   string
}

type liveShare struct {
	ID           string
	RoomID       int64
	Name         string
	Publisher    publisher
	Enabled      bool
	Disconnected time.Time
}

type shareState struct {
	mu         sync.Mutex
	checkMu    sync.Mutex
	clock      func() time.Time
	rooms      map[int64]*liveShare
	publishers map[string]publisher
	checkedAt  time.Time
	healthy    bool
	instance   string
	revision   uint64
}

func newShareState() *shareState {
	return &shareState{clock: time.Now, rooms: make(map[int64]*liveShare), publishers: make(map[string]publisher)}
}

var shares = newShareState()

func isShareToken(token string) bool {
	return len(token) == 49 && strings.HasPrefix(token, "share_")
}

func (s *shareState) expireLocked() {
	for id, share := range s.rooms {
		if !share.Disconnected.IsZero() && s.clock().Sub(share.Disconnected) >= shareGrace {
			delete(s.rooms, id)
		}
	}
}

func (s *shareState) publish(p publisher) {
	if p.App != "live" || p.ClientID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	s.revision++
	s.publishers[p.Stream] = p
	for _, share := range s.rooms {
		if share.Name == p.Stream && share.Publisher.App == p.App {
			share.Publisher = p
			share.Disconnected = time.Time{}
		}
	}
}

func (s *shareState) stop(clientID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for name, p := range s.publishers {
		if p.ClientID == clientID {
			delete(s.publishers, name)
			changed = true
		}
	}
	for _, share := range s.rooms {
		if share.Publisher.ClientID == clientID && share.Disconnected.IsZero() {
			share.Disconnected = s.clock()
			changed = true
		}
	}
	if changed {
		s.revision++
	}
}

func (s *shareState) endRoom(id int64) {
	s.mu.Lock()
	delete(s.rooms, id)
	s.mu.Unlock()
}

func (s *shareState) applyCheck(publishers map[string]publisher, instance string, revision uint64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	if err != nil {
		s.healthy = false
		return
	}
	// A slow API response must not undo a newer publish/unpublish callback.
	if revision != s.revision {
		return
	}
	if (!s.checkedAt.IsZero() && s.clock().Sub(s.checkedAt) >= shareGrace) || (s.instance != "" && s.instance != instance) {
		s.rooms = make(map[int64]*liveShare)
	}
	s.checkedAt, s.healthy, s.instance = s.clock(), true, instance
	s.publishers = publishers
	for _, share := range s.rooms {
		p, active := publishers[share.Name]
		if active {
			// Reconciliation also discovers reconnects whose callback was missed.
			share.Publisher = p
			share.Disconnected = time.Time{}
		} else if share.Disconnected.IsZero() {
			share.Disconnected = s.clock()
		}
	}
}

func activePublishers() (map[string]publisher, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(map[string]publisher)
	ambiguous := make(map[string]bool)
	instance := ""
	for start := 0; ; start += 100 {
		u := strings.TrimRight(configSnapshot().SRSAPI, "/") + "/api/v1/streams/?start=" + strconv.Itoa(start) + "&count=100"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, "", err
		}
		resp, err := srsHTTPClient.Do(req)
		if err != nil {
			return nil, "", err
		}
		var page struct {
			Code    int    `json:"code"`
			Server  string `json:"server"`
			Service string `json:"service"`
			Streams []struct {
				Name    string `json:"name"`
				App     string `json:"app"`
				Publish struct {
					Active bool   `json:"active"`
					CID    string `json:"cid"`
				} `json:"publish"`
			} `json:"streams"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || page.Code != 0 || page.Streams == nil {
			return nil, "", fmt.Errorf("invalid SRS streams response (HTTP %d)", resp.StatusCode)
		}
		pageInstance := page.Server + "/" + page.Service
		if instance != "" && instance != pageInstance {
			return nil, "", fmt.Errorf("SRS restarted during stream listing")
		}
		instance = pageInstance
		for _, stream := range page.Streams {
			if stream.App == "live" && stream.Publish.Active && stream.Publish.CID != "" {
				if _, exists := result[stream.Name]; exists {
					ambiguous[stream.Name] = true
				}
				result[stream.Name] = publisher{ClientID: stream.Publish.CID, App: stream.App, Stream: stream.Name}
			}
		}
		if len(page.Streams) < 100 {
			break
		}
	}
	for name := range ambiguous {
		delete(result, name)
	}
	return result, instance, nil
}

func (s *shareState) check() {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	s.mu.Lock()
	revision := s.revision
	s.mu.Unlock()
	publishers, instance, err := activePublishers()
	s.applyCheck(publishers, instance, revision, err)
	if err != nil {
		slog.Warn("share playback paused", "reason", "SRS stream check failed")
	}
}

func (s *shareState) statusLocked(share *liveShare) string {
	if share == nil {
		return "ended"
	}
	if !share.Enabled {
		return "closed"
	}
	if !s.healthy || s.clock().Sub(s.checkedAt) >= 2*sharePollInterval {
		return "unavailable"
	}
	if !share.Disconnected.IsZero() {
		return "reconnecting"
	}
	return "playing"
}

func shareRoomEnabledLocked(id int64) (Room, bool) {
	room, ok := roomByID(store.data.Rooms, id)
	if !ok {
		return Room{}, false
	}
	key, ok := keyByID(store.data.Keys, room.KeyID)
	return room, ok && key.Enabled && (key.ExpiresAt == 0 || key.ExpiresAt >= now())
}

// Keep the store -> share lock order everywhere; share credentials never enter tokens.json.
func validateStream(token, action, app, stream string) (Key, bool) {
	if !isShareToken(token) {
		key, ok := validate(token, action)
		return key, ok && streamAllowed(key.Stream, stream)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	shares.mu.Lock()
	defer shares.mu.Unlock()
	shares.expireLocked()
	for id, share := range shares.rooms {
		if share.ID != token {
			continue
		}
		if _, ok := shareRoomEnabledLocked(id); !ok {
			delete(shares.rooms, id)
			return Key{}, false
		}
		if action != "on_play" || app != share.Publisher.App || stream != share.Name || shares.statusLocked(share) != "playing" {
			return Key{}, false
		}
		return Key{ID: -id, Name: "分享观看 · " + share.Name, Scope: "play", Stream: share.Name, Enabled: true}, true
	}
	return Key{}, false
}

func shareView(share *liveShare, status string) map[string]any {
	view := map[string]any{"status": status, "enabled": false}
	if share != nil {
		view["enabled"] = share.Enabled
		view["path"] = "/watch/" + share.Name + "/" + share.ID
		view["room"] = share.Name
	}
	return view
}

func manageShare(w http.ResponseWriter, r *http.Request) {
	if !requireAuth(w, r) {
		return
	}
	if r.Method != http.MethodGet && r.Header.Get("Origin") != "" {
		origin, err := url.Parse(r.Header.Get("Origin"))
		if err != nil || origin.Host != r.Host {
			jsonOut(w, 403, map[string]string{"error": "请在控制台中操作分享"})
			return
		}
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		jsonOut(w, 400, map[string]string{"error": "无效房间"})
		return
	}
	var patch struct {
		Enabled *bool `json:"enabled"`
	}
	if r.Method == http.MethodPatch && (readJSON(r, &patch) != nil || patch.Enabled == nil) {
		jsonOut(w, 400, map[string]string{"error": "需要指定分享开关"})
		return
	}
	if r.Method == http.MethodPost {
		shares.check()
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	shares.mu.Lock()
	defer shares.mu.Unlock()
	room, enabled := shareRoomEnabledLocked(id)
	if !enabled {
		delete(shares.rooms, id)
		jsonOut(w, 409, map[string]string{"error": "房间不存在、已停用或 Token 已失效"})
		return
	}
	shares.expireLocked()
	share := shares.rooms[id]
	if r.Method == http.MethodPost && share == nil {
		p, active := shares.publishers[room.Name]
		if !shares.healthy || shares.clock().Sub(shares.checkedAt) >= 2*sharePollInterval {
			jsonOut(w, 503, map[string]string{"error": "暂时无法确认推流状态，请稍后重试"})
			return
		}
		if !active {
			jsonOut(w, 409, map[string]string{"error": "请先向此房间推流，再生成分享"})
			return
		}
		token, err := newToken()
		if err != nil {
			jsonOut(w, 500, map[string]string{"error": "分享生成失败"})
			return
		}
		share = &liveShare{ID: "share_" + token, RoomID: id, Name: room.Name, Publisher: p, Enabled: true}
		shares.rooms[id] = share
	}
	if r.Method == http.MethodPatch {
		if share == nil {
			jsonOut(w, 410, map[string]string{"error": "分享已结束，请在推流后重新生成"})
			return
		}
		share.Enabled = *patch.Enabled
	}
	jsonOut(w, 200, shareView(share, shares.statusLocked(share)))
}

func publicShareStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	store.mu.Lock()
	defer store.mu.Unlock()
	shares.mu.Lock()
	defer shares.mu.Unlock()
	shares.expireLocked()
	for id, share := range shares.rooms {
		if share.Name != chi.URLParam(r, "room") || share.ID != chi.URLParam(r, "shareID") {
			continue
		}
		if _, ok := shareRoomEnabledLocked(id); !ok {
			delete(shares.rooms, id)
			break
		}
		status := shares.statusLocked(share)
		view := map[string]any{"status": status, "room": share.Name}
		if status == "playing" {
			view["media_url"] = "/live/" + share.Name + ".m3u8?token=" + url.QueryEscape(share.ID)
		}
		jsonOut(w, 200, view)
		return
	}
	jsonOut(w, 410, map[string]string{"status": "ended"})
}

func registerShareRoutes(r chi.Router) {
	r.Get("/api/token/rooms/{id}/share", manageShare)
	r.Post("/api/token/rooms/{id}/share", manageShare)
	r.Patch("/api/token/rooms/{id}/share", manageShare)
	r.Get("/watch/{room}/{shareID}/status", publicShareStatus)
	r.Get("/watch/{room}/{shareID}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; media-src 'self' blob:; connect-src 'self'; worker-src blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		http.ServeFileFS(w, r, watchFiles, "ui/watch.html")
	})
	assets, _ := fs.Sub(watchFiles, "ui")
	for _, name := range []string{"watch.js", "watch.css", "hls.min.js"} {
		r.Handle("/watch-assets/"+name, http.StripPrefix("/watch-assets/", http.FileServerFS(assets)))
	}
}
