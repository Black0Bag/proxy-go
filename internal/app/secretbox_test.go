package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hexKey(b byte) string { return strings.Repeat(string(rune(b)), 64) }

// 1. roundtrip：seal → open 还原；密文带前缀且不等于明文；两次 seal 结果不同（随机 nonce）。
func TestSecretBoxRoundtrip(t *testing.T) {
	t.Setenv(envMasterKey, hexKey('a'))

	plain := "workos:super-secret-token-value"
	sealed := sealSecret(plain)
	if sealed == plain {
		t.Fatalf("sealed value must differ from plaintext")
	}
	if !isEncryptedSecret(sealed) {
		t.Fatalf("sealed value missing %q prefix: %q", encSecretPrefix, sealed)
	}
	if sealed2 := sealSecret(plain); sealed2 == sealed {
		t.Fatalf("two seals of the same plaintext must differ (random nonce)")
	}

	got, err := openSecret(sealed)
	if err != nil {
		t.Fatalf("openSecret: %v", err)
	}
	if got != plain {
		t.Fatalf("roundtrip mismatch: got %q want %q", got, plain)
	}
}

// 2. 错误 masterkey：密钥 A 加密后密钥 B 无法解密。
func TestSecretBoxWrongKeyDecryptFails(t *testing.T) {
	t.Setenv(envMasterKey, hexKey('1'))
	sealed := sealSecret("s3cret")

	t.Setenv(envMasterKey, hexKey('2'))
	if got, err := openSecret(sealed); err == nil {
		t.Fatalf("openSecret with wrong master key must fail, got %q", got)
	}
}

// 3. 明文 passthrough：无 enc:v1: 前缀的值原样返回（明文迁移兼容）。
func TestSecretBoxPlaintextPassthrough(t *testing.T) {
	t.Setenv(envMasterKey, hexKey('3'))

	legacy := "legacy-plain-token"
	got, err := openSecret(legacy)
	if err != nil {
		t.Fatalf("openSecret plaintext: %v", err)
	}
	if got != legacy {
		t.Fatalf("plaintext passthrough mismatch: got %q want %q", got, legacy)
	}
}

// 4. CLINE_PROXY_PLAINTEXT=1：sealSecret 原样直通。
func TestSecretBoxPlaintextModeBypass(t *testing.T) {
	t.Setenv(envMasterKey, hexKey('4'))
	t.Setenv(envPlaintextMode, "1")

	plain := "raw-token-kept-as-is"
	if got := sealSecret(plain); got != plain {
		t.Fatalf("sealSecret in plaintext mode must pass through, got %q", got)
	}
	if got, err := openSecret(plain); err != nil || got != plain {
		t.Fatalf("openSecret in plaintext mode must pass through, got %q err %v", got, err)
	}
}

// 5. masterkey 文件：首次调用生成 .masterkey(0600)，再次调用复用同一密钥。
func TestSecretBoxMasterKeyFileGeneratedAndReused(t *testing.T) {
	t.Chdir(t.TempDir()) // 隔离工作目录，避免读到/污染真实 .masterkey
	t.Setenv(envMasterKey, "")

	k1, err := masterKey()
	if err != nil {
		t.Fatalf("masterKey generate: %v", err)
	}
	if len(k1) != masterKeySize {
		t.Fatalf("master key size = %d, want %d", len(k1), masterKeySize)
	}

	fi, err := os.Stat(masterKeyFile)
	if err != nil {
		t.Fatalf(".masterkey not generated: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf(".masterkey perm = %v, want 0600", fi.Mode().Perm())
	}
	data, err := os.ReadFile(masterKeyFile)
	if err != nil {
		t.Fatalf("read .masterkey: %v", err)
	}
	if len(strings.TrimSpace(string(data))) != masterKeySize*2 {
		t.Fatalf(".masterkey content must be %d hex chars, got %q", masterKeySize*2, data)
	}

	k2, err := masterKey()
	if err != nil {
		t.Fatalf("masterKey reuse: %v", err)
	}
	if string(k1) != string(k2) {
		t.Fatal("second masterKey() call must reuse the persisted key")
	}

	// 密钥文件模式下 seal/open 全链路可用
	sealed := sealSecret("file-key-secret")
	got, err := openSecret(sealed)
	if err != nil || got != "file-key-secret" {
		t.Fatalf("seal/open with file key failed: got %q err %v", got, err)
	}
}

// 集成：loadPool 检测旧版明文凭据 → 迁移为密文落盘；重启后加载仍还原明文。
func TestPoolCredentialMigration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envMasterKey, hexKey('5'))
	t.Setenv(envPlaintextMode, "")

	oldPath := poolPath
	poolPath = filepath.Join(dir, ".cline-accounts.json")
	t.Cleanup(func() {
		poolPath = oldPath
		pool = nil
	})

	legacy := []byte(`{"accounts":[{"accountId":"acc_1","email":"a@b.c","refreshToken":"legacy-plain-token","status":"active"}],"keys":["legacy-plain-key"],"currentIdx":0}`)
	if err := os.WriteFile(poolPath, legacy, 0o600); err != nil {
		t.Fatalf("write legacy pool: %v", err)
	}
	pool = nil

	p := loadPool()
	if len(p.Accounts) != 1 || p.Accounts[0].RefreshToken != "legacy-plain-token" {
		t.Fatalf("in-memory refresh token must stay plaintext, got %+v", p.Accounts)
	}
	if len(p.Keys) != 1 || p.Keys[0] != "legacy-plain-key" {
		t.Fatalf("in-memory key must stay plaintext, got %v", p.Keys)
	}

	// 迁移后落盘内容不含明文，且带 enc:v1: 前缀
	data, err := os.ReadFile(poolPath)
	if err != nil {
		t.Fatalf("read migrated pool: %v", err)
	}
	serialized := string(data)
	if strings.Contains(serialized, "legacy-plain-token") || strings.Contains(serialized, "legacy-plain-key") {
		t.Fatalf("plaintext credentials must not persist after migration:\n%s", serialized)
	}
	if !strings.Contains(serialized, encSecretPrefix) {
		t.Fatalf("migrated pool must contain %q sealed values:\n%s", encSecretPrefix, serialized)
	}

	// 模拟重启：重新加载还原为明文
	pool = nil
	p2 := loadPool()
	if len(p2.Accounts) != 1 || p2.Accounts[0].RefreshToken != "legacy-plain-token" {
		t.Fatalf("reload must decrypt refresh token back, got %+v", p2.Accounts)
	}
	if len(p2.Keys) != 1 || p2.Keys[0] != "legacy-plain-key" {
		t.Fatalf("reload must decrypt key back, got %v", p2.Keys)
	}
}

// 集成：解密失败时单字段置空 + 池正常加载（不 panic、不失败）。
func TestPoolDecryptFailureClearsFieldOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envMasterKey, hexKey('6'))
	sealedToken := sealSecret("real-token")
	sealedKey := sealSecret("real-key")

	t.Setenv(envMasterKey, hexKey('7')) // 换密钥，模拟密钥丢失/不匹配

	oldPath := poolPath
	poolPath = filepath.Join(dir, ".cline-accounts.json")
	t.Cleanup(func() {
		poolPath = oldPath
		pool = nil
	})

	raw, err := json.Marshal(map[string]any{
		"accounts": []map[string]any{
			{"accountId": "acc_1", "email": "a@b.c", "refreshToken": sealedToken, "status": "active"},
			{"accountId": "acc_2", "email": "c@d.e", "refreshToken": "", "status": "active"},
		},
		"keys":       []string{sealedKey},
		"currentIdx": 0,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(poolPath, raw, 0o600); err != nil {
		t.Fatalf("write pool: %v", err)
	}
	pool = nil

	p := loadPool() // 不得 panic
	if p == nil {
		t.Fatal("pool must still load when individual fields fail to decrypt")
	}
	if len(p.Accounts) != 2 {
		t.Fatalf("all accounts must survive, got %d", len(p.Accounts))
	}
	if p.Accounts[0].RefreshToken != "" {
		t.Fatalf("undecryptable refresh token must be cleared, got %q", p.Accounts[0].RefreshToken)
	}
	if p.Accounts[1].RefreshToken != "" {
		t.Fatalf("empty refresh token must stay empty, got %q", p.Accounts[1].RefreshToken)
	}
	if len(p.Keys) != 1 || p.Keys[0] != "" {
		t.Fatalf("undecryptable key must be cleared, got %v", p.Keys)
	}
}
