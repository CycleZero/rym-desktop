package client

import (
	"os"
	"path/filepath"
	"runtime"
)

// 应用目录只分两种，够用就好：
//
//	DataDir  —— 只增不丢的本机状态：store.json（会话/消息/登录态），
//	            将来换 SQLite 也是这个目录
//	CacheDir —— 随时可删：头像、HTTP 缓存、WebView 浏览器数据
//
// 为什么不拆成 config/data/state/secret 四套：
// 客户端目前没有“用户可编辑的配置文件”，唯一持久化内容（会话、消息、token）
// 在语义上都是本机数据；多拆一层只是多几行路径解析，不会改变任何行为。
// 代价：将来加用户可编辑设置（服务器地址、主题）时，Linux/macOS 上它会和
// 消息库同目录；真要分开时再加 ConfigDir()，调用点不用改。
const appDirName = "rym-desktop"

// DataDir 返回本机持久状态目录（不创建）：
//
//	Windows  %LOCALAPPDATA%\rym-desktop
//	macOS    ~/Library/Application Support/rym-desktop
//	Linux    $XDG_DATA_HOME/rym-desktop 或 ~/.local/share/rym-desktop
//
// Windows 选 Local 而不是 Roaming：消息库会持续增长，放 Roaming 会被域漫游
// 配置同步到服务器（拖慢登录），而且在网络盘上跑 SQLite（-wal/-shm）容易损坏。
// 代价：设置项也不跟随漫游；本项目没有多机同步需求，接受。
func DataDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		// %LOCALAPPDATA% 是 Windows 官方给“本机、不漫游”数据的目录；
		// os.UserCacheDir 在 Windows 上恰好也是它，作为兜底。
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return filepath.Join(base, appDirName), nil
		}
		base, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(base, appDirName), nil

	case "darwin":
		// Apple 规定应用自建的支持文件放 Application Support；
		// Go 在 macOS 上把 UserConfigDir 映射到这里。
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(base, appDirName), nil

	default:
		// Linux/BSD 按 XDG：相对路径视为未设置（与 XDG 规范一致）。
		if dir := os.Getenv("XDG_DATA_HOME"); dir != "" && filepath.IsAbs(dir) {
			return filepath.Join(dir, appDirName), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share", appDirName), nil
	}
}

// CacheDir 返回可随时清空的本机缓存目录（不创建）：
//
//	Windows  %LOCALAPPDATA%\rym-desktop\cache （数据目录下单独一层，
//	         避免“清缓存”时误删消息库）
//	macOS    ~/Library/Caches/rym-desktop
//	Linux    $XDG_CACHE_HOME/rym-desktop 或 ~/.cache/rym-desktop
func CacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, appDirName)
	if runtime.GOOS == "windows" {
		// Windows 上 UserCacheDir == %LOCALAPPDATA%，与 DataDir 同根。
		return filepath.Join(dir, "cache"), nil
	}
	return dir, nil
}
