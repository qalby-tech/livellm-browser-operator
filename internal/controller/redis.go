package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	redisKeyBrowsers          = "livellm:browsers"
	redisKeyControllerBrowsers = "livellm:controller:browsers"
	redisKeyDesiredBrowsers    = "livellm:desired:browsers"
)

type RedisState struct {
	client *redis.Client
}

func NewRedisState(url string) (*RedisState, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return &RedisState{client: client}, nil
}

func (s *RedisState) Close() error {
	return s.client.Close()
}

// ────────────────────────────────────────────────────────────
// Browser state (published by browser pods)
// ────────────────────────────────────────────────────────────

type BrowserState struct {
	WsURL        string   `json:"ws_url"`
	ProxyPort    string   `json:"proxy_port"`
	CDPPort      int      `json:"cdp_port"`
	RegisteredAt string   `json:"registered_at"`
	Extensions   []string `json:"extensions"`
}

func (s *RedisState) GetBrowserState(ctx context.Context, browserID string) (*BrowserState, error) {
	val, err := s.client.HGet(ctx, redisKeyBrowsers, browserID).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state BrowserState
	if err := json.Unmarshal([]byte(val), &state); err != nil {
		return nil, fmt.Errorf("unmarshal browser state: %w", err)
	}
	return &state, nil
}

func (s *RedisState) GetAllBrowserStates(ctx context.Context) (map[string]*BrowserState, error) {
	raw, err := s.client.HGetAll(ctx, redisKeyBrowsers).Result()
	if err != nil {
		return nil, err
	}
	result := make(map[string]*BrowserState, len(raw))
	for id, val := range raw {
		var state BrowserState
		if err := json.Unmarshal([]byte(val), &state); err != nil {
			continue
		}
		result[id] = &state
	}
	return result, nil
}

// ────────────────────────────────────────────────────────────
// Controller browser state (published by controller pods)
// ────────────────────────────────────────────────────────────

type ControllerBrowserState struct {
	SessionCount int    `json:"session_count"`
	Connected    bool   `json:"connected"`
	WsURL        string `json:"ws_url"`
}

func (s *RedisState) GetControllerBrowserStates(ctx context.Context) (map[string]*ControllerBrowserState, error) {
	raw, err := s.client.HGetAll(ctx, redisKeyControllerBrowsers).Result()
	if err != nil {
		return nil, err
	}
	result := make(map[string]*ControllerBrowserState, len(raw))
	for id, val := range raw {
		var state ControllerBrowserState
		if err := json.Unmarshal([]byte(val), &state); err != nil {
			continue
		}
		result[id] = &state
	}
	return result, nil
}

// ────────────────────────────────────────────────────────────
// Desired state (written by operator, read by browser pods)
// ────────────────────────────────────────────────────────────

type DesiredProxy struct {
	Server   string `json:"server"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Bypass   string `json:"bypass,omitempty"`
}

type DesiredBrowserState struct {
	Extensions []string                 `json:"extensions,omitempty"`
	Cookies    []map[string]interface{} `json:"cookies,omitempty"`
	Proxy      *DesiredProxy            `json:"proxy,omitempty"`
}

func (s *RedisState) SetDesiredBrowserState(ctx context.Context, browserID string, desired *DesiredBrowserState) error {
	data, err := json.Marshal(desired)
	if err != nil {
		return fmt.Errorf("marshal desired state: %w", err)
	}
	return s.client.HSet(ctx, redisKeyDesiredBrowsers, browserID, data).Err()
}

func (s *RedisState) GetDesiredBrowserState(ctx context.Context, browserID string) (*DesiredBrowserState, error) {
	val, err := s.client.HGet(ctx, redisKeyDesiredBrowsers, browserID).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var desired DesiredBrowserState
	if err := json.Unmarshal([]byte(val), &desired); err != nil {
		return nil, fmt.Errorf("unmarshal desired state: %w", err)
	}
	return &desired, nil
}
