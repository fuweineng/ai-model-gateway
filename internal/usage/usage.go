package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Entry struct {
	Timestamp string `json:"timestamp"`
	Date      string `json:"date"`
	Role      string `json:"role"`
	Model     string `json:"model"`
	Upstream  string `json:"upstream"`
	LatencyMs int    `json:"latency_ms"`
	Status    int    `json:"status"`
	TokensIn  int    `json:"tokens_in"`
	TokensOut int    `json:"tokens_out"`
	Error     string `json:"error"`
	ClientIP  string `json:"client_ip,omitempty"`
}

type RequestBrief struct {
	Time      string `json:"time"`
	Role      string `json:"role"`
	Model     string `json:"model"`
	Upstream  string `json:"upstream"`
	LatencyMs int    `json:"latency_ms"`
	Status    int    `json:"status"`
	ClientIP  string `json:"client_ip,omitempty"`
	Error     string `json:"error,omitempty"`
}

type Tracker struct {
	mu             sync.Mutex
	logDir         string
	usageFilename  string
	stats          map[string]*ModelStats  // key: "date:model"
	recentRequests []RequestBrief
}

type ModelStats struct {
	Count          int `json:"count"`
	Errors         int `json:"errors"`
	TotalLatencyMs int `json:"total_latency_ms"`
}

func NewTracker(logDir, usageFilename string) *Tracker {
	return &Tracker{
		logDir:         logDir,
		usageFilename:  usageFilename,
		stats:          make(map[string]*ModelStats),
		recentRequests: make([]RequestBrief, 0, 50),
	}
}

func (t *Tracker) Log(entry Entry) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// 更新统计
	key := entry.Date + ":" + entry.Model
	ms, ok := t.stats[key]
	if !ok {
		ms = &ModelStats{}
		t.stats[key] = ms
	}
	ms.Count++
	if entry.Status >= 400 {
		ms.Errors++
	}
	ms.TotalLatencyMs += entry.LatencyMs

	// 最近请求
	rb := RequestBrief{
		Time:      entry.Timestamp,
		Role:      entry.Role,
		Model:     entry.Model,
		Upstream:  entry.Upstream,
		LatencyMs: entry.LatencyMs,
		Status:    entry.Status,
		ClientIP:  entry.ClientIP,
		Error:     entry.Error,
	}
	if len(t.recentRequests) >= 50 {
		t.recentRequests = t.recentRequests[1:]
	}
	t.recentRequests = append(t.recentRequests, rb)

	// 写 JSONL
	_ = os.MkdirAll(t.logDir, 0755)
	logPath := filepath.Join(t.logDir, t.usageFilename)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(entry)
	f.Write(line)
	f.Write([]byte{'\n'})
}

func (t *Tracker) GetStats() map[string]*ModelStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make(map[string]*ModelStats, len(t.stats))
	for k, v := range t.stats {
		cp := *v
		result[k] = &cp
	}
	return result
}

func (t *Tracker) GetRecentRequests() []RequestBrief {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]RequestBrief, len(t.recentRequests))
	copy(result, t.recentRequests)
	return result
}

func NewEntry(role, model, upstream, clientIP string, latencyMs, status int, tokensIn, tokensOut int, err string) Entry {
	now := time.Now()
	return Entry{
		Timestamp: now.Format(time.RFC3339),
		Date:      now.Format("2006-01-02"),
		Role:      role,
		Model:     model,
		Upstream:  upstream,
		LatencyMs: latencyMs,
		Status:    status,
		TokensIn:  tokensIn,
		TokensOut: tokensOut,
		Error:     err,
		ClientIP:  clientIP,
	}
}
