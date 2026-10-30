(function () {
    "use strict";
    var video = document.getElementById("video");
    var status = document.getElementById("status");
    var play = document.getElementById("play");
    var sound = document.getElementById("sound");
    var endpoint = location.pathname.replace(/\/$/, "") + "/status";
    var roomName = location.pathname.split("/")[2] || "";
    if (/^[a-z0-9][a-z0-9_-]{0,63}$/.test(roomName)) {
        document.getElementById("room").textContent = roomName;
        document.title = roomName + " · 直播观看";
    }
    var hls = null, source = "", timer = null, stopped = false, checking = false;
    var messages = {playing:"播放中", reconnecting:"推流已断开，等待重连…", closed:"分享已关闭", ended:"直播已结束，分享链接已失效", unavailable:"直播服务暂时不可用，正在重试…"};

    function clearPlayer() {
        source = "";
        if (hls) { hls.destroy(); hls = null; }
        video.pause();
        video.removeAttribute("src");
        video.load();
        play.hidden = true;
    }
    function tryPlay() {
        var pending = video.play();
        if (pending) pending.then(function () { play.hidden = true; }).catch(function () {
            if (source) { play.hidden = false; status.textContent = "点击播放即可观看"; }
        });
    }
    function startPlayer(url) {
        if (source === url) return;
        clearPlayer();
        source = url;
        if (video.canPlayType("application/vnd.apple.mpegurl")) {
            video.src = url;
            tryPlay();
        } else if (window.Hls && Hls.isSupported()) {
            hls = new Hls();
            hls.loadSource(url);
            hls.attachMedia(video);
            hls.on(Hls.Events.MANIFEST_PARSED, tryPlay);
            hls.on(Hls.Events.ERROR, function (_, data) {
                if (data.fatal) {
                    clearPlayer();
                    status.textContent = "等待直播画面，正在重试…";
                }
            });
        } else {
            clearPlayer();
            stopped = true;
            status.textContent = "此浏览器不支持直播播放，请使用新版 Chrome、Edge 或 Safari";
        }
    }
    async function check() {
        if (checking || stopped) return;
        checking = true;
        var controller = new AbortController();
        var timeout = setTimeout(function () { controller.abort(); }, 5000);
        try {
            var response = await fetch(endpoint, {credentials:"omit", cache:"no-store", signal:controller.signal});
            if (!response.ok && response.status !== 410) throw new Error("status unavailable");
            var data = await response.json();
            if (data.room) {
                document.getElementById("room").textContent = data.room;
                document.title = data.room + " · 直播观看";
            }
            status.textContent = messages[data.status] || messages.unavailable;
            if (data.status === "playing" && data.media_url) {
                // The API supplies same-origin media only; never load a remote URL from page input.
                var url = new URL(data.media_url, location.origin);
                if (url.origin !== location.origin || !url.pathname.startsWith("/live/")) throw new Error("invalid media URL");
                startPlayer(url.href);
                if (!stopped) status.textContent = !play.hidden ? "点击播放即可观看" : (video.readyState < 2 ? "正在加载直播画面…" : (video.paused ? "播放已暂停" : messages.playing));
            } else {
                clearPlayer();
                if (data.status === "ended") stopped = true;
            }
        } catch (_) {
            clearPlayer();
            status.textContent = messages.unavailable;
        } finally {
            clearTimeout(timeout);
            checking = false;
            if (!stopped) timer = setTimeout(check, 3000);
        }
    }
    video.addEventListener("error", function () {
        if (source) { clearPlayer(); status.textContent = "等待直播画面，正在重试…"; }
    });
    video.addEventListener("volumechange", function () { sound.textContent = video.muted ? "开启声音" : "静音"; });
    video.addEventListener("playing", function () { status.textContent = messages.playing; play.hidden = true; });
    play.addEventListener("click", tryPlay);
    sound.addEventListener("click", function () { video.muted = !video.muted; });
    document.getElementById("retry").addEventListener("click", function () {
        if (checking) return;
        stopped = false;
        clearTimeout(timer);
        clearPlayer();
        check();
    });
    window.addEventListener("pagehide", function () { stopped = true; clearTimeout(timer); clearPlayer(); });
    check();
}());
