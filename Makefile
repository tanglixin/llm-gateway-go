COMPOSE := cd deploy && docker compose
MODEL   ?= qwen2:0.5b

.PHONY: up down ps logs pull run demo test build fmt vet verify clean

up: ## 起基础设施（PG + Redis + MinIO + Ollama）
	cd deploy && docker compose up -d

down: ## 停掉并移除容器（保留数据卷）
	cd deploy && docker compose down

ps: ## 查看容器与健康状态
	cd deploy && docker compose ps

logs: ## 跟踪日志
	cd deploy && docker compose logs -f --tail=100

pull: ## 拉取 Ollama 模型（默认 qwen2:7b）
	cd deploy && docker compose exec ollama ollama pull $(MODEL)

run: ## 本地起 Go 服务（:8080）
	go run ./cmd/server

demo: ## 调一次 Ollama，打印回答与 token 统计
	go run ./cmd/ollama-demo

test: ## 跑单元测试（用 mock Ollama，不需要真服务）
	go test ./...

build: ## 编译到 bin/
	go build -o bin/llm-gateway ./cmd/server
	go build -o bin/ollama-demo ./cmd/ollama-demo

fmt:
	gofmt -l -w .

vet:
	go vet ./...

verify: ## 检查基础设施连通性
	./deploy/verify.sh

clean:
	rm -rf bin
