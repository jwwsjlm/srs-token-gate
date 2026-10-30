scApp.config(["$routeProvider", function($routeProvider) {
    $routeProvider.when("/tokens", {templateUrl:"views/tokens.html?v=4", controller:"CSCTokens"});
}]);

// Keep the built-in SRS preview on the public reverse-proxy origin instead of
// exposing the internal SRS HTTP port. The last selected Token is reused so
// preview requests pass the same stream authentication check as HTTP-FLV.
scApp.filter("sc_filter_preview_url", ["$sc_server", function($sc_server) {
    return function(v) {
        if (!v) return "javascript:void(0)";
        var origin = window.location.origin;
        var host = window.location.hostname;
        var schema = window.location.protocol === "https:" ? "https" : "http";
        var port = window.location.port || (schema === "https" ? "443" : "80");
        var vhost = v.owner && v.owner.name ? v.owner.name : (v.vhost || host);
        var app = v.app || "live";
        var stream = (v.name || "") + ".flv";
        var query = "vhost=" + encodeURIComponent(vhost) +
            "&app=" + encodeURIComponent(app) +
            "&stream=" + encodeURIComponent(stream) +
            "&server=" + encodeURIComponent(host) +
            "&port=" + encodeURIComponent(port) +
            "&autostart=true&schema=" + encodeURIComponent(schema);
        var token = "";
        try { token = window.localStorage.getItem("SRS_PREVIEW_TOKEN") || ""; } catch (e) {}
        if (token) query += "&token=" + encodeURIComponent(token);
        return origin + "/players/srs_player.html?" + query;
    };
}]);

scApp.run(["$rootScope", "$location", function($rootScope, $location) {
    function rootApply(fn) { if ($rootScope.$$phase) fn(); else $rootScope.$apply(fn); }
    $rootScope.tokenLoggedIn = false;
    $rootScope.tokenLogout = function() {
        fetch("/api/token/logout", {method:"POST", credentials:"same-origin"}).finally(function() {
            rootApply(function() {
                $rootScope.tokenLoggedIn = false;
                $rootScope.$broadcast("tokenLoggedOut");
                $location.path("/tokens");
            });
        });
    };
    fetch("/api/token/me", {credentials:"same-origin"}).then(function(r) {
        rootApply(function() {
            $rootScope.tokenLoggedIn = r.ok;
            if (!r.ok && $location.path() !== "/tokens") $location.path("/tokens");
        });
    }).catch(function() {
        rootApply(function() {
            $rootScope.tokenLoggedIn = false;
            if ($location.path() !== "/tokens") $location.path("/tokens");
        });
    });
}]);

scApp.controller("CSCTokens", ["$scope", "$rootScope", "$location", "$sc_nav", function($scope, $rootScope, $location, $sc_nav) {
    $sc_nav.selected = "/tokens";
    $scope.authenticated = false;
    $scope.login = {username:"admin", password:"", error:""};
    $scope.keys = [];
    $scope.rooms = [];
    $scope.sessions = [];
    $scope.publishCount = 0;
    $scope.playCount = 0;
    $scope.createdToken = "";
    $scope.createdRoom = null;
    $scope.share = null;
    $scope.shareBusy = false;
    $scope.roomCreate = {name:""};
    $scope.sessionRoom = null;
    $scope.address = {room:null, key:null, stream:"stream"};
    $scope.publicConfig = {address:"", rtmpPort:1935};
    $scope.copyMessage = "";
    $scope.copyClass = "alert-success";
    var copyTimer = null;

    $scope.rememberKey = function(key) {
        try {
            if (key && key.token) window.localStorage.setItem("SRS_PREVIEW_TOKEN", key.token);
        } catch (e) {}
    };

    function apply(fn) { if ($scope.$$phase) fn(); else $scope.$apply(fn); }
    function request(url, options) {
        options = options || {};
        options.credentials = "same-origin";
        options.headers = options.headers || {};
        if (options.body) options.headers["Content-Type"] = "application/json";
        return fetch(url, options).then(function(r) {
            if (r.status === 401) throw {unauthorized:true};
            return r.json().then(function(data) {
                if (!r.ok) throw data;
                return data;
            });
        });
    }

    function updateConnectionCounts(sessions) {
        var publish = 0, play = 0;
        (sessions || []).forEach(function(session) {
            if (session.action === "on_publish") publish++;
            if (session.action === "on_play") play++;
        });
        $scope.publishCount = publish;
        $scope.playCount = play;
    }

    function findByID(items, id) {
        if (!id) return null;
        for (var i = 0; i < (items || []).length; i++) {
            if (items[i].id === id) return items[i];
        }
        return null;
    }

    function roomToken(room) {
        return room ? findByID($scope.keys, room.key_id) : null;
    }

    $scope.addressKeys = function() {
        var room = $scope.address.room;
        if (!room) return $scope.keys;
        return ($scope.keys || []).filter(function(key) {
            return key.id === room.key_id || !key.stream || key.stream === room.name;
        });
    };

    $scope.refresh = function(preferredRoomID) {
        var selectedAddressRoomID = preferredRoomID || ($scope.address.room && $scope.address.room.id);
        var selectedKeyID = $scope.address.key && $scope.address.key.id;
        var selectedFilterID = $scope.sessionRoom && $scope.sessionRoom.id;
        Promise.all([request("/api/token/keys"), request("/api/token/rooms")]).then(function(meta) {
            var roomID = preferredRoomID || selectedFilterID;
            var sessionURL = "/api/token/sessions" + (roomID ? "?room_id=" + encodeURIComponent(roomID) : "");
            return request(sessionURL).then(function(sessionData) { return [meta[0], meta[1], sessionData]; });
        }).then(function(values) {
            apply(function() {
                $scope.authenticated = true;
                $rootScope.tokenLoggedIn = true;
                $scope.keys = values[0].keys || [];
                $scope.rooms = values[1].rooms || [];
                $scope.sessions = values[2].sessions || [];
                $scope.sessionRoom = findByID($scope.rooms, preferredRoomID || selectedFilterID);
                $scope.address.room = findByID($scope.rooms, selectedAddressRoomID);
                if (!$scope.address.room && !$scope.address.key && $scope.rooms.length) $scope.address.room = $scope.rooms[0];
                if ($scope.address.room) {
                    $scope.address.stream = $scope.address.room.name;
                    $scope.address.key = roomToken($scope.address.room) || findByID($scope.keys, selectedKeyID);
                } else {
                    $scope.address.key = findByID($scope.keys, selectedKeyID) || $scope.keys[0] || null;
                    if ($scope.address.key && $scope.address.key.stream) $scope.address.stream = $scope.address.key.stream;
                }
                if (values[2].counts) {
                    $scope.publishCount = values[2].counts.publish || 0;
                    $scope.playCount = values[2].counts.play || 0;
                } else {
                    updateConnectionCounts($scope.sessions);
                }
                $scope.rememberKey($scope.address.key);
            });
            $scope.refreshShare();
        }).catch(function(e) {
            if (e && e.unauthorized) apply(function() { $scope.authenticated = false; $rootScope.tokenLoggedIn = false; });
        });
    };

    $scope.doLogin = function() {
        $scope.login.error = "";
        request("/api/token/login", {method:"POST", body:JSON.stringify($scope.login)}).then(function() {
            apply(function() { $scope.login.password = ""; $scope.authenticated = true; $rootScope.tokenLoggedIn = true; });
            $scope.refresh();
        }).catch(function(e) {
            apply(function() { $scope.login.error = (e && e.error) || "登录失败"; });
        });
    };

    $scope.createKey = function() {
        request("/api/token/keys", {method:"POST", body:JSON.stringify($scope.create)}).then(function(data) {
            apply(function() { $scope.createdToken = data.token; $scope.create.name = ""; $scope.create.stream = ""; });
            $scope.refresh();
        }).catch(function(e) { apply(function() { $scope.error = e.error || "生成失败"; }); });
    };
    $scope.create = {name:"", scope:"both", stream:""};

    $scope.createRoom = function() {
        request("/api/token/rooms", {method:"POST", body:JSON.stringify($scope.roomCreate)}).then(function(data) {
            apply(function() {
                $scope.createdRoom = data.room;
                $scope.roomCreate.name = "";
            });
            $scope.refresh(data.room.id);
        }).catch(function(e) { announceCopy((e && e.error) || "创建房间失败", "alert-error"); });
    };

    $scope.toggleRoom = function(room) {
        request("/api/token/rooms/" + room.id, {method:"PATCH", body:JSON.stringify({enabled:!room.enabled})}).then(function() {
            $scope.refresh();
        }).catch(function(e) { announceCopy((e && e.error) || "更新房间失败", "alert-error"); });
    };

    $scope.deleteRoom = function(room) {
        if (!window.confirm("确认删除房间 “" + room.name + "” 及其自带 Token 吗？")) return;
        request("/api/token/rooms/" + room.id, {method:"DELETE"}).then(function() {
            apply(function() {
                if ($scope.address.room && $scope.address.room.id === room.id) $scope.address.room = null;
                if ($scope.sessionRoom && $scope.sessionRoom.id === room.id) $scope.sessionRoom = null;
            });
            announceCopy("房间已删除", "alert-success");
            $scope.refresh();
        }).catch(function(e) { announceCopy((e && e.error) || "删除房间失败", "alert-error"); });
    };

    $scope.useRoom = function(room) {
        $scope.address.room = room;
        if (room) {
            $scope.address.stream = room.name;
            $scope.address.key = roomToken(room);
            $scope.rememberKey($scope.address.key);
        }
        $scope.share = null;
        $scope.refreshShare();
    };

    $scope.selectAddressRoom = function(room) {
        $scope.useRoom(room);
    };

    $scope.selectSessionRoom = function() {
        $scope.refresh();
    };

    $scope.reloadSRS = function() {
        if (!window.confirm("确认重载统一配置吗？修改管理员、Session 或 Hook Secret 后仍需重启。")) return;
        request("/api/token/config/reload", {method:"POST"}).then(function(data) {
            var message = data.restart_required && data.restart_required.length ? "配置已检查，需要重启：" + data.restart_required.join(", ") : "统一配置重载成功";
            announceCopy(message, data.restart_required && data.restart_required.length ? "alert-warning" : "alert-success");
        }).catch(function(e) {
            announceCopy((e && e.error) || "统一配置重载失败", "alert-error");
        });
    };

    $scope.toggle = function(key) {
        request("/api/token/keys/" + key.id, {method:"PATCH", body:JSON.stringify({enabled:!key.enabled})}).then(function() { $scope.refresh(); });
    };

    $scope.useKey = function(key) {
        var linkedRoom = key && key.room_id ? findByID($scope.rooms, key.room_id) : null;
        $scope.address.room = linkedRoom;
        $scope.address.key = key;
        if (linkedRoom) $scope.address.stream = linkedRoom.name;
        else if (key && key.stream) $scope.address.stream = key.stream;
        $scope.rememberKey(key);
        $scope.share = null;
        $scope.refreshShare();
    };
    $scope.selectAddressKey = function(key) {
        $scope.address.key = key;
        if (!$scope.address.room && key && key.stream) $scope.address.stream = key.stream;
        $scope.rememberKey(key);
    };
    function announceCopy(message, cssClass) {
        apply(function() {
            $scope.copyMessage = message;
            $scope.copyClass = cssClass;
        });
        if (copyTimer) clearTimeout(copyTimer);
        copyTimer = setTimeout(function() {
            apply(function() { $scope.copyMessage = ""; });
        }, 1800);
    }
    function fallbackCopy(value) {
        var area = document.createElement("textarea");
        area.value = value;
        area.setAttribute("readonly", "");
        area.style.position = "fixed";
        area.style.left = "-9999px";
        document.body.appendChild(area);
        area.select();
        var ok = false;
        try { ok = document.execCommand("copy"); } catch (e) {}
        document.body.removeChild(area);
        announceCopy(ok ? "复制成功，内容已放入剪贴板" : "复制失败，请手动复制", ok ? "alert-success" : "alert-error");
    }
    $scope.copy = function(value) {
        if (!value) {
            announceCopy("没有可复制的内容", "alert-error");
            return;
        }
        if (navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(value).then(function() {
                announceCopy("复制成功，内容已放入剪贴板", "alert-success");
            }).catch(function() { fallbackCopy(value); });
        } else {
            fallbackCopy(value);
        }
    };
    $scope.streamName = function() {
        var roomStream = $scope.address.room && $scope.address.room.name;
        var keyStream = $scope.address.key && $scope.address.key.stream;
        return (roomStream || keyStream || $scope.address.stream || "stream").replace(/[^A-Za-z0-9_.-]/g, "");
    };
    $scope.rtmpServer = function() {
        var host = $scope.publicConfig.address || location.hostname;
        var port = parseInt($scope.publicConfig.rtmpPort, 10) || 1935;
        return "rtmp://" + host + (port === 1935 ? "" : ":" + port) + "/live";
    };
    $scope.rtmpURL = function() { var k=$scope.address.key; return k ? $scope.rtmpServer()+"/"+$scope.streamName()+"?token="+k.token : ""; };
    $scope.flvURL = function() { var k=$scope.address.key; return k ? location.origin+"/live/"+$scope.streamName()+".flv?token="+k.token : ""; };
    $scope.hlsURL = function() { var k=$scope.address.key; return k ? location.origin+"/live/"+$scope.streamName()+".m3u8?token="+k.token : ""; };
    $scope.whipURL = function() {
        var k=$scope.address.key;
        return k ? location.origin+"/rtc/v1/whip/?app=live&stream="+encodeURIComponent($scope.streamName())+"&token="+encodeURIComponent(k.token) : "";
    };
    $scope.whepURL = function() {
        var k=$scope.address.key;
        return k ? location.origin+"/rtc/v1/whep/?app=live&stream="+encodeURIComponent($scope.streamName())+"&token="+encodeURIComponent(k.token) : "";
    };
    $scope.obsKey = function() { var k=$scope.address.key; return k ? $scope.streamName()+"?token="+k.token : ""; };

    var shareVersion = 0;
    $scope.shareStatus = function() {
        var labels = {playing:"分享已开启", closed:"分享已关闭", reconnecting:"等待推流重连（30 秒内保留链接）", ended:"尚未生成或本次分享已结束", unavailable:"暂时无法确认推流状态，分享播放已暂停"};
        return labels[$scope.share && $scope.share.status] || "正在读取分享状态…";
    };
    $scope.shareURL = function() { return $scope.share && $scope.share.path ? location.origin + $scope.share.path : ""; };
    $scope.refreshShare = function() {
        var room = $scope.address.room;
        var version = ++shareVersion;
        if (!room) { $scope.share = null; return; }
        if ($scope.shareBusy) return;
        return request("/api/token/rooms/" + room.id + "/share").then(function(data) {
            apply(function() {
                if (version === shareVersion && $scope.address.room && room.id === $scope.address.room.id) $scope.share = data;
            });
        }).catch(function() {
            apply(function() { if (version === shareVersion) $scope.share = {status:"unavailable"}; });
        });
    };
    function changeShare(method, enabled) {
        var room = $scope.address.room;
        if (!room || $scope.shareBusy) return;
        ++shareVersion;
        $scope.shareBusy = true;
        var options = {method:method};
        if (method === "PATCH") options.body = JSON.stringify({enabled:enabled});
        request("/api/token/rooms/" + room.id + "/share", options).then(function(data) {
            apply(function() { if ($scope.address.room && room.id === $scope.address.room.id) $scope.share = data; });
            announceCopy(method === "POST" ? "分享链接已生成，打开即可观看" : (enabled ? "分享已开启" : "分享已关闭"), "alert-success");
        }).catch(function(e) {
            announceCopy(e.error || "分享操作失败，请重试", "alert-error");
        }).finally(function() {
            apply(function() { $scope.shareBusy = false; });
            $scope.refreshShare();
        });
    }
    $scope.createShare = function() { changeShare("POST"); };
    $scope.toggleShare = function() { changeShare("PATCH", !$scope.share.enabled); };

    fetch("/api/token/me", {credentials:"same-origin"}).then(function(r) {
        if (r.ok) $scope.refresh(); else apply(function() { $scope.authenticated = false; });
    });
    fetch("/api/token/public-config", {credentials:"same-origin"}).then(function(r) {
        return r.ok ? r.json() : null;
    }).then(function(config) {
        if (!config) return;
        apply(function() {
            $scope.publicConfig.address = config.public_address || "";
            $scope.publicConfig.rtmpPort = config.rtmp_public_port || 1935;
        });
    }).catch(function() {});
    $scope.$on("tokenLoggedOut", function() {
        $scope.authenticated = false;
        $scope.keys = [];
        $scope.rooms = [];
        $scope.sessions = [];
        $scope.publishCount = 0;
        $scope.playCount = 0;
        $scope.createdToken = "";
        $scope.createdRoom = null;
        $scope.share = null;
        ++shareVersion;
        $scope.address.room = null;
        $scope.sessionRoom = null;
    });
    var timer = setInterval(function() { if ($scope.authenticated) $scope.refresh(); }, 5000);
    $scope.$on("$destroy", function() {
        clearInterval(timer);
        if (copyTimer) clearTimeout(copyTimer);
    });
}]);
