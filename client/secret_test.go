package client

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// memoryTokenStore 只在测试里用：不碰真实凭据库，也不写 HOME。
type memoryTokenStore struct {
	token string
}

func (m *memoryTokenStore) Get() (string, error) {
	if m.token == "" {
		return "", errNoToken
	}
	return m.token, nil
}

func (m *memoryTokenStore) Set(token string) error {
	m.token = token
	return nil
}

func (m *memoryTokenStore) Delete() error {
	m.token = ""
	return nil
}

// stubTokenStore 模拟“凭据库可用/不可用”两种状态。
type stubTokenStore struct {
	token string
	err   error
}

func (s *stubTokenStore) Get() (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if s.token == "" {
		return "", errNoToken
	}
	return s.token, nil
}

func (s *stubTokenStore) Set(token string) error {
	if s.err != nil {
		return s.err
	}
	s.token = token
	return nil
}

func (s *stubTokenStore) Delete() error {
	if s.err != nil {
		return s.err
	}
	s.token = ""
	return nil
}

func TestFileTokenStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secret")
	store := fileTokenStore{path: filepath.Join(dir, "token")}

	if _, err := store.Get(); !errors.Is(err, errNoToken) {
		t.Fatalf("没有令牌时应返回 errNoToken，实际 %v", err)
	}

	if err := store.Set("t1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if token, err := store.Get(); err != nil || token != "t1" {
		t.Fatalf("Get = %q, %v", token, err)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(store.path)
		if err != nil {
			t.Fatalf("stat token: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("token 文件权限 = %o，期望 600", perm)
		}
		dirInfo, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat secret 目录: %v", err)
		}
		if perm := dirInfo.Mode().Perm(); perm != 0o700 {
			t.Fatalf("secret 目录权限 = %o，期望 700", perm)
		}
	}

	if err := store.Set("t2"); err != nil {
		t.Fatalf("覆盖写: %v", err)
	}
	if token, _ := store.Get(); token != "t2" {
		t.Fatalf("覆盖写后 Get = %q", token)
	}

	if err := store.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete 应幂等: %v", err)
	}
	if _, err := store.Get(); !errors.Is(err, errNoToken) {
		t.Fatalf("删除后应返回 errNoToken，实际 %v", err)
	}
}

// 凭据库不可用（无桌面会话的 Linux、WSL、容器）时退到 0600 文件。
func TestHybridTokenStoreFallsBackToFile(t *testing.T) {
	file := fileTokenStore{path: filepath.Join(t.TempDir(), "secret", "token")}
	store := hybridTokenStore{
		keyring: &stubTokenStore{err: errors.New("dbus 不可用")},
		file:    file,
	}

	if err := store.Set("fallback"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if token, err := store.Get(); err != nil || token != "fallback" {
		t.Fatalf("Get = %q, %v", token, err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(); !errors.Is(err, errNoToken) {
		t.Fatalf("删除后应返回 errNoToken，实际 %v", err)
	}
}

// 凭据库可用时优先用它，并清掉可能残留的明文兜底文件。
func TestHybridTokenStorePrefersKeyring(t *testing.T) {
	keyring := &stubTokenStore{token: "keyring-token"}
	file := fileTokenStore{path: filepath.Join(t.TempDir(), "secret", "token")}
	if err := file.Set("stale-file-token"); err != nil {
		t.Fatalf("准备兜底文件: %v", err)
	}
	store := hybridTokenStore{keyring: keyring, file: file}

	if token, err := store.Get(); err != nil || token != "keyring-token" {
		t.Fatalf("Get = %q, %v", token, err)
	}
	if err := store.Set("new-token"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if keyring.token != "new-token" {
		t.Fatalf("凭据库未更新：%q", keyring.token)
	}
	if _, err := file.Get(); !errors.Is(err, errNoToken) {
		t.Fatalf("凭据库可用时不应保留明文 token 文件")
	}
}
