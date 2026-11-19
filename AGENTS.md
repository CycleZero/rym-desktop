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
- 不 import 服务端 module；只按 `docs/client-protocol.md` 的 JSON 契约工作。
- 不在 client 里引入 Wails 依赖（保证 `go test ./client` 可独立跑）。

## 当前 MVP 与代价

- 本地缓存用单个 JSON 文件（`~/.config/rym-desktop/store.json`，原子写）；
  下一步换 SQLite（`modernc.org/sqlite`，纯 Go）。
- token 与 store 同文件；下一步接系统 keychain。
- 没有 outbox 持久化；发送失败由 UI 保留输入，后续补 outbox。
- 没有群聊/媒体/撤回/离线推送。

## 构建

```bash
cd frontend && npm install
wails3 dev
wails3 build
```

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
