#!/usr/bin/env bash
# 逐个检查基础设施是否真的可用（不只是容器在跑）。
set -uo pipefail

fail=0
check() {
  local name=$1 url=$2
  if curl -fsS --max-time 5 "$url" >/dev/null 2>&1; then
    printf '  \033[32m✓\033[0m %-10s %s\n' "$name" "$url"
  else
    printf '  \033[31m✗\033[0m %-10s %s\n' "$name" "$url"
    fail=1
  fi
}

echo "检查服务连通性："
check minio    "http://127.0.0.1:${MINIO_PORT:-9000}/minio/health/live"
check ollama   "http://127.0.0.1:${OLLAMA_PORT:-11434}/api/tags"

# postgres / redis 是 TCP 服务，用 nc 探端口
for pair in "postgres:${POSTGRES_PORT:-5432}" "redis:${REDIS_PORT:-6379}"; do
  name=${pair%%:*}; port=${pair##*:}
  if nc -z 127.0.0.1 "$port" 2>/dev/null; then
    printf '  \033[32m✓\033[0m %-10s 127.0.0.1:%s\n' "$name" "$port"
  else
    printf '  \033[31m✗\033[0m %-10s 127.0.0.1:%s\n' "$name" "$port"
    fail=1
  fi
done

echo
if [ "$fail" = "0" ]; then
  echo "全部就绪。"
else
  echo "有服务未就绪，先看 docker compose ps 与 logs。"
fi
exit "$fail"
