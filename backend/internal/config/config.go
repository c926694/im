package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

type Config struct {
	HTTP     HTTP     `yaml:"http"`
	MySQL    MySQL    `yaml:"mysql"`
	Auth     Auth     `yaml:"auth"`
	RocketMQ RocketMQ `yaml:"rocketmq"`
	Storage  Storage  `yaml:"storage"`
}

type HTTP struct {
	Addr string `yaml:"addr"`
	Mode string `yaml:"mode"`
}

type MySQL struct {
	DSN string `yaml:"dsn"`
}

type Auth struct {
	JWTSecret string `yaml:"jwt_secret"`
}

type RocketMQ struct {
	NameServer string `yaml:"nameserver"`
	Proxy      string `yaml:"proxy"`
}

type Storage struct {
	AvatarDir    string `yaml:"avatar_dir"`
	ChatImageDir string `yaml:"chat_image_dir"`
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	// 严格解析能在启动时发现字段拼写错误。
	decoder := yaml.NewDecoder(file, yaml.Strict())
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, yamlError(path, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err != nil {
			return Config{}, yamlError(path, err)
		}
		return Config{}, fmt.Errorf("config %q must contain one YAML document", path)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("config %q: %w", path, err)
	}
	return cfg, nil
}

func yamlError(path string, err error) error {
	// 解析器的错误可能包含配置原文，只记录位置和错误类型。
	var parseErr yaml.Error
	if errors.As(err, &parseErr) {
		if tok := parseErr.GetToken(); tok != nil && tok.Position != nil {
			return fmt.Errorf("invalid YAML config %q at line %d, column %d (%T)", path,
				tok.Position.Line, tok.Position.Column, parseErr)
		}
	}
	return fmt.Errorf("invalid YAML config %q", path)
}

func (c Config) validate() error {
	for _, entry := range []struct{ key, value string }{
		{"http.addr", c.HTTP.Addr},
		{"rocketmq.nameserver", c.RocketMQ.NameServer},
		{"rocketmq.proxy", c.RocketMQ.Proxy},
	} {
		host, port, err := net.SplitHostPort(entry.value)
		number, portErr := strconv.ParseUint(port, 10, 16)
		if err != nil || portErr != nil || number == 0 || strings.TrimSpace(host) == "" {
			return fmt.Errorf("%s must be a host and numeric port", entry.key)
		}
	}
	if c.HTTP.Mode != "debug" && c.HTTP.Mode != "release" {
		return errors.New("http.mode must be debug or release")
	}
	if strings.TrimSpace(c.MySQL.DSN) == "" {
		return errors.New("mysql.dsn is required")
	}
	if len(c.Auth.JWTSecret) < 32 || strings.TrimSpace(c.Auth.JWTSecret) == "" {
		return errors.New("auth.jwt_secret must be at least 32 bytes")
	}
	if strings.TrimSpace(c.Storage.AvatarDir) == "" {
		return errors.New("storage.avatar_dir is required")
	}
	if strings.TrimSpace(c.Storage.ChatImageDir) == "" {
		return errors.New("storage.chat_image_dir is required")
	}
	return nil
}
