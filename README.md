# rym-desktop

rym 的桌面客户端：**Wails v3 + Go + React/TypeScript**。

- 网络、WS 重连、本地缓存都在 Go 侧（`client/`）
- HTTP 调用使用服务端 proto 生成的 `UserServiceHTTPClient` / `ImServiceHTTPClient`，
  只保留一层“信封 + 鉴权 + content-type”适配
- 前端只做 UI，通过 Wails bindings/events 与 Go 通信
- 服务端契约见 [../rym/docs/client-protocol.md](../rym/docs/client-protocol.md)

## 当前范围（MVP）

- 注册 / 登录 / 自动登录
- 会话列表（peer 昵称、最后一条、未读）
- 搜索用户、打开单聊
- 拉历史、发文本、WS 实时收消息
- 已读上报、对方已读位
- 连接状态 / 断线重连
- 系统托盘（显示/隐藏/退出）
- 关闭窗口收进托盘，不退出进程
- 窗口不可见/失焦时发系统通知，点击通知回到窗口

暂不做：群聊建群、图片/文件、撤回/编辑、离线推送、presence/正在输入。

## 目录

```
client/              Go 客户端核心（生成 HTTP client / WS / JSON Store）
chatservice.go       Wails 服务：把 client 包装成前端绑定
main.go              Wails 应用入口
frontend/            React + TypeScript + Vite
frontend/bindings/   Wails 生成的 TS 绑定（入库）
```

## 运行

前置：

- Go 1.26+（本项目用 1.27）
- Node.js 20+ / npm
- Wails v3.1：`go install github.com/wailsapp/wails/v3/cmd/wails3@latest`
- Windows：WebView2 运行时
- Linux：GTK4 + WebKitGTK 6.0
- WSL 下如果默认 gcc 是 conda 的，需要显式用系统 gcc：`CC=/usr/bin/gcc`

```bash
cd frontend && npm install
cd ..
wails3 dev          # 开发（热更新）
wails3 build        # 产物在 bin/rym-desktop
```

WSL 构建 Linux：

```bash
CC=/usr/bin/gcc wails3 build
```

## 后端配置

服务端需要开启 WS：

```yaml
server:
  ws:
    enable: true
    host: "0.0.0.0"
    port: 8001
```

登录页填写：

- 服务端地址：`http://<host>:8000`
- WS 端口：`8001`
- 用户名 / 密码

## HTTP 客户端代码生成

`client/http.go` 不手写端点，使用服务端 `api/im/v1`、`api/user/v1` 生成的客户端：

- `github.com/CycleZero/rym` 作为 module 依赖（只 import `api/`）；
- 生成客户端默认 `application/protojson`，这里用 `jsonOpts` 覆盖为 `application/json`；
- 服务端统一信封由 `decodeEnvelope` 拆 `data`，错误由 `decodeAPIError` 映射；
- 鉴权由 `authTransport` 统一加 `Authorization`。

服务端 API 变更后：

```bash
go get github.com/CycleZero/rym@<commit>
go mod tidy
```

## 托盘与通知

- 关闭窗口 = 收进托盘；从托盘菜单「退出」才结束进程。
- 只有窗口不可见或失焦时才发系统通知；自己的消息不通知。
- macOS 首次会请求通知授权；Linux 需要桌面环境支持通知区域（没有托盘时仍可用窗口模式）。

## 数据存放位置

只有两个目录（`client/paths.go`）：

| 平台 | 数据（不可删） | 缓存（可随时删） |
|---|---|---|
| Windows | `%LOCALAPPDATA%\rym-desktop` | `%LOCALAPPDATA%\rym-desktop\cache` |
| macOS | `~/Library/Application Support/rym-desktop` | `~/Library/Caches/rym-desktop` |
| Linux | `$XDG_DATA_HOME/rym-desktop`（默认 `~/.local/share/rym-desktop`） | `$XDG_CACHE_HOME/rym-desktop`（默认 `~/.cache/rym-desktop`） |

- 数据目录：`store.json`（session、会话、消息），原子写入，目录 0700 / 文件 0600；
  下一步换成 SQLite，仍放这里。
- 缓存目录：WebView 浏览器数据等，删掉只会丢缓存，不影响登录态和消息。
- Windows 用 **Local** 而不是 Roaming：消息库会持续增长，放 Roaming 会被域漫游
  同步到服务器、拖慢登录，且网络盘上跑 SQLite 容易损坏。代价是设置项也不漫游。
- Token 目前与 `store.json` 同文件，后续接系统 keychain（Linux 需要 Secret
  Service，缺失时回退到 0600 文件）。

早期版本把 store 写在 `~/.config/rym-desktop/`，不做自动迁移：老环境重新登录一次，
旧目录可以手工删除。

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

## 说明

- 桌面端 WS 直接用 `Authorization: Bearer`，不需要 ticket；
- 浏览器/Flutter Web 才需要 `POST /v1/im/ws-ticket`；
- Wails 版本跟随 latest（v3.x）；升级后跑一次登录/收消息/打包冒烟。
