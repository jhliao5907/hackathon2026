// Package secrets 在宿主机的操作系统密钥库里安全地存取密钥。
//
// macOS 走 Keychain，Linux 走 Secret Service，Windows 走 Credential Manager
// （由 go-keyring 统一抽象）。密钥永不写入任何配置文件、永不进入容器镜像，
// 也永不暴露给沙箱内的 agent —— 这是「MCP 配置零敏感信息」的基础。
package secrets

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// service 是写入系统密钥库时使用的命名空间，便于区分与清理。
const service = "desktop-sandbox"

// ErrNotFound 表示请求的密钥不存在。
var ErrNotFound = errors.New("密钥不存在")

// Store 是对系统密钥库的薄封装。
type Store struct{}

// New 返回一个 Store。
func New() *Store { return &Store{} }

// Set 存入（或覆盖）一个命名密钥。
func (s *Store) Set(name, value string) error {
	if name == "" {
		return errors.New("密钥名不能为空")
	}
	if err := keyring.Set(service, name, value); err != nil {
		return fmt.Errorf("写入密钥 %q 失败: %w", name, err)
	}
	return nil
}

// Get 读取一个命名密钥；不存在时返回 ErrNotFound。
func (s *Store) Get(name string) (string, error) {
	v, err := keyring.Get(service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("读取密钥 %q 失败: %w", name, err)
	}
	return v, nil
}

// Delete 删除一个命名密钥（幂等：不存在也视为成功）。
func (s *Store) Delete(name string) error {
	err := keyring.Delete(service, name)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("删除密钥 %q 失败: %w", name, err)
	}
	return nil
}

// Has 判断密钥是否已设置。
func (s *Store) Has(name string) bool {
	_, err := s.Get(name)
	return err == nil
}
