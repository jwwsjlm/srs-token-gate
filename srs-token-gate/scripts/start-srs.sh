#!/bin/sh
set -eu

cd /usr/local/srs
mkdir -p /usr/local/srs/objs/nginx/html/console/js /usr/local/srs/objs/nginx/html/console/views
cp /opt/srs-token-gate/conf/console/ng_index.html /usr/local/srs/objs/nginx/html/console/ng_index.html
cp /opt/srs-token-gate/conf/console/js/srs.token.js /usr/local/srs/objs/nginx/html/console/js/srs.token.js
cp /opt/srs-token-gate/conf/console/views/tokens.html /usr/local/srs/objs/nginx/html/console/views/tokens.html

# Keep the built-in WHIP/WHEP pages connected to the unified Token Gate console.
# The marker makes this safe across container restarts and SRS image upgrades.
for player in whep.html whip.html; do
    player_file="/usr/local/srs/objs/nginx/html/players/$player"
    if [ -f "$player_file" ] && ! grep -q 'srs-token-gate-nav' "$player_file"; then
        sed -i 's|<ul class="nav">|<ul class="nav"><li id="srs-token-gate-nav"><a href="/console/ng_index.html#/tokens">Token 鉴权</a></li><li><a href="/console/ng_index.html#/summaries">返回控制台</a></li>|' "$player_file"
    fi
done

test -s /generated/token-auth.conf
exec ./objs/srs -c /generated/token-auth.conf
