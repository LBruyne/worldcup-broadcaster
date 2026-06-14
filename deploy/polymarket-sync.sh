#!/bin/bash
OUT=/root/wcb-polymarket/wc.json
python3 /root/wcb-polymarket/fetch.py > /tmp/wc.json.tmp 2>/dev/null
if [ -s /tmp/wc.json.tmp ] && python3 -c "import json,sys;d=json.load(open('/tmp/wc.json.tmp'));sys.exit(0 if d.get('markets') else 1)" 2>/dev/null; then
  mv /tmp/wc.json.tmp "$OUT"
  scp -q -i /root/.ssh/wcb_deploy -P 8733 -o ConnectTimeout=15 -o StrictHostKeyChecking=accept-new "$OUT" hins@115.236.33.124:/opt/worldcup/data/polymarket/wc.json \
    && echo "$(date -u +%H:%MZ) synced $(wc -c <$OUT)B OK" || echo "$(date -u +%H:%MZ) scp FAILED"
else
  echo "$(date -u +%H:%MZ) fetch empty/failed"
fi
