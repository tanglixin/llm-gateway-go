// Package queue 是基于 Redis list 的简单任务队列。
package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Queue 用 RPUSH + BLPOP 实现 FIFO。
type Queue struct {
	rdb *redis.Client
	key string
}

// New 建连接并 ping 一次。
func New(addr, key string) (*Queue, error) {
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("queue: ping redis: %w", err)
	}
	return &Queue{rdb: rdb, key: key}, nil
}

// Push 入队。
func (q *Queue) Push(ctx context.Context, id string) error {
	if err := q.rdb.RPush(ctx, q.key, id).Err(); err != nil {
		return fmt.Errorf("queue: push: %w", err)
	}
	return nil
}

// Pop 阻塞取一个任务；超时返回空字符串和 nil。
func (q *Queue) Pop(ctx context.Context, timeout time.Duration) (string, error) {
	res, err := q.rdb.BLPop(ctx, timeout, q.key).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("queue: pop: %w", err)
	}
	if len(res) < 2 {
		return "", nil
	}
	return res[1], nil
}

// Depth 返回队列长度。
func (q *Queue) Depth(ctx context.Context) (int64, error) {
	return q.rdb.LLen(ctx, q.key).Result()
}

// Close 关闭连接。
func (q *Queue) Close() { _ = q.rdb.Close() }
