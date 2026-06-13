# WorldCup-Broadcaster 2026

2026 美加墨世界杯 QQ 群播报 Bot（2022 卡塔尔版重写）。

## 功能

- **实时播报**：开球 / 进球（含进球者+助攻、点球、乌龙）/ 红黄牌 / 换人 / 中场与下半场（含比分）/
  加时各节点 / **点球大战逐轮** / 终场，全部事件可在配置中独立开关
- **每晚 23:00**（北京时间）：次日全部赛程预告——开球时间、球场、两队近 5 场状态、
  历史交锋、小组积分榜 + **DeepSeek 生成的看点/出线形势分析**
- **每天 15:00**：当日（凌晨完赛）全部赛果总结——比分、进球者、红牌、积分榜 + **DeepSeek 锐评**
- **每日榜单**：预告/总结自动附带最新射手榜、助攻榜、相关小组完整积分榜与出线形势分析
- **群聊命令**：/help、/ask（整活人设问答，带20条群聊上下文+实时数据）、/赛果、/积分榜 [A-L]、/射手榜、/助攻榜、/晋级；多群支持（上下文按群隔离）
- **QQ 保活**：每3分钟探测在线状态，掉线自动重启 NapCat 并告警管理员
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

## /ask 联网搜索（DeepSeek 原生 web_search）

`/ask` 与 @机器人 默认走手写的 DuckDuckGo 抓取 + 核实管线。也可切换到 **DeepSeek 官方 Anthropic
端点的原生 `web_search`**（模型自主联网核实，替代抓取）：在 `config.yaml` 把

```yaml
llm:
  base_url: "https://api.deepseek.com/anthropic"   # api_format 留空会自动识别为 anthropic
  model: "deepseek-v4-pro"
  web_search: true
```

开启后 `/ask` 跳过 DuckDuckGo 抓取与人工核实步骤，由模型边搜边答；ESPN 的赛程/积分/榜单/逐场
详情仍作为权威数据内联喂入（DeepSeek 的 Anthropic 端点不支持 MCP，故 RAG 走内联）。

## 钉钉 ChatOps：群里指挥 Claude 改代码

在钉钉群 @机器人 发命令即可驱动 **Claude Code** 修改本仓库并自测，结果回贴群里。需要一个
**企业内部应用-机器人**并开启 **Stream 模式**（websocket，无需公网），配置见 `dingtalk_bot`：

```yaml
dingtalk_bot:
  enabled: true
  app_key: "..."          # 也可用环境变量 DINGBOT_APP_KEY / DINGBOT_APP_SECRET
  app_secret: "..."
  admin_staff_ids: ["你的staffId"]   # 仅这些人能跑改动类命令
```

群命令：`做 <需求>`（改代码+自测）、`问 <问题>`（只读分析）、`测试`、`状态`、`diff`、`pr`、`帮助`。
改动全部在**隔离的 git worktree + `chatops/work` 分支**进行，永不直接动 master；`pr` 推分支开 PR
由人审。详见 [设计文档](docs/superpowers/specs/2026-06-13-dingtalk-chatops-and-deepseek-search-design.md)。

## 开发

```bash
go test ./...        # 全部单测 + e2e（含 2022 决赛全场重放）
go test -race ./...
./broadcaster -run-preview-now   # 手动触发明日预告
./broadcaster -run-recap-now -date 2026-06-12   # 手动补发指定日战报
./broadcaster -simulate-fixture testdata/summary-633850.json  # 实时链路演习（只打第一个群）
./broadcaster -test-alert        # 测试管理员告警私聊
```
