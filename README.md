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
- `POST /api/v1/flags` —— 注册功能开关，请求体 `{"key":"checkout"}`，可选带 `description` 与 `labels`：
  - `description` 去除首尾空白后为 1–512 个 Unicode 字符；省略时为空。
  - `labels` 每个值匹配 `[a-z0-9_-]{1,32}`，自动去重，最多 20 个，按字典序返回；省略时为空数组。
  - 响应返回 `key`、`description`、`labels`、`created_at`、`updated_at`；新建开关 `updated_at` 与 `created_at` 相同。
- `GET /api/v1/flags` —— 列出全部开关标识（仅标识数组，不含定义详情）。
- `GET /api/v1/flags/{flagKey}` —— 返回单个开关的定义详情（同创建响应字段）；未知开关返回 404 `FlagNotFound`。
- `PUT /api/v1/flags/{flagKey}/definition` —— 整体替换开关定义：
  - 请求体必须同时提供 `description` 与 `labels`，并适用与创建相同的取值规则；不接受 `key` 字段。
  - 替换成功后 `created_at` 不变、`updated_at` 更新，重复相同替换内容一致；不生成任何配置版本。
  - 未知开关返回 404 `FlagNotFound`。
- `GET /api/v1/flag-definitions` —— 开关定义文本检索（纯查询，不写入）：
  - `q`：忽略大小写匹配 `key` 或 `description` 的子串；空白或省略视为未提供。
  - `label`：可重复提供，多个标签必须同时命中（AND）；非法标签返回 400 `InvalidRequest`。
  - 无条件时返回全部定义，按 `key` 升序；响应为 `{"definitions":[…]}`，每项与定义详情同形。
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

### 按历史时点还原配置并评估（本次新增）

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

### 单次开关判定解释（本次新增）

`GET /api/v1/environments/{environment}/flags/{flagKey}/explain?marker=alpha&at=2026-01-01T05:00:00Z`

只解释一个开关对一个标记的一次判定，是纯查询：不追加任何变更记录，相同输入重复查询结果完全相同。

- `marker`（必填）沿用统一标记格式；缺失、为空或非法均返回 `InvalidMarker`。
- `at`（可选）缺省时按服务当前时刻判定；提供时按 RFC 3339 绝对时刻还原 `changed_at` 不晚于该时点的最后一条配置版本（同时刻按写入顺序取最后一条）。提供但为空或非法时返回 `InvalidTimestamp`。

响应字段固定为 `environment`、`flag_key`、`marker`、`evaluated_at`、`status`、`reason`、`config`：

```json
{
  "environment": "prod",
  "flag_key": "checkout",
  "marker": "alpha",
  "evaluated_at": "2026-01-01T05:00:00Z",
  "status": "on",
  "reason": "enabled",
  "config": {"version":"cfg-…","enabled":true,"percentage":50,
             "window":{"starts_at":null,"ends_at":null}}
}
```

- `status` 只使用 `on`、`off`、`unconfigured`；`reason` 只使用 `enabled`、`disabled`、`window_inactive`、`rollout_miss`、`unconfigured`。
- 判定顺序固定：无配置（没有任何不晚于 `at` 的版本）或最后版本为墓碑 → `unconfigured` / `unconfigured` 且 `config` 为 `null`；否则 `enabled=false` → `disabled`；窗口不覆盖判定时点（`starts_at <= 时点 < ends_at`）→ `window_inactive`；灰度分桶未命中 → `rollout_miss`；其余 → `on` / `enabled`。
- 有配置时 `config` 含 `version`、`enabled`、`percentage`、`window`；空窗口端点仍为 `null`。`version` 可与变更历史中的版本直接对照。
- `at` 早于首条配置时仍返回 200 且 `reason` 为 `unconfigured`，不倒灌后续配置；时间窗口与灰度分桶均沿用既有规则。

校验顺序固定为：路径参数 → `at` → `marker` → 环境 → 开关。不合法路径返回 400 `InvalidRequest`，未知环境返回 404 `EnvironmentNotFound`，未知开关返回 404 `FlagNotFound`；其他服务错误沿用统一的单个 `error` 对象。

### 跨开关变更审计（本次新增）

`GET /api/v1/environments/{environment}/changes?flagKey=checkout&from=2026-01-01T00:00:00Z&to=2026-02-01T00:00:00Z`

按变更顺序审计一个环境内全部（或单个）开关的配置变更，是纯查询：不追加任何变更记录，相同输入重复查询结果完全相同。

- 路径参数 `environment`：目标环境标识，非法返回 400 `InvalidRequest`。
- `flagKey`（可选）：设置后只查该开关；非法返回 400 `InvalidRequest`（不与 `FlagNotFound` 混淆），未知开关返回 404 `FlagNotFound`。
- `from`、`to`（可选）：RFC 3339，按 `changed_at` 过滤，`from` 含、`to` 不含；为空或非法返回 400 `InvalidTimestamp`，同时给出且 `from` 不早于 `to` 同样返回 400 `InvalidTimestamp`。
- 校验顺序固定为：路径参数 → `from` → `to` → `flagKey` → 环境 → 开关；未知环境返回 404 `EnvironmentNotFound`。
- 结果按 `changed_at` 升序，同一时刻按写入先后排列；无匹配变更时仍返回 200，`items` 为空数组。

响应顶层固定含 `environment`、`items`。每项含 `flag_key`、`version`、`changed_at`、`action`、`changed_fields`、`before`、`after`：

- `action` 仅为 `created`、`updated`、`deleted`：无前序有效配置或前序为墓碑时出现的新有效配置记为 `created`；两个连续有效配置之间记为 `updated`；墓碑记为 `deleted`。`from`/`to` 只影响输出范围，动作判定始终基于完整追加链。
- `before`、`after` 为配置对象或 `null`，配置对象含 `enabled`、`percentage`、`window`，`window` 含 `starts_at`、`ends_at`，空端点为 `null`；`created` 的 `before` 与 `deleted` 的 `after` 为 `null`。
- `changed_fields` 按 `enabled`、`percentage`、`window.starts_at`、`window.ends_at` 固定顺序：`created` 列非空字段，`updated` 列变化字段，`deleted` 列删除前非空字段。

### 跨环境同刻对比（本次新增）

`GET /api/v1/compare/{flagKey}?source=prod&target=staging&at=2026-01-01T05:00:00Z&marker=alpha`

对比同一个开关在两个不同环境、同一历史时点的配置，是纯查询：不追加任何变更记录，相同输入重复查询结果完全相同。

- 路径参数 `flagKey`：开关标识，沿用开关标识规则；非法返回 400 `InvalidRequest`，未知开关返回 404 `FlagNotFound`。
- 查询参数 `source`、`target`（必填）：两个不同的环境标识；缺失、非法返回 400 `InvalidRequest`，相同也返回 400 `InvalidRequest`；任一环境不存在返回 404 `EnvironmentNotFound`（先校验 source 再校验 target）。
- 查询参数 `at`（必填）：RFC 3339 历史时点；缺失或非法返回 400 `InvalidTimestamp`。
- 查询参数 `marker`（可选）：用户标记，沿用标记格式；非法返回 400 `InvalidMarker`。
- 校验顺序固定为：`flagKey` → `source`/`target` → `at` → `marker` → 环境 → 开关。

版本选择：每侧各取该环境内 `changed_at <= at` 的最后一条记录（同一时刻按写入顺序取后者）；最后一条是墓碑或此前没有任何记录时，该侧视为无有效配置。该时点之后的写入不会被沿用。

响应顶层固定包含 `source`、`target`、`flag_key`、`at`、`marker`、`changed_fields`、`comparison`、`source_status`、`target_status`：

- `source`、`target` 为含 `version`、`enabled`、`percentage`、`window` 的配置对象，或无有效配置时为 `null`；`window` 空端点为 `null`。
- `changed_fields` 按 `enabled`、`percentage`、`window.starts_at`、`window.ends_at` 固定顺序列出差异，`version` 不参与；仅一侧有配置时列出该侧非空字段，两边均无配置时为 `[]`。
- `comparison` 仅取 `same`（两侧有效配置相同）、`different`（两侧有效配置不同）、`only_source`（仅来源有配置）、`only_target`（仅目标有配置）、`unconfigured_both`（两边均无配置）。
- 提供 `marker` 时 `source_status`、`target_status` 按禁用、窗口、灰度顺序分别判定，返回 `on`、`off`、`unconfigured`（分桶仍以各自环境标识为输入）；未提供 `marker` 时两者均为 `not_evaluated`，`marker` 字段为 `null`。

## 错误约定

所有错误响应都是单个顶层 `error` 对象，包含 `code` 与 `message` 两个字符串字段；`message` 不包含 SQL、堆栈或文件路径。

历史时点查询的边界结果唯一：

| HTTP | code | 触发条件 |
|---|---|---|
| 400 | `InvalidTimestamp` | `at`、`from`、`to`（或配置窗口）时间戳缺失或无法按 RFC 3339 解析；变更审计中 `from` 不早于 `to` |
| 400 | `InvalidMarker` | `marker` 不符合标记格式（实时评估缺失标记同样返回此码） |
| 404 | `EnvironmentNotFound` | 环境不存在，或该环境在 `at` 时点没有任何可还原的有效配置 |

其他错误码：`InvalidRequest`（请求体或参数不合法）、`FlagNotFound`（开关不存在或无可删除配置）、`AlreadyExists`（重复创建）、`route_not_found`（路径无匹配）、`internal_error`（服务内部故障）、`storage_unavailable`（健康检查发现存储不可用）。
