scApp.config(["$routeProvider", function($routeProvider) {
    $routeProvider.when("/tokens", {templateUrl:"views/tokens.html?v=2", controller:"CSCTokens"});
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
    $scope.sessions = [];
    $scope.publishCount = 0;
    $scope.playCount = 0;
    $scope.createdToken = "";
    $scope.address = {key:null, stream:"stream"};
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

    $scope.refresh = function() {
        Promise.all([request("/api/token/keys"), request("/api/token/sessions")]).then(function(values) {
            apply(function() {
                $scope.authenticated = true;
                $rootScope.tokenLoggedIn = true;
                $scope.keys = values[0].keys || [];
                $scope.sessions = values[1].sessions || [];
                if (values[1].counts) {
                    $scope.publishCount = values[1].counts.publish || 0;
                    $scope.playCount = values[1].counts.play || 0;
                } else {
                    updateConnectionCounts($scope.sessions);
                }
                if (!$scope.address.key && $scope.keys.length) $scope.address.key = $scope.keys[0];
                if ($scope.address.key && $scope.address.key.stream) $scope.address.stream = $scope.address.key.stream;
                $scope.rememberKey($scope.address.key);
            });
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
        request("/api/token/keys/" + key.id, {method:"PATCH", body:JSON.stringify({enabled:!key.enabled})}).then($scope.refresh);
    };

    $scope.useKey = function(key) {
        $scope.address.key = key;
        if (key && key.stream) $scope.address.stream = key.stream;
        $scope.rememberKey(key);
    };
    $scope.selectAddressKey = function(key) {
        $scope.useKey(key);
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
        var keyStream = $scope.address.key && $scope.address.key.stream;
        return (keyStream || $scope.address.stream || "stream").replace(/[^A-Za-z0-9_.-]/g, "");
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
        $scope.sessions = [];
        $scope.publishCount = 0;
        $scope.playCount = 0;
        $scope.createdToken = "";
    });
    var timer = setInterval(function() { if ($scope.authenticated) $scope.refresh(); }, 5000);
    $scope.$on("$destroy", function() {
        clearInterval(timer);
        if (copyTimer) clearTimeout(copyTimer);
    });
}]);
