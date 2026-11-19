package client

import (
	"os"
	"path/filepath"
	"runtime"
)

// 应用目录（多个函数的唯一出口，其它地方不要自己拼 HOME / AppData）：
//
//	ConfigDir  —— 用户可编辑的设置（服务器地址、主题…）。小、可读、可漫游。
//	DataDir    —— 不可丢的本机数据（会话、消息、将来的 SQLite）。只增不减。
//	StateDir   —— 重启后需要延续、但丢了也不致命的状态（窗口位置、上次会话、日志）。
//	CacheDir   —— 随时可删（头像、HTTP 缓存、WebView 浏览器数据）。
//	SecretDir  —— 凭据（token）。目标是系统 keychain；文件只是兜底。
//
// 使用现状（不要以为没用就是可以删）：
//
//	DataDir / CacheDir 已在使用；ConfigDir / StateDir / SecretDir 已预留，
//	接功能时直接调用即可，不要另写一套路径解析。
//
// 平台映射见各函数注释。Windows 一律用 Local 而不是 Roaming：消息库会持续
// 增长，放 Roaming 会被域漫游配置同步到服务器（拖慢登录），而且在网络盘上跑
// SQLite（-wal/-shm）容易损坏。代价：Data/State/Cache/Secret 都不跟随漫游；
// Config 用 %APPDATA% 保持“小配置可漫游”的 Windows 惯例。
const appDirName = "rym-desktop"

// ConfigDir 返回用户设置目录（不创建）：
//
//	Windows  %APPDATA%\rym-desktop
//	macOS    ~/Library/Application Support/rym-desktop
//	Linux    $XDG_CONFIG_HOME/rym-desktop 或 ~/.config/rym-desktop
//
// 三平台都恰好等于 os.UserConfigDir()/rym-desktop，所以直接复用标准库
// （它已处理 XDG 相对路径、%AppData% 缺失等边界）。
// 注意：macOS 上它与 DataDir 是同一路径——Apple 没有单独的“配置文件目录”，
// 非 plist 的应用文件都放 Application Support，这是平台事实，不是疏漏。
func ConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appDirName), nil
}

// DataDir 返回本机数据目录（不创建）：
//
//	Windows  %LOCALAPPDATA%\rym-desktop
//	macOS    ~/Library/Application Support/rym-desktop
//	Linux    $XDG_DATA_HOME/rym-desktop 或 ~/.local/share/rym-desktop
func DataDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		// %LOCALAPPDATA% 是 Windows 给“本机、不漫游”数据的目录；
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
		// Apple 规定应用自建的支持文件放 Application Support。
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(base, appDirName), nil

	default:
		base, err := xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share"))
		if err != nil {
			return "", err
		}
		return filepath.Join(base, appDirName), nil
	}
}

// StateDir 返回运行期状态目录（不创建）：
//
//	Windows  %LOCALAPPDATA%\rym-desktop      （与 DataDir 同根）
//	macOS    ~/Library/Application Support/rym-desktop（与 DataDir 同根）
//	Linux    $XDG_STATE_HOME/rym-desktop 或 ~/.local/state/rym-desktop
//
// 只有 Linux 有专门的 state 约定（XDG_STATE_HOME，放日志/历史这类
// “该持久但不该备份”的东西）；Windows/macOS 没有对应概念，与数据同根即可，
// 不为了“对称”硬造一层子目录。
func StateDir() (string, error) {
	switch runtime.GOOS {
	case "windows", "darwin":
		return DataDir()
	default:
		base, err := xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
		if err != nil {
			return "", err
		}
		return filepath.Join(base, appDirName), nil
	}
}

// CacheDir 返回可随时清空的缓存目录（不创建）：
//
//	Windows  %LOCALAPPDATA%\rym-desktop\cache （数据目录下的子目录，
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

// SecretDir 返回凭据目录（不创建）：
//
//	所有平台都是 <DataDir>/secret（Windows 在 %LOCALAPPDATA% 下，不漫游）。
//
// 目标是系统凭据库（Windows Credential Manager / macOS Keychain /
// Linux Secret Service），文件只是不可用时的兜底。兜底文件必须放在
// 0700 的私有目录里、0600 写入；调用方先 EnsureDir(dir, 0o700)。
//
// 为什么放在 DataDir 下而不单独开一套平台映射：凭据是本机数据，
// 生命周期与数据目录一致（登出清空、卸载删除），拆开只会多一处平台分支。
func SecretDir() (string, error) {
	data, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "secret"), nil
}

// DefaultDBPath 返回本地数据库路径（DataDir 下的 rym.db）。
//
// 同目录的旧版 store.json 会在首次打开时导入（见 legacy.go）；
// 不做路径自动迁移——项目还没有真实用户，多一段搬迁代码只会让
// “到底读哪个文件”多一个分支。
func DefaultDBPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rym.db"), nil
}

// EnsureDir 创建目录（含父目录），权限只作用于新建目录。
//
// 故意不 chmod 已存在的目录：调用方可能传进来一个共享目录（测试里的
// t.TempDir()、或用户自选路径），擅自改权限会伤到别人的数据。
func EnsureDir(dir string, perm os.FileMode) error {
	return os.MkdirAll(dir, perm)
}

// xdgDir 解析 XDG 基础目录：只有显式设置且为绝对路径才采用
// （相对路径按 XDG 规范视为未设置），否则用 home 下的标准默认值。
func xdgDir(envKey, fallback string) (string, error) {
	if dir := os.Getenv(envKey); dir != "" && filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, fallback), nil
}
