package app

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
)

// 凭据静态加密（AES-256-GCM）。
// 落盘格式："enc:v1:" + base64(12B nonce || GCM 密文)。
//
// 主密钥来源优先级：
//  1. 环境变量 CLINE_PROXY_MASTER_KEY：64 位 hex（=32 字节）直接作为密钥；
//     非空但不是合法的 32 字节 hex 时，用 sha256(原串) 派生 32 字节；
//  2. 工作目录下 .masterkey 文件（0600，内容为 64 位 hex）；
//  3. 均不存在时生成 32 字节随机密钥并写入 .masterkey（0600）。
//
// 环境变量 CLINE_PROXY_PLAINTEXT=1 时 sealSecret/openSecret 均直通明文（调试逃生门）。

const (
	encSecretPrefix  = "enc:v1:"
	envMasterKey     = "CLINE_PROXY_MASTER_KEY"
	envPlaintextMode = "CLINE_PROXY_PLAINTEXT"
	masterKeyFile    = ".masterkey"
	masterKeySize    = 32
	gcmNonceSize     = 12
)

// masterKey 解析主密钥；无法生成或持久化密钥文件时返回错误。
func masterKey() ([]byte, error) {
	if env := strings.TrimSpace(os.Getenv(envMasterKey)); env != "" {
		if raw, err := hex.DecodeString(env); err == nil && len(raw) == masterKeySize {
			return raw, nil
		}
		// 非空但非合法 32 字节 hex：sha256 派生，保证密钥长度正确
		sum := sha256.Sum256([]byte(env))
		return sum[:], nil
	}

	if data, err := os.ReadFile(masterKeyFile); err == nil {
		raw, derr := hex.DecodeString(strings.TrimSpace(string(data)))
		if derr != nil || len(raw) != masterKeySize {
			return nil, fmt.Errorf("secretbox: %s exists but does not contain a valid %d-byte hex key", masterKeyFile, masterKeySize)
		}
		return raw, nil
	}

	key := make([]byte, masterKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("secretbox: generate master key: %w", err)
	}
	if err := os.WriteFile(masterKeyFile, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		return nil, fmt.Errorf("secretbox: persist master key: %w", err)
	}
	return key, nil
}

// isEncryptedSecret 判断字符串是否为 enc:v1: 密文格式。
func isEncryptedSecret(s string) bool {
	return strings.HasPrefix(s, encSecretPrefix)
}

// sealSecret 加密敏感字符串；空串原样返回；CLINE_PROXY_PLAINTEXT=1 或
// 主密钥/加密初始化不可用时降级原样返回（降级会打印警告日志，不 panic）。
func sealSecret(plain string) string {
	if plain == "" || os.Getenv(envPlaintextMode) == "1" {
		return plain
	}
	key, err := masterKey()
	if err != nil {
		log.Printf("secretbox: master key unavailable, falling back to plaintext: %v", err)
		return plain
	}
	aead, err := newGCM(key)
	if err != nil {
		log.Printf("secretbox: init cipher failed, falling back to plaintext: %v", err)
		return plain
	}
	nonce := make([]byte, gcmNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		log.Printf("secretbox: nonce generation failed, falling back to plaintext: %v", err)
		return plain
	}
	sealed := aead.Seal(nil, nonce, []byte(plain), nil)
	buf := make([]byte, 0, len(nonce)+len(sealed))
	buf = append(buf, nonce...)
	buf = append(buf, sealed...)
	return encSecretPrefix + base64.StdEncoding.EncodeToString(buf)
}

// openSecret 解密 enc:v1: 密文；不带前缀的原样返回（旧版明文迁移兼容）。
// 解密失败（密钥不匹配/数据损坏）返回错误，由调用方决定降级策略。
func openSecret(stored string) (string, error) {
	if !isEncryptedSecret(stored) {
		return stored, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, encSecretPrefix))
	if err != nil {
		return "", fmt.Errorf("secretbox: decode sealed value: %w", err)
	}
	if len(raw) < gcmNonceSize {
		return "", errors.New("secretbox: sealed value too short")
	}
	key, err := masterKey()
	if err != nil {
		return "", err
	}
	aead, err := newGCM(key)
	if err != nil {
		return "", err
	}
	plain, err := aead.Open(nil, raw[:gcmNonceSize], raw[gcmNonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("secretbox: open sealed value: %w", err)
	}
	return string(plain), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
