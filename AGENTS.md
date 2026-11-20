# AGENTS.md — rym-desktop

本文件适用于 `rym-desktop` 仓库。服务端在 `../rym`，客户端协议以
`../rym/docs/client-protocol.md` 为准。

## 技术栈（已定）

- **Wails v3，始终 latest。** 不锁小版本；破坏性变更当轮修复。
- React + TypeScript + Vite。
- Go 客户端核心负责 HTTP / WS / 重连 / 缓存；**前端不直接发网络请求**。
- 移动端是独立 Flutter 仓库，不在本仓库。

## 分层

```
frontend/          React UI（只通过 Wails bindings/events 与 Go 通信）
chatservice.go     Wails 服务：client -> 前端绑定
client/            Go 核心：HTTP / WS / Store / Sync
main.go            Wails 入口
```

硬约束：

- 前端不接触 token；token 只在 Go 侧。
- WS 用 `Authorization: Bearer`，桌面端不需要 ticket。
- Go 核心可以使用 `github.com/CycleZero/rym/api/...` 生成的 HTTP client；
  只 import 服务端的 `api/` 包，不 import `app/`、`pkg/` 业务实现。
- 生成客户端默认 `application/protojson` 且假设裸响应；必须通过
  `client/http.go` 的 `jsonOpts` / `decodeEnvelope` / `decodeAPIError` 适配。
- 不在 client 里引入 Wails 依赖（保证 `go test ./client` 可独立跑）。

## 当前 MVP 与代价

- 本地缓存是 SQLite（`modernc.org/sqlite`，纯 Go）：`DataDir()/rym.db`，
  目录 0700 / 库文件 0600；旧版 `store.json` 首次打开时导入并改名 `.imported`。
- token 走系统凭据库（`client/secret.go`），不可用时退到 `SecretDir()/token`；
  数据库里**不存** token。
- 发送走 outbox：先落库再后台发，网络错误指数退避、4xx 标 failed；
  这条路径已实现并有单测，别再退回“同步发完才返回”。
- 托盘/通知已接：关闭窗口收进托盘；窗口不可见/失焦时发通知。
- 没有群聊/媒体/撤回/离线推送。

## 本地目录（不要自由发挥）

五个目录全部定义在 `client/paths.go`，**其它地方不得再拼 `HOME` / `AppData` /
`XDG_*`**：

| 函数 | Windows | macOS | Linux | 现状 |
|---|---|---|---|---|
| `ConfigDir()` | `%APPDATA%\rym-desktop` | `~/Library/Application Support/rym-desktop` | `$XDG_CONFIG_HOME/rym-desktop` | 预留 |
| `DataDir()` | `%LOCALAPPDATA%\rym-desktop` | `~/Library/Application Support/rym-desktop` | `$XDG_DATA_HOME/rym-desktop` | 已用 |
| `StateDir()` | 同 DataDir | 同 DataDir | `$XDG_STATE_HOME/rym-desktop` | 预留 |
| `CacheDir()` | `<DataDir>\cache` | `~/Library/Caches/rym-desktop` | `$XDG_CACHE_HOME/rym-desktop` | 已用 |
| `SecretDir()` | `<DataDir>\secret` | `<DataDir>/secret` | `<DataDir>/secret` | 预留 |

硬约束：

- `ConfigDir` / `StateDir` / `SecretDir` 现在没有调用方，但**不是死代码**：
  加设置项、日志、keychain 时直接调用，不要新增路径解析或平台分支。
- 语义别串：用户可编辑设置进 Config，消息/会话/DB 进 Data，窗口位置/日志进 State，
  可删的进 Cache，凭据进 Secret（文件兜底时必须 `EnsureDir(dir, 0o700)` + 0600）。
- **Windows 一律用 Local，不用 Roaming**：SQLite 在漫游网络盘上会损坏，且消息库会让
  域漫游配置膨胀。只有 Config 用 `%APPDATA%`（小配置可漫游）。
- macOS 上 Config/Data/State 同路径是 Apple 的约定，不是 bug，不要再拆子目录。
- 路径解析不读业务配置、不做环境变量覆盖（XDG 是平台约定，不是我们的配置）；
  测试隔离直接给 `New(storePath)` 传临时路径。
- `CacheDir()` 必须能整体删除而不影响登录态与消息。
- `EnsureDir` 只对新建目录生效，**不要**改成顺手 chmod 已有目录：调用方可能传共享目录。
- `store.json` 是旧格式，只读不写；兜底 token 文件必须 0600、目录 0700。

## 本地存储（SQLite）

`client/db.go` 打开库并建表，`client/store.go` 是全部读写，`client/legacy.go` 只负责旧 JSON 导入。

硬约束：

- 缓存写入失败不阻断 UI（可以按游标从服务端重建）；但**不要**把 Store 当事实来源。
- 消息幂等靠唯一索引 `(conversation_id, msg_id)`，顺序靠主键 `(conversation_id, seq)`；
  先查后插会漏掉并发重发。
- 水位（`last_seq` / `read_seq` / `peer_read_seq`）只能取 max，不能直接覆盖：
  服务端摘要乱序到达会让客户端“重新发现”已读消息或误判漏消息。
- 展示字段（`unread_count` / `version` / `pinned` / `muted`）以最新一份摘要为准，含零值。
- 表结构版本用 `PRAGMA user_version`：升级加增量 SQL + 版本号 +1，不改已发布语句。
- 库文件必须 0600、目录 0700（库里虽然不再有 token，但仍有全部聊天内容）。
  SQLite 默认按 umask 建文件，所以 `NewStore` 里先 `Ping` 建文件再 `chmod`，
  且必须在开 WAL 之前。
- **token 只在 `tokenStore` 里**：`Store.SetSession` 会抹掉 token 再落库。
  新增持久化字段时别把凭据塞进 SQLite。
- 令牌存储是“凭据库优先、0600 文件兜底”：`Set` 成功要删明文兜底；
  `Delete` 里凭据库不可达不算失败，只有兜底文件的错误必须上报。
- 测试必须注入内存 `tokenStore`（`newClient(path, &memoryTokenStore{})`），
  禁止让 `go test` 碰真实凭据库或写 `$HOME`。
- `SetMaxOpenConns(1)`：**`rows` 没关之前不要再发查询**，否则自己等自己死锁。
  （读出来的行先 `Close`/读完，再做下一件事。`Messages` 合并 outbox 就是这样做的。）
- 发送队列 `outbox`：
  - 未确认消息**不进 messages**（没有 seq），读的时候由 `Store.Messages` 合并到列表尾部；
  - 入队顺序用 `created_at, rowid` 排序，**不要用 `client_msg_id` 当 tiebreak**：
    同一毫秒连发两条时随机 UUID 会让它们换位，进而拿反 seq；
  - 只有 `sending` 会被调度，`failed` 留给 UI 展示，不自动重试；
  - 4xx 才标 failed；401 必须保留（token 失效不是消息的错），网络/429/5xx 走退避；
  - 确认用 `ResolveOutbox`（同事务写 messages + 删队列），服务端回推带
    `client_msg_id` 的消息时也直接清队列项（HTTP 响应可能丢）；
  - 401 只清令牌、保留队列（token 过期不该丢用户已发的消息）；
    换账号时必须清库——`Login` 按缓存的 user_id 判断，否则会用新账号
    把上一个账号的待发消息发出去。

## 坑

- Kratos 生成客户端用 protoreflect 的 JSON name 替换 path 变量。
  proto 的 path 字段如果是 snake_case（如 `conversation_id`），必须显式
  `[json_name = "conversation_id"]`，否则会拼出 `/conversations//read`。
- `khttp` 的 errorDecoder 对 2xx 也会调用；2xx 必须直接返回 nil 且不能读 body，
  否则 responseDecoder 拿不到响应体。
- nil 指针装进 `any` 之后不等于 nil，`json.Marshal` 会得到 `"null"`。
  存 `peer` / `last_message` 这类可空字段时先判空，别指望 `v == nil`。

## 构建

```bash
cd frontend && npm install
wails3 dev
wails3 build
```

Windows（交叉编译，无需 cgo）：

```bash
wails3 build GOOS=windows
wails3 package GOOS=windows INSTALL_SCOPE=user
```

## Windows 实机注意

- 窗口 `Width/Height` 是 **DIP**，实际像素要乘系统缩放（150% → 1.5x）。
  用 PowerShell 之类 DPI-unaware 进程截图时，`GetWindowRect` 返回的是虚拟化坐标，
  直接按它建位图会截掉右/下部分；要乘 `GetDpiForWindow/96` 再 `PrintWindow`。
- WebView2 数据目录通过 `Windows.WebviewUserDataPath` 指到 `CacheDir()/webview`，
  不要让它落回 `%APPDATA%`（Roaming）。
- 登录页等高卡片必须放在可滚动容器里：小窗口 / 高缩放下不能依赖垂直居中，
  否则按钮会被挤出可视区且无法触达。

WSL 下如果默认 gcc 是 conda 的：

```bash
CC=/usr/bin/gcc wails3 build
```

Windows 需要 WebView2；Linux 需要 GTK4 + WebKitGTK 6.0。

## 测试

```bash
go test ./client
```

真实服务端 E2E（默认 skip）：

```bash
RYM_E2E_BASE=http://127.0.0.1:18082 \
RYM_E2E_WS_PORT=18083 \
go test ./client -run TestE2E -v
```

## 提交

- Conventional Commits + 中文描述。
- 改协议行为时先改 `../rym/docs/client-protocol.md`。
- 生成物 `frontend/bindings/` 入库；`bin/`、`frontend/dist/`、`node_modules/` 不入库。
