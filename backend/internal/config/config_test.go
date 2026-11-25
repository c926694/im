package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSecret = "yaml-test-secret-0123456789abcdef"

const testConfig = `
http:
  addr: "127.0.0.1:8080"
  mode: "release"
mysql:
  dsn: "test:pass@tcp(mysql:3306)/im_test"
auth:
  jwt_secret: "` + testSecret + `"
rocketmq:
  nameserver: "rocketmq-namesrv:9876"
  proxy: "rocketmq-broker:8081"
storage:
  avatar_dir: "test-avatars"
  chat_image_dir: "test-images"
`

func TestLoad(t *testing.T) {
	t.Setenv("IM_MYSQL_DSN", "unused")
	t.Setenv("IM_JWT_SECRET", "unused")
	got, err := Load(writeConfig(t, testConfig))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		HTTP:     HTTP{Addr: "127.0.0.1:8080", Mode: "release"},
		MySQL:    MySQL{DSN: "test:pass@tcp(mysql:3306)/im_test"},
		Auth:     Auth{JWTSecret: testSecret},
		RocketMQ: RocketMQ{NameServer: "rocketmq-namesrv:9876", Proxy: "rocketmq-broker:8081"},
		Storage:  Storage{AvatarDir: "test-avatars", ChatImageDir: "test-images"},
	}
	if got != want {
		t.Fatalf("配置与 YAML 内容不一致")
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	for _, tc := range []struct{ name, input, message string }{
		{"unknown field", strings.Replace(testConfig, "jwt_secret:", "jwt_secert:", 1), "UnknownFieldError"},
		{"duplicate key", testConfig + "mysql: {}\n", "invalid YAML config"},
		{"multiple documents", testConfig + "---\nhttp: {}\n", "one YAML document"},
		{"invalid secret", strings.Replace(testConfig, testSecret, "too-short", 1), "auth.jwt_secret"},
		{"missing database", strings.Replace(testConfig, "test:pass@tcp(mysql:3306)/im_test", "", 1), "mysql.dsn"},
		{"invalid address", strings.Replace(testConfig, "127.0.0.1:8080", "127.0.0.1:abc", 1), "http.addr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.input))
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("错误应包含 %q，实际为 %v", tc.message, err)
			}
			if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), "test:pass@") {
				t.Fatal("配置错误不应包含凭据")
			}
		})
	}
}

func TestLoadRequiresFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "config.yaml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缺少配置文件时应返回读取错误，实际为 %v", err)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
