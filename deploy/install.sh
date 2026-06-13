#!/usr/bin/env bash
# Builds the broadcaster and (re)installs it as a systemd service.
# Re-run after any code or config change. Preserves /opt/worldcup/config.yaml.
set -euo pipefail
cd "$(dirname "$0")/.."

go build -o /tmp/broadcaster ./cmd/broadcaster

sudo mkdir -p /opt/worldcup /var/log/worldcup
sudo mv /tmp/broadcaster /opt/worldcup/broadcaster
# Never clobber a configured config.yaml. Seed from the tracked template
# (config.example.yaml); the real config.yaml holds secrets and is gitignored.
if [ ! -f /opt/worldcup/config.yaml ]; then
  if [ -f config.yaml ]; then
    sudo cp config.yaml /opt/worldcup/config.yaml
  else
    sudo cp config.example.yaml /opt/worldcup/config.yaml
  fi
  echo ">>> 模板已复制到 /opt/worldcup/config.yaml，请填写必填项！"
fi
sudo cp deploy/worldcup-broadcaster.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable worldcup-broadcaster
sudo systemctl restart worldcup-broadcaster
echo "Installed. Logs: journalctl -u worldcup-broadcaster -f  或 /var/log/worldcup/broadcaster.log"
