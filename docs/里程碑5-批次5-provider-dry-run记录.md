# 里程碑 5 · 批次 5 provider dry-run 记录

日期：2026-07-21

## 一、边界与结论

本批按 M5-A 已批准边界，以不含真实候选人身份的合成 fixture 调用真实 DeepSeek V4 Pro。运行使用临时数据库，命令 runner 不具备自动回复派发能力，因此只能形成建议，不能构造或发送 Chrome 命令。

最终 dry-run 通过：请求路径保持硬编码 `thinking={"type":"disabled"}`；两次响应均为 `UsageShape=reasoningFieldAbsent`，且已确认所消费 `message.reasoning_content` 缺失或为空。两次调用严格按 `intent -> reply` 串行发生，无 adapter 重试、无 V4 Flash 或影子评测、无 Chrome 副作用。

## 二、放宽裁决的如实落地

本轮先后独立提交：

- `bd2ea19 docs: 勘误 M5 非思考用量闸`：将“reasoning token 字段缺失即阻断”修订为“字段缺失且 `reasoning_content` 为空时放行”。
- `94467bb fix: 兼容 V4 非思考用量缺失形态`：生产实现与测试按同一判据收敛；`reasoning_content` 只做空值判断，不落库、不进日志。

回归测试覆盖三类获批形态：

1. `reasoning_tokens` 缺失且 `reasoning_content` 为空：放行；
2. `reasoning_content` 非空：`manualRequired/reasoningUsageUnsafe`；
3. `reasoning_tokens` 为正值：`manualRequired/reasoningUsageUnsafe`。

输出 token 与费用始终使用 provider 返回的 `completion_tokens` 如实计量；未把缺失的 reasoning token 字段伪造为 0。

## 三、最终隔离 dry-run 证据

| 顺序 | purpose | input tokens | cached input | output tokens | usage shape | reasoning content | latency | estimated cost |
|---|---:|---:|---:|---:|---|---|---:|---:|
| 1 | intent | 136 | 128 | 11 | `reasoningFieldAbsent` | empty | 1,315 ms | 14 microUSD |
| 2 | reply | 440 | 0 | 72 | `reasoningFieldAbsent` | empty | 1,915 ms | 254 microUSD |
| 合计 | 2 次独立调用 | 576 | 128 | 83 | — | — | 3,230 ms | 268 microUSD |

本次总估算成本为 `$0.000268`。输入/输出只记录 hash、token、时延和费用；未记录 prompt、聊天/简历正文、平台 ID、displayName、base request/response、base URL 或 key。最终建议停在 `adviceReady`，未进入任何手端命令。

勘误前曾有两次 intent-only 诊断调用，用于确认 V4 Pro 的真实 usage 形态；它们均发生在隔离临时数据路径，未产生 reply、生产账本或 Chrome 命令。最终验收数据只取上表所列的勘误后完整 dry-run。

## 四、生产脑配置生效确认

生产脑以最新代码受控重启后记录：

- 启动日志只显示 `provider=deepseek model=deepseek-v4-pro`，未输出 base URL 或 key；
- 脱敏配置接口确认 base URL/key 均已配置，请求超时与 P 档输入/输出上限已加载；
- 账号仍为 `enabledToday=false`、`pausedReason=userPaused`、`identityCurrent=false`；
- 手重新连接，但没有启动巡检或自动派发；
- 生产库保持 `ai_invocations=0`、`dialogue_turns=0`、`communication_actions=0`、`messages=3`；
- `chat.sendMessage` 命令总数仍为 0。

因此，本次配置生效确认没有扩大试运行范围，也没有触发真实候选人数据出站或平台副作用。

## 五、门禁与停止点

最终串行门禁全部绿色：

- `go test -count=1 ./...`
- `go test -race -count=1 ./client/service/...`
- `go run ./contract/codegen -check`
- `CGO_ENABLED=0 GOOS=windows go build ./client/service`

批次 5 当前可达阻断项为零。按批准的实施边界，工作停在批次 6 真机验收开始前；未启用账号、未调用真实页面自动回复，也未开始 M5-B。
