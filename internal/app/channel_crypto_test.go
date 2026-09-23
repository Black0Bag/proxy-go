package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 集成：Balancer.Load 检测旧版明文 APIKey → 自动迁移为密文落盘；重启后加载仍还原明文。
func TestBalancerCredentialMigration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envMasterKey, hexKey('8'))
	t.Setenv(envPlaintextMode, "")

	path := filepath.Join(dir, "channels.json")
	legacy := []byte(`{"channels":[{"id":"ch1","name":"legacy","provider":"openai","api_key":"legacy-plain-key","weight":1}],"groups":[{"name":"g1","members":["ch1"],"strategy":"round_robin"}]}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatalf("write legacy channels: %v", err)
	}

	b := NewBalancer()
	if err := b.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	c, ok := b.Channel("ch1")
	if !ok || c.APIKey != "legacy-plain-key" {
		t.Fatalf("in-memory api key must stay plaintext, got %+v ok=%v", c, ok)
	}

	// 迁移后落盘内容不含明文，且带 enc:v1: 前缀
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migrated channels: %v", err)
	}
	serialized := string(data)
	if strings.Contains(serialized, "legacy-plain-key") {
		t.Fatalf("plaintext api key must not persist after migration:\n%s", serialized)
	}
	if !strings.Contains(serialized, encSecretPrefix) {
		t.Fatalf("migrated channels must contain %q sealed values:\n%s", encSecretPrefix, serialized)
	}

	// 模拟重启：重新加载还原为明文
	b2 := NewBalancer()
	if err := b2.Load(path); err != nil {
		t.Fatalf("reload: %v", err)
	}
	c2, ok := b2.Channel("ch1")
	if !ok || c2.APIKey != "legacy-plain-key" {
		t.Fatalf("reload must decrypt api key back, got %+v ok=%v", c2, ok)
	}
}

// 集成：Save 落盘加密、内存保持明文；Load 全链路还原。
func TestBalancerCredentialSaveEncrypted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envMasterKey, hexKey('9'))
	t.Setenv(envPlaintextMode, "")

	path := filepath.Join(dir, "channels.json")
	b := NewBalancer()
	b.UpsertChannel(Channel{ID: "ch9", Name: "n9", Provider: "openai", APIKey: "secret-key-9", Weight: 1})
	b.UpsertGroup(ModelGroup{Name: "g9", Members: []string{"ch9"}, Strategy: "round_robin"})
	if err := b.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 内存仍是明文（快照加密不污染运行时状态）
	c, ok := b.Channel("ch9")
	if !ok || c.APIKey != "secret-key-9" {
		t.Fatalf("in-memory key must stay plaintext after Save, got %+v ok=%v", c, ok)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat channels: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("channels file perm must be 0600, got %v", fi.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read channels: %v", err)
	}
	serialized := string(data)
	if strings.Contains(serialized, "secret-key-9") {
		t.Fatalf("file must not contain plaintext key:\n%s", serialized)
	}
	if !strings.Contains(serialized, encSecretPrefix) {
		t.Fatalf("file must contain %q sealed value:\n%s", encSecretPrefix, serialized)
	}

	// Load 还原
	b2 := NewBalancer()
	if err := b2.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	c2, ok := b2.Channel("ch9")
	if !ok || c2.APIKey != "secret-key-9" {
		t.Fatalf("Load must decrypt key back, got %+v ok=%v", c2, ok)
	}
}

// 集成：解密失败时单字段置空 + 配置正常加载（不 panic、不失败）；明文字段仍可用并触发迁移。
func TestBalancerDecryptFailureClearsFieldOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envMasterKey, hexKey('b'))
	sealedKey := sealSecret("real-key")
	t.Setenv(envMasterKey, hexKey('c')) // 换密钥，模拟密钥丢失/不匹配

	path := filepath.Join(dir, "channels.json")
	raw, err := json.Marshal(map[string]any{
		"channels": []map[string]any{
			{"id": "ch_bad", "provider": "openai", "api_key": sealedKey},
			{"id": "ch_empty", "provider": "openai", "api_key": ""},
			{"id": "ch_ok", "provider": "openai", "api_key": "plain-ok"},
		},
		"groups": []map[string]any{
			{"name": "g", "members": []string{"ch_bad", "ch_ok"}, "strategy": "round_robin"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write channels: %v", err)
	}

	b := NewBalancer()
	if err := b.Load(path); err != nil { // 不得 panic、不得失败
		t.Fatalf("Load must succeed when individual fields fail to decrypt: %v", err)
	}
	cBad, ok := b.Channel("ch_bad")
	if !ok {
		t.Fatal("channel with undecryptable key must survive")
	}
	if cBad.APIKey != "" {
		t.Fatalf("undecryptable api key must be cleared, got %q", cBad.APIKey)
	}
	cEmpty, ok := b.Channel("ch_empty")
	if !ok || cEmpty.APIKey != "" {
		t.Fatalf("empty api key must stay empty, got %+v ok=%v", cEmpty, ok)
	}
	cOk, ok := b.Channel("ch_ok")
	if !ok || cOk.APIKey != "plain-ok" {
		t.Fatalf("plaintext key must pass through, got %+v ok=%v", cOk, ok)
	}
}
