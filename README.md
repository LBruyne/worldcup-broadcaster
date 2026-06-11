# WorldCup-Broadcaster 2026

2026 美加墨世界杯 QQ 群播报 Bot（2022 卡塔尔版重写）。

## 功能

- **实时播报**：开球 / 进球（含进球者+助攻、点球、乌龙）/ 红黄牌 / 换人 / 中场与下半场（含比分）/
  加时各节点 / **点球大战逐轮** / 终场，全部事件可在配置中独立开关
- **每晚 23:00**（北京时间）：次日全部赛程预告——开球时间、球场、两队近 5 场状态、
  历史交锋、小组积分榜 + **DeepSeek 生成的看点/出线形势分析**
- **每天 15:00**：当日（凌晨完赛）全部赛果总结——比分、进球者、红牌、积分榜 + **DeepSeek 锐评**
- 抓取数据全部落盘为 LLM 友好 JSON（`data/<日期>/`），事件去重状态持久化，重启不刷屏
- 结构化日志（按天轮转，保留 30 天），故障私聊告警管理员 QQ（限流防轰炸）

## 技术栈

| 模块 | 实现 |
|------|------|
| 数据源 | ESPN 公开 API（免 key，实测 75ms，含赛程/事件流/点球大战/H2H/积分榜） |
| QQ 推送 | NapCat（Docker）+ OneBot 11 HTTP，串行发送队列防风控 |
| 看点/锐评 | DeepSeek `deepseek-v4-pro`（失败自动降级，不影响主消息） |
| 运行时 | Go 单二进制 + systemd，robfig/cron（Asia/Shanghai） |

## 快速开始

```bash
bash deploy/setup-napcat.sh   # 启动 NapCat，扫码登录
bash deploy/install.sh        # 编译并安装 systemd 服务
vim /opt/worldcup/config.yaml # 填群号 / admin QQ / DeepSeek key / token
systemctl restart worldcup-broadcaster
```

详细步骤见 [deploy/README.md](deploy/README.md)。

## 开发

```bash
go test ./...        # 全部单测 + e2e（含 2022 决赛全场重放）
go test -race ./...
./broadcaster -run-preview-now   # 手动触发明日预告
./broadcaster -run-recap-now -date 2026-06-12   # 手动补发指定日战报
```
