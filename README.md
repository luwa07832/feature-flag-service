# feature-flag-service

把功能开关的定义、目标环境、灰度比例、生效时间窗口和变更历史记录成可查询的服务，支持按环境和标记评估开关状态、追溯配置变更，并可按历史时点还原配置并评估当时状态。

## 运行要求

- Go 1.26 或以上
- SQLite（本服务自带存储，不需要外部数据库）

## 构建、测试与启动

```bash
go build ./...
go test ./...
go run .
```

服务默认监听 `127.0.0.1:8080`。可用环境变量覆盖：

| 变量 | 默认值 | 用途 |
|---|---|---|
| `ADDR` | `127.0.0.1:8080` | HTTP 监听地址 |
| `DB_PATH` | `feature-flag-service.db` | SQLite 数据库文件路径 |

## 统一语义

- 时间：所有时间点都是 UTC 时间线上的瞬时值，输入接受 RFC 3339 / RFC 3339Nano（如 `2026-03-01T08:00:00Z`、`2026-03-01T16:00:00+08:00`），响应规范化为 UTC 的 RFC 3339Nano（`Z` 结尾）。
- 标记（marker）：1–64 个字母、数字、`_`、`-`、`.` 字符。
- 灰度：环境与开关确定一个 FNV-1a 哈希分桶（0–9999），标记分桶小于 `灰度比例 × 100` 时命中；同环境、同开关、同标记结果恒定。
- 生效窗口：半开区间 `[start, end)`；未配置窗口表示任意时刻生效。
- 判定顺序：开关未启用或不在生效窗口内为 `off`，否则按灰度比例得到 `on`/`off`。
- 变更历史只追加：配置写入只新增不可变版本，任何查询（含历史还原）都不会写入或改写历史。

## 已公开的入口

### `GET /healthz`

返回服务与存储状态。正常时 HTTP 200：

```json
{"status":"ok","database":"ok"}
```

存储不可用时 HTTP 503：

```json
{"error":{"code":"storage_unavailable","message":"database is not available"}}
```

### `PUT /api/v1/environments/{environmentID}/flags/{flagID}/config`

追加一条配置版本。请求体：

```json
{
  "enabled": true,
  "rollout_percentage": 50,
  "effective_window": {"start": "2026-03-01T00:00:00Z", "end": "2026-12-31T00:00:00Z"},
  "changed_at": "2026-03-01T08:00:00Z",
  "note": "half rollout"
}
```

`effective_window` 可省略（表示无窗口）；`changed_at` 可省略（取服务当前时间）。成功返回 HTTP 201 与落库记录，记录带有可与变更历史对照的 `version_id`（如 `v3`）。

### `GET /api/v1/environments/{environmentID}/flags/{flagID}/evaluate?marker=...`

实时评估单个开关，始终使用该开关在目标环境内的最新配置。不带 `marker` 时返回快照字段与 `evaluated:false`、`result:"unevaluated"`；带合法 `marker` 时返回 `evaluated:true` 与 `on`/`off`。

### `GET /api/v1/environments/{environmentID}/flags/{flagID}/changes`

返回该开关在目标环境内的变更历史，按变更时间倒序。

### `GET /api/v1/environments/{environmentID}/snapshot?at=<历史时间点>&marker=<可选标记>`

按历史时点还原环境内全部开关并评估状态。

配置版本选择规则：同一开关在目标环境内，选取变更时间不晚于 `at` 的最后一条有效记录。当时尚无有效配置的开关（包括该时点之后才新增的开关）不会沿用未来配置，而是输出确定的 `unconfigured` 状态。

- 不带 `marker`：只返回配置快照，已配置开关统一为 `evaluated:false`、`result:"unevaluated"`。
- 带合法 `marker`：返回 `evaluated:true`，结果为 `on`/`off`（已配置）或 `unconfigured`（当时无配置）；每个开关同时给出所使用的 `version_id`。

响应示例（带标记）：

```json
{
  "environment_id": "prod",
  "at": "2026-05-01T00:00:00Z",
  "marker": "user-1",
  "flags": [
    {
      "flag_id": "checkout",
      "version_id": "v1",
      "enabled": true,
      "rollout_percentage": 50,
      "effective_window": {"start": "2026-03-01T00:00:00Z", "end": "2026-12-31T00:00:00Z"},
      "evaluated": true,
      "result": "on"
    },
    {
      "flag_id": "later_added",
      "version_id": null,
      "enabled": false,
      "rollout_percentage": null,
      "effective_window": null,
      "evaluated": true,
      "result": "unconfigured"
    }
  ]
}
```

同一环境、同一历史时间点、同一标记重复查询返回相同结果。

## 错误约定

所有错误响应都是单个顶层 `error` 对象，包含 `code` 与 `message` 两个字符串字段；`message` 不包含 SQL、堆栈或文件路径。

历史快照入口的边界错误码：

| HTTP | code | 触发条件 |
|---|---|---|
| 400 | `InvalidTimestamp` | `at`（或写入中的时间字段）无法按统一时间语义解析 |
| 404 | `EnvironmentNotFound` | 环境不存在，或在该时点没有任何可还原配置 |
| 400 | `InvalidMarker` | `marker` 不符合标记格式 |
