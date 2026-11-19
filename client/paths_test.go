package client

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDataDir(t *testing.T) {
	dir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Fatalf("DataDir 应为绝对路径，得到 %q", dir)
	}
	if filepath.Base(dir) != appDirName {
		t.Fatalf("DataDir 末级目录应为 %q，得到 %q", appDirName, dir)
	}
}

func TestDataDirHonoursXDG(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG 只影响 Linux/BSD")
	}
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	dir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if want := filepath.Join(root, appDirName); dir != want {
		t.Fatalf("DataDir = %q，期望 %q", dir, want)
	}

	// XDG 规范：相对路径视为未设置，不能拿它拼目录。
	t.Setenv("XDG_DATA_HOME", "relative/dir")
	dir, err = DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if dir != "" && filepath.IsAbs(dir) == false {
		t.Fatalf("相对 XDG_DATA_HOME 不应被采用，得到 %q", dir)
	}
}

func TestConfigDir(t *testing.T) {
	dir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if !filepath.IsAbs(dir) || filepath.Base(dir) != appDirName {
		t.Fatalf("ConfigDir = %q，期望绝对路径且末级为 %q", dir, appDirName)
	}
}

func TestConfigDirHonoursXDG(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG 只影响 Linux/BSD")
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	dir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if want := filepath.Join(root, appDirName); dir != want {
		t.Fatalf("ConfigDir = %q，期望 %q", dir, want)
	}
}

// macOS/Windows 没有独立的 state 目录约定，跟数据目录同根；
// Linux 走 XDG_STATE_HOME。
func TestStateDir(t *testing.T) {
	dir, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Fatalf("StateDir 应为绝对路径，得到 %q", dir)
	}
	if runtime.GOOS == "linux" {
		root := t.TempDir()
		t.Setenv("XDG_STATE_HOME", root)
		dir, err = StateDir()
		if err != nil {
			t.Fatalf("StateDir: %v", err)
		}
		if want := filepath.Join(root, appDirName); dir != want {
			t.Fatalf("StateDir = %q，期望 %q", dir, want)
		}
		return
	}
	data, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if dir != data {
		t.Fatalf("StateDir = %q，期望与 DataDir 相同（%q）", dir, data)
	}
}

func TestSecretDir(t *testing.T) {
	dir, err := SecretDir()
	if err != nil {
		t.Fatalf("SecretDir: %v", err)
	}
	data, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if want := filepath.Join(data, "secret"); dir != want {
		t.Fatalf("SecretDir = %q，期望 %q", dir, want)
	}
}

func TestEnsureDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := EnsureDir(dir, 0o700); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%q 不是目录", dir)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("新建目录权限 = %o，期望 700", perm)
		}
	}
	// 重复调用必须幂等。
	if err := EnsureDir(dir, 0o700); err != nil {
		t.Fatalf("EnsureDir 重复调用: %v", err)
	}
}

func TestCacheDir(t *testing.T) {
	dir, err := CacheDir()
	if err != nil {
		t.Fatalf("CacheDir: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Fatalf("CacheDir 应为绝对路径，得到 %q", dir)
	}
	// Windows 上缓存放进数据目录的子目录，避免清缓存时误删消息库。
	if runtime.GOOS == "windows" {
		data, err := DataDir()
		if err != nil {
			t.Fatalf("DataDir: %v", err)
		}
		if want := filepath.Join(data, "cache"); dir != want {
			t.Fatalf("CacheDir = %q，期望 %q", dir, want)
		}
		return
	}
	if filepath.Base(dir) != appDirName {
		t.Fatalf("CacheDir 末级目录应为 %q，得到 %q", appDirName, dir)
	}
}

func TestDefaultStorePath(t *testing.T) {
	path, err := DefaultStorePath()
	if err != nil {
		t.Fatalf("DefaultStorePath: %v", err)
	}
	dir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if want := filepath.Join(dir, "store.json"); path != want {
		t.Fatalf("DefaultStorePath = %q，期望 %q", path, want)
	}
}

// store.json 里有 token：目录必须 0700、文件必须 0600。
func TestStorePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不使用 POSIX 权限位")
	}
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "store.json")

	s := NewStore(path)
	s.SetSession(&Session{Token: "secret"})

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat store.json: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("store.json 权限 = %o，期望 600", perm)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat 数据目录: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("数据目录权限 = %o，期望 700", perm)
	}
}
