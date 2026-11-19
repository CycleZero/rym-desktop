package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

// tokenStore 保存登录令牌。
//
// 优先级：系统凭据库（Windows Credential Manager / macOS Keychain /
// Linux Secret Service）→ 不可用时退到 <SecretDir>/token（0600）。
// 退到文件是必要的：没有桌面会话的 Linux（SSH、WSL、容器）根本没有 Secret Service，
// 而桌面客户端在这些环境下依然要“下次自动登录”。
type tokenStore interface {
	Get() (string, error)
	Set(token string) error
	Delete() error
}

// errNoToken 表示“本地没有令牌”。与“凭据库不可用”区分开：
// 前者是正常状态（没登录过），后者要触发文件兜底。
var errNoToken = errors.New("本地没有登录令牌")

// keyringService 是凭据库里的一条固定记录。
//
// 一个客户端同时只保持一个登录态（换账号 = 覆盖），所以用固定 key，
// 不按 uid / server 派生条目——那会让旧令牌在凭据库里越积越多。
const (
	keyringService = "rym-desktop"
	keyringUser    = "access-token"
)

type keyringTokenStore struct{}

func (keyringTokenStore) Get() (string, error) {
	token, err := keyring.Get(keyringService, keyringUser)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", errNoToken
		}
		return "", fmt.Errorf("系统凭据库不可用: %w", err)
	}
	if token == "" {
		return "", errNoToken
	}
	return token, nil
}

func (keyringTokenStore) Set(token string) error {
	return keyring.Set(keyringService, keyringUser, token)
}

func (keyringTokenStore) Delete() error {
	if err := keyring.Delete(keyringService, keyringUser); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

// fileTokenStore 是凭据库不可用时的兜底：单个 0600 文件。
type fileTokenStore struct {
	path string
}

func (f fileTokenStore) Get() (string, error) {
	raw, err := os.ReadFile(f.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errNoToken
		}
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", errNoToken
	}
	return token, nil
}

func (f fileTokenStore) Set(token string) error {
	if err := EnsureDir(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	// 原子替换：写一半掉电也不会留下半截 token。
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

func (f fileTokenStore) Delete() error {
	if err := os.Remove(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// hybridTokenStore 先试凭据库，失败再用文件；凭据库写入成功后删掉明文兜底。
type hybridTokenStore struct {
	keyring tokenStore
	file    tokenStore
}

func (h hybridTokenStore) Get() (string, error) {
	if token, err := h.keyring.Get(); err == nil {
		return token, nil
	}
	// 凭据库不可用或没有记录：两种情况都看兜底文件（有就说明上次只能写文件）。
	return h.file.Get()
}

func (h hybridTokenStore) Set(token string) error {
	if err := h.keyring.Set(token); err == nil {
		// 凭据库可用时不在磁盘留明文 token。
		_ = h.file.Delete()
		return nil
	}
	return h.file.Set(token)
}

func (h hybridTokenStore) Delete() error {
	// 凭据库不可达时删不掉是既定事实（写的时候可能也根本没写进去），
	// 不能让它挡住登出；只有兜底文件是我们能控制的，错误才向上报。
	_ = h.keyring.Delete()
	return h.file.Delete()
}

// newTokenStore 返回默认实现：凭据库优先，<SecretDir>/token 兜底。
func newTokenStore() tokenStore {
	dir, err := SecretDir()
	if err != nil {
		// 连 secret 目录都定位不到，只能指望凭据库。
		return keyringTokenStore{}
	}
	return hybridTokenStore{
		keyring: keyringTokenStore{},
		file:    fileTokenStore{path: filepath.Join(dir, "token")},
	}
}
