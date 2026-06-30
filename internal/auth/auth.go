package auth

import (
	"crypto/hmac"
	"net"
	"net/http"
	"sync"
	"time"
)

type Auth struct {
	Token            string
	PublicPaths      []string
	AllowLocalhost   bool
	TrustCloudflare  bool
	MaxPerMinute     int
	MaxConcurrent    int

	mu        sync.Mutex
	rateLimits map[string][]time.Time
	sem       chan struct{}
}

func NewAuth(cfg Config) *Auth {
	a := &Auth{
		Token:           cfg.Token,
		PublicPaths:     cfg.PublicPaths,
		AllowLocalhost:  cfg.AllowLocalhost,
		TrustCloudflare: cfg.TrustCloudflare,
		MaxPerMinute:    cfg.MaxPerMinute,
		MaxConcurrent:   cfg.MaxConcurrent,
		rateLimits:      make(map[string][]time.Time),
	}
	if a.MaxPerMinute <= 0 {
		a.MaxPerMinute = 60
	}
	if a.MaxConcurrent <= 0 {
		a.MaxConcurrent = 16
	}
	a.sem = make(chan struct{}, a.MaxConcurrent)
	return a
}

type Config struct {
	Token           string
	PublicPaths     []string
	AllowLocalhost  bool
	TrustCloudflare bool
	MaxPerMinute    int
	MaxConcurrent   int
}

func (a *Auth) GetClientIP(r *http.Request) string {
	realIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	// 只有非 localhost 才信任代理头
	if a.TrustCloudflare && !isLocalhost(realIP) {
		if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
			return cf
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if parts := splitCSV(xff); len(parts) > 0 {
				return parts[0]
			}
		}
	}
	return realIP
}

func isLocalhost(ip string) bool {
	return ip == "127.0.0.1" || ip == "::1" || ip == "localhost"
}

func splitCSV(s string) []string {
	var result []string
	current := ""
	for _, c := range s {
		if c == ',' {
			result = append(result, current)
			current = ""
		} else if c != ' ' {
			current += string(c)
		}
	}
	if current != "" {
		result = append(result, current)
	}
	return result
}

// CheckAuth 检查认证，返回是否通过
func (a *Auth) CheckAuth(r *http.Request) bool {
	// 未配置 token = 免认证
	if a.Token == "" {
		return true
	}
	// 公开路径
	path := r.URL.Path
	for _, p := range a.PublicPaths {
		if path == p || path == p+"/" {
			return true
		}
	}
	// localhost 免认证
	if a.AllowLocalhost {
		clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
		if isLocalhost(clientIP) {
			return true
		}
	}
	// Bearer token
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && auth[:7] == "Bearer " {
		token := auth[7:]
		if token != "" && hmac.Equal([]byte(token), []byte(a.Token)) {
			return true
		}
	}
	return false
}

// CheckRateLimit 检查 IP 限速，返回是否允许请求
func (a *Auth) CheckRateLimit(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-time.Minute)
	times := a.rateLimits[ip]
	// 清理旧时间戳
	var kept []time.Time
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= a.MaxPerMinute {
		a.rateLimits[ip] = kept
		return false
	}
	kept = append(kept, now)
	a.rateLimits[ip] = kept
	return true
}

// Acquire 获取并发槽位
func (a *Auth) Acquire() bool {
	select {
	case a.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release 释放并发槽位
func (a *Auth) Release() {
	<-a.sem
}
