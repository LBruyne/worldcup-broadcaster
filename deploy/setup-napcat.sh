#!/usr/bin/env bash
# Launches NapCat (QQ protocol endpoint, OneBot 11) in Docker.
# After it starts: open the WebUI, scan the QR code with the bot QQ's phone,
# then enable an HTTP server (port 3000) in the network config — see
# deploy/README.md for the click-by-click steps.
set -euo pipefail

NAPCAT_DIR="${NAPCAT_DIR:-/opt/worldcup/napcat}"
mkdir -p "$NAPCAT_DIR/config" "$NAPCAT_DIR/qq"

docker rm -f napcat 2>/dev/null || true
docker run -d \
  --name napcat \
  --restart=always \
  --network host \
  --mac-address "02:42:ac:11:00:99" \
  -e NAPCAT_UID=0 -e NAPCAT_GID=0 ${ACCOUNT:+-e ACCOUNT=$ACCOUNT} \
  -v "$NAPCAT_DIR/config:/app/napcat/config" \
  -v "$NAPCAT_DIR/qq:/app/.config/QQ" \
  mlikiowa/napcat-docker:latest

echo "NapCat started."
echo "WebUI:  http://<server-ip>:6099/webui  (token below)"
sleep 8
docker logs napcat 2>&1 | grep -iE "webui|token|key" | tail -5 || true
echo
echo "下一步：手机 QQ 扫码登录，然后在 WebUI 的「网络配置」里新建 HTTP服务器："
echo "  端口 3000，token 填 config.yaml 里 onebot.access_token 的值"
