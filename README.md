# feature-flag-service

把功能开关的定义、目标环境、灰度比例、生效时间窗口和变更历史记录成可查询的服务，支持按环境和标记评估开关状态并追溯配置变更，并支持按历史时点还原配置并评估状态。

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

- **时间**：所有出入参时间戳均为带偏移量的 RFC 3339 字符串（如 `2026-01-01T08:00:00Z`），服务统一解释为绝对时刻（Unix 纳秒），与服务器本地时区无关。
- **标识**：环境标识与开关标识匹配 `^[a-z0-9_-]{1,64}$`。
- **标记**：用户标记匹配 `^[a-z0-9_-]{1,64}$`。
- **灰度**：灰度比例为 0–100 的整数；分桶是 `(开关标识, 环境标识, 标记)` 的纯函数（FNV-1a），同一输入在任何实例上结果一致。
- **生效窗口**：`starts_at <= 时点 < ends_at`，起点含、终点不含；两端均可为空表示不限。
- **变更历史**：只追加，不修改不删除；删除配置会追加一条 `tombstone` 墓碑记录。
- **状态词汇**：`on`（对该标记启用）、`off`（有配置但未命中）、`unconfigured`（该时点无有效配置的确定状态）、`not_evaluated`（未提供标记时的统一未评估状态）。

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

### 管理面

- `POST /api/v1/environments` —— 注册环境，请求体 `{"key":"prod"}`。
- `POST /api/v1/flags` —— 注册功能开关，请求体 `{"key":"checkout"}`。
- `GET /api/v1/flags` —— 列出全部开关标识。
- `PUT /api/v1/environments/{environment}/flags/{flagKey}/config` —— 追加一个配置版本：
  ```json
  {"enabled":true,"percentage":50,"window":{"starts_at":"2026-01-01T03:00:00Z","ends_at":"2026-01-01T06:00:00Z"}}
  ```
  `window` 整体或其中任一端点可省略。响应返回新配置版本（含 `version` 与 `changed_at`）。
- `DELETE /api/v1/environments/{environment}/flags/{flagKey}/config` —— 对当前配置追加墓碑记录。

### 实时评估（既有行为）

`GET /api/v1/environments/{environment}/evaluate?marker=alpha`

按当前时刻评估该环境下全部开关。每个开关返回 `flag_key`、当前 `version`、`percentage`、`window`、`enabled` 与确定 `status`；当前没有有效配置的开关状态为 `unconfigured`。

### 变更历史查询（既有行为）

`GET /api/v1/environments/{environment}/flags/{flagKey}/history`

按时间升序返回该开关在该环境中的全部配置版本（含墓碑），每条都带 `version`、`changed_at`、灰度比例、窗口与启用状态。

### 单次判定解释（本次新增）

`GET /api/v1/environments/{environment}/flags/{flagKey}/explain?marker=alpha&at=2026-01-01T05:00:00Z`

输入：

- 路径参数 `environment`、`flagKey`：目标环境与开关标识。
- 查询参数 `marker`：用户标记（必填，需符合标记格式）。
- 查询参数 `at`：历史时间点（RFC 3339，可选）；缺省时按服务当前时刻判定，提供时还原 `changed_at` 不晚于该时点的最后一条配置（同一时刻以写入顺序的最后一条为准）。

判定顺序固定：无配置或最后版本为墓碑时 `reason` 为 `unconfigured`；否则 `enabled` 为 `false` 时为 `disabled`；窗口不覆盖判定时点时为 `window_inactive`；灰度未命中时为 `rollout_miss`；其余情况为 `enabled`。`at` 早于首条配置时同样返回 `unconfigured`，不会倒灌后续配置。

```json
{
  "environment": "prod",
  "flag_key": "checkout",
  "marker": "alpha",
  "evaluated_at": "2026-01-01T05:00:00Z",
  "status": "on",
  "reason": "enabled",
  "config": {
    "version": "cfg-…",
    "enabled": true,
    "percentage": 50,
    "window": {"starts_at": null, "ends_at": null}
  }
}
```

`status` 只取 `on` / `off` / `unconfigured`；`config` 在无有效配置时为 `null`，有配置时含 `version`、`enabled`、`percentage`、`window`（空窗口端点为 `null`），`version` 可与变更历史中的记录对照。接口只查询不写历史；相同输入重复查询结果稳定。

### 按历史时点还原配置并评估（既有行为）

`GET /api/v1/environments/{environment}/evaluate-at?at=2026-01-01T05:00:00Z&marker=alpha`

输入：

- 路径参数 `environment`：目标环境标识。
- 查询参数 `at`：历史时间点（RFC 3339，必填）。
- 查询参数 `marker`：用户标记（可选，需符合标记格式）。

版本选择规则：对每个开关，选择目标环境内 `changed_at <= at` 的最后一条记录（同一时刻以写入顺序的最后一条为准）。该时点之后新增或修改的配置不会被沿用；若最后一条是墓碑或此前没有任何记录，该开关输出 `unconfigured` 确定状态，快照字段为 `null`。

不带 `marker` 时只返回配置快照，每个开关的 `status` 统一为 `not_evaluated`：

```json
{
  "environment": "prod",
  "at": "2026-01-01T05:00:00Z",
  "marker": null,
  "flags": [
    {"flag_key":"checkout","version":"cfg-…","percentage":50,
     "window":{"starts_at":null,"ends_at":null},"enabled":true,
     "status":"not_evaluated"},
    {"flag_key":"coupon","version":null,"percentage":null,"window":null,
     "enabled":null,"status":"not_evaluated"}
  ]
}
```

带 `marker` 时返回确定状态（`on` / `off` / `unconfigured`），每个开关同时给出该状态所依据的 `version`，可直接与变更历史中的记录对照。同一环境、同一历史时点、同一标记重复查询结果完全相同；查询不会写入任何变更记录。

## 错误约定

所有错误响应都是单个顶层 `error` 对象，包含 `code` 与 `message` 两个字符串字段；`message` 不包含 SQL、堆栈或文件路径。

历史时点查询的边界结果唯一：

| HTTP | code | 触发条件 |
|---|---|---|
| 400 | `InvalidTimestamp` | `at`（或配置窗口）时间戳缺失或无法按 RFC 3339 解析 |
| 400 | `InvalidMarker` | `marker` 不符合标记格式（实时评估缺失标记同样返回此码） |
| 404 | `EnvironmentNotFound` | 环境不存在，或该环境在 `at` 时点没有任何可还原的有效配置 |

其他错误码：`InvalidRequest`（请求体或参数不合法）、`FlagNotFound`（开关不存在或无可删除配置）、`AlreadyExists`（重复创建）、`route_not_found`（路径无匹配）、`internal_error`（服务内部故障）、`storage_unavailable`（健康检查发现存储不可用）。
