// Package config 从环境变量加载服务配置。
package config

import (
	"encoding/json"
	"os"
)

// ProviderConfig 是一个上游 provider 的配置。
type ProviderConfig struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // ollama | openai
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
	Token   string `json:"token"`
	Weight  int    `json:"weight"`
}

// Config 是服务运行参数。默认值对齐 deploy/.env.example。
type Config struct {
	Addr           string
	OllamaBaseURL  string
	OllamaModel    string
	OllamaEmbed    string
	PostgresDSN    string
	Tenant         string
	Providers      []ProviderConfig
	RedisAddr      string
	MinioEndpoint  string
	MinioAccessKey string
	MinioSecretKey string
	MinioBucket    string
}

// Load 读取环境变量，缺省值面向本地 compose 环境。
func Load() Config {
	return Config{
		Addr:           env("SERVER_ADDR", ":8080"),
		OllamaBaseURL:  env("OLLAMA_BASE_URL", "http://127.0.0.1:11434"),
		OllamaModel:    env("OLLAMA_MODEL", "qwen2:0.5b"),
		OllamaEmbed:    env("OLLAMA_EMBED_MODEL", "nomic-embed-text"),
		PostgresDSN:    env("POSTGRES_DSN", "postgres://llm:llm@127.0.0.1:5432/llm?sslmode=disable"),
		Tenant:         env("NOVA_TENANT", "default"),
		Providers:      loadProviders(),
		RedisAddr:      env("REDIS_ADDR", "127.0.0.1:6379"),
		MinioEndpoint:  env("MINIO_ENDPOINT", "127.0.0.1:9000"),
		MinioAccessKey: env("MINIO_ROOT_USER", "minioadmin"),
		MinioSecretKey: env("MINIO_ROOT_PASSWORD", "minioadmin"),
		MinioBucket:    env("MINIO_BUCKET", "llm-gateway"),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadProviders 解析 GATEWAY_PROVIDERS（JSON 数组）。
// 没配时退化成单个 Ollama provider。
func loadProviders() []ProviderConfig {
	raw := os.Getenv("GATEWAY_PROVIDERS")
	if raw != "" {
		var list []ProviderConfig
		if err := json.Unmarshal([]byte(raw), &list); err == nil && len(list) > 0 {
			for i := range list {
				if list[i].Weight <= 0 {
					list[i].Weight = 100
				}
				if list[i].Kind == "" {
					list[i].Kind = "ollama"
				}
				if list[i].Name == "" {
					list[i].Name = list[i].Model
				}
			}
			return list
		}
	}
	return []ProviderConfig{{
		Name:    env("OLLAMA_MODEL", "qwen2:0.5b"),
		Kind:    "ollama",
		BaseURL: env("OLLAMA_BASE_URL", "http://127.0.0.1:11434"),
		Model:   env("OLLAMA_MODEL", "qwen2:0.5b"),
		Weight:  100,
	}}
}
