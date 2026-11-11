// Package config 从环境变量加载服务配置。
package config

import "os"

// Config 是服务运行参数。默认值对齐 deploy/.env.example。
type Config struct {
	Addr          string
	OllamaBaseURL string
	OllamaModel   string
	PostgresDSN   string
	Tenant        string
	RedisAddr     string
	MinioEndpoint string
}

// Load 读取环境变量，缺省值面向本地 compose 环境。
func Load() Config {
	return Config{
		Addr:          env("SERVER_ADDR", ":8080"),
		OllamaBaseURL: env("OLLAMA_BASE_URL", "http://127.0.0.1:11434"),
		OllamaModel:   env("OLLAMA_MODEL", "qwen2:0.5b"),
		PostgresDSN:   env("POSTGRES_DSN", "postgres://llm:llm@127.0.0.1:5432/llm?sslmode=disable"),
		Tenant:        env("NOVA_TENANT", "default"),
		RedisAddr:     env("REDIS_ADDR", "127.0.0.1:6379"),
		MinioEndpoint: env("MINIO_ENDPOINT", "127.0.0.1:9000"),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
