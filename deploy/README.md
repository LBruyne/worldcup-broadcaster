# 部署手册

服务器上已完成：Go 编译、systemd 服务（`worldcup-broadcaster`）、NapCat 容器。
**你只需要完成下面 3 步，Bot 即可真实发消息。**

## 你的待办（约 5 分钟）

### 1. 填配置 `/opt/worldcup/config.yaml`

```yaml
onebot:
  access_token: "<已预生成，保持不动>"   # 第 2 步会把它填进 NapCat
  group_id: 123456789                  # 改成目标 QQ 群号
  admin_qq: 123456                     # 改成你自己的 QQ（接收告警私聊）
llm:
  api_key: "sk-..."                    # 你的 DeepSeek API Key
```

### 2. 登录 NapCat 并开启 HTTP 服务

1. 浏览器打开 `http://<服务器IP>:6099/webui`，token 看
   `docker logs napcat | grep -i token`
2. 用 **Bot 的 QQ 手机扫码登录**（该 QQ 需已加入目标群）
3. WebUI →「网络配置」→ 新建 **HTTP服务器**：
   - 端口：`3000`，host：`0.0.0.0`（或 127.0.0.1）
   - token：填 config.yaml 里 `onebot.access_token` 的值
   - 启用并保存

### 3. 重启服务并验证

```bash
systemctl restart worldcup-broadcaster

# 立即手动发一条明日预告到群里验证链路（23:00 cron 之外的补发手段）
cd /opt/worldcup && ./broadcaster -run-preview-now

# 看日志
journalctl -u worldcup-broadcaster -f
tail -f /var/log/worldcup/broadcaster.log
```

## 日常运维

| 操作 | 命令 |
|------|------|
| 看服务状态 | `systemctl status worldcup-broadcaster` |
| 实时日志 | `journalctl -u worldcup-broadcaster -f` |
| 手动补发预告/战报 | `./broadcaster -run-preview-now` / `-run-recap-now`（可加 `-date 2026-06-13`） |
| 改配置后 | `systemctl restart worldcup-broadcaster` |
| 更新代码后 | `bash deploy/install.sh` |
| NapCat 状态 | `docker logs napcat --tail 50` |

## 自动行为（无需干预）

- **每晚 23:00（北京时间）**：推送次日全部赛程 + 两队近况/交锋/积分 + DeepSeek 看点
- **每天 15:00**：总结当日凌晨完赛的全部比赛 + 积分榜 + DeepSeek 锐评
- **比赛期间**：每 20 秒轮询，实时推送开球/进球/红黄牌/换人/中场/加时/点球大战逐轮/终场
- **异常**：ESPN 连续拉取失败、QQ 发送失败 → 私聊告警你的 admin_qq（同类告警 30 分钟最多一条）
- 数据落盘 `/opt/worldcup/data/<日期>/`，事件去重状态保证服务重启不重复刷群

## 故障排查

- 群里收不到消息：先看 `docker logs napcat`（QQ 是否掉线）；再看 broadcaster 日志里
  `send group message failed`
- QQ 异地风控：重新扫码登录即可，登录态持久化在 `/opt/worldcup/napcat/qq`
- LLM 看点缺失：检查 api_key 与余额；看点失败不影响预告主体发送
