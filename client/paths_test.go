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
