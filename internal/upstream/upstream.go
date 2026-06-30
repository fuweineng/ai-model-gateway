package upstream

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fuweineng/ai-model-gateway/internal/config"
)

// HealthStatus 上游健康状态
type HealthStatus struct {
	Healthy    bool   `json:"healthy"`
	LastCheck  int64  `json:"last_check"`
	LatencyMs  int    `json:"latency_ms"`
	Status     int    `json:"status,omitempty"`
	Error      string `json:"error,omitempty"`
	FailCount  int    `json:"fail_count"`
}

// UpstreamManager 管理所有上游
type UpstreamManager struct {
	mu       sync.RWMutex
	health   map[string]*HealthStatus
	client   *http.Client
	cfg      *config.Config
}

func NewUpstreamManager(cfg *config.Config) *UpstreamManager {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		MaxIdleConns:    50,
		IdleConnTimeout: 90 * time.Second,
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   120 * time.Second,
	}

	um := &UpstreamManager{
		health: make(map[string]*HealthStatus),
		client: client,
		cfg:    cfg,
	}
	// 初始化所有上游为未知状态
	for name := range cfg.Upstreams {
		um.health[name] = &HealthStatus{Healthy: true, LastCheck: 0}
	}
	return um
}

func (um *UpstreamManager) GetHealth() map[string]*HealthStatus {
	um.mu.RLock()
	defer um.mu.RUnlock()
	result := make(map[string]*HealthStatus, len(um.health))
	for k, v := range um.health {
		// 浅拷贝
		h := *v
		result[k] = &h
	}
	return result
}

func (um *UpstreamManager) IsHealthy(name string) bool {
	um.mu.RLock()
	defer um.mu.RUnlock()
	if h, ok := um.health[name]; ok {
		return h.Healthy
	}
	return true
}

func (um *UpstreamManager) RecordResult(name string, success bool, errMsg string) {
	um.mu.Lock()
	defer um.mu.Unlock()

	h, ok := um.health[name]
	if !ok {
		h = &HealthStatus{}
		um.health[name] = h
	}

	threshold := um.cfg.Security.MaxConsecutiveFails
	if threshold <= 0 {
		threshold = 3
	}

	if success {
		h.FailCount = 0
		h.Healthy = true
		h.Error = ""
	} else {
		h.FailCount++
		h.Error = truncate(errMsg, 200)
		if h.FailCount >= threshold {
			h.Healthy = false
		}
	}
	h.LastCheck = time.Now().Unix()
}

// CallUpstream 调用上游 API，返回 (statusCode, responseBody, latencyMs, error)
func (um *UpstreamManager) CallUpstream(
	upstreamName, model string,
	body map[string]interface{},
	timeoutSec int,
) (int, []byte, int, error) {
	upCfg, ok := um.cfg.Upstreams[upstreamName]
	if !ok {
		return 0, nil, 0, fmt.Errorf("unknown upstream: %s", upstreamName)
	}

	baseURL := strings.TrimRight(upCfg.BaseURL, "/")
	apiKey := upCfg.ResolveAPIKey()

	if timeoutSec <= 0 {
		timeoutSec = upCfg.Timeout
		if timeoutSec <= 0 {
			timeoutSec = 60
		}
	}

	// 构建请求 body——替换 model
	reqBody := make(map[string]interface{})
	for k, v := range body {
		reqBody[k] = v
	}
	reqBody["model"] = model
	delete(reqBody, "role")

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return 0, nil, 0, fmt.Errorf("marshal body: %w", err)
	}

	url := baseURL + "/chat/completions"
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, 0, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "AI-Model-Gateway/2.0")
	httpReq.Header.Set("Accept", "application/json")

	start := time.Now()

	maxRetries := 1
	if upCfg.MaxRetries > 0 {
		maxRetries = upCfg.MaxRetries
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		// 创建带超时的 client 用于单次调用
		client := &http.Client{
			Transport: um.client.Transport,
			Timeout:   time.Duration(timeoutSec) * time.Second,
		}

		resp, err := client.Do(httpReq)
		latency := int(time.Since(start).Milliseconds())

		if err != nil {
			lastErr = err
			isTLS := isTLSError(err)
			if attempt < maxRetries && isTLS {
				continue // TLS 错误重试
			}
			um.RecordResult(upstreamName, false, err.Error())
			return 0, nil, latency, err
		}
		defer resp.Body.Close()

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = err
			um.RecordResult(upstreamName, false, err.Error())
			return 0, nil, latency, err
		}

		um.RecordResult(upstreamName, true, "")
		return resp.StatusCode, data, latency, nil
	}

	return 0, nil, 0, lastErr
}

// PassthroughResponses 透传 Responses API（不 fallback）
func (um *UpstreamManager) PassthroughResponses(upstreamName string, body map[string]interface{}) (int, []byte, int, error) {
	upCfg, ok := um.cfg.Upstreams[upstreamName]
	if !ok {
		return 0, nil, 0, fmt.Errorf("unknown upstream: %s", upstreamName)
	}

	baseURL := strings.TrimRight(upCfg.BaseURL, "/")
	apiKey := upCfg.ResolveAPIKey()

	timeoutSec := upCfg.Timeout
	if timeoutSec <= 0 {
		timeoutSec = 60
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return 0, nil, 0, fmt.Errorf("marshal body: %w", err)
	}

	url := baseURL + "/responses"
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, 0, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "AI-Model-Gateway/2.0")

	start := time.Now()
	client := &http.Client{
		Transport: um.client.Transport,
		Timeout:   time.Duration(timeoutSec) * time.Second,
	}

	resp, err := client.Do(httpReq)
	latency := int(time.Since(start).Milliseconds())

	if err != nil {
		um.RecordResult(upstreamName, false, err.Error())
		return 0, nil, latency, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, latency, err
	}

	um.RecordResult(upstreamName, true, "")
	return resp.StatusCode, data, latency, nil
}

// CheckHealth 检查单个上游健康
func (um *UpstreamManager) CheckHealth(name string) bool {
	upCfg, ok := um.cfg.Upstreams[name]
	if !ok {
		return false
	}

	baseURL := strings.TrimRight(upCfg.BaseURL, "/")
	apiKey := upCfg.ResolveAPIKey()

	url := baseURL + "/models"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		um.updateHealth(name, false, 0, 0, err.Error())
		return false
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("User-Agent", "AI-Model-Gateway/2.0")

	timeout := um.cfg.HealthCheck.Timeout
	if timeout <= 0 {
		timeout = 10
	}

	client := &http.Client{
		Transport: um.client.Transport,
		Timeout:   time.Duration(timeout) * time.Second,
	}

	start := time.Now()
	resp, err := client.Do(req)
	latency := int(time.Since(start).Milliseconds())

	if err != nil {
		um.updateHealth(name, false, 0, latency, err.Error())
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	healthy := resp.StatusCode < 500
	um.updateHealth(name, healthy, resp.StatusCode, latency, "")
	return healthy
}

func (um *UpstreamManager) updateHealth(name string, healthy bool, status, latency int, errStr string) {
	um.mu.Lock()
	defer um.mu.Unlock()

	h, ok := um.health[name]
	if !ok {
		h = &HealthStatus{}
		um.health[name] = h
	}

	h.LastCheck = time.Now().Unix()
	h.LatencyMs = latency
	h.Status = status

	threshold := um.cfg.Security.MaxConsecutiveFails
	if threshold <= 0 {
		threshold = 3
	}

	if healthy {
		h.FailCount = 0
		h.Healthy = true
		h.Error = ""
	} else {
		h.FailCount++
		h.Error = truncate(errStr, 200)
		if h.FailCount >= threshold {
			h.Healthy = false
		}
	}
}

// HealthCheckLoop 定期健康检查
func (um *UpstreamManager) HealthCheckLoop(stopCh chan struct{}) {
	interval := um.cfg.HealthCheck.Interval
	if interval <= 0 {
		interval = 300
	}

	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			for name := range um.cfg.Upstreams {
				um.CheckHealth(name)
			}
		case <-stopCh:
			return
		}
	}
}

func isTLSError(err error) bool {
	errStr := strings.ToLower(err.Error())
	keywords := []string{"ssl", "tls", "certificate", "handshake", "connection reset",
		"eof", "protocol error", "connection refused", "connection aborted",
		"temporarily unavailable"}
	for _, kw := range keywords {
		if strings.Contains(errStr, kw) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
