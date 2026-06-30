package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fuweineng/ai-model-gateway/internal/auth"
	"github.com/fuweineng/ai-model-gateway/internal/config"
	"github.com/fuweineng/ai-model-gateway/internal/dashboard"
	"github.com/fuweineng/ai-model-gateway/internal/router"
	"github.com/fuweineng/ai-model-gateway/internal/upstream"
	"github.com/fuweineng/ai-model-gateway/internal/usage"
)

type Handler struct {
	cfg     *config.Config
	um      *upstream.UpstreamManager
	auth    *auth.Auth
	usage   *usage.Tracker
}

func New(cfg *config.Config, um *upstream.UpstreamManager, a *auth.Auth, ut *usage.Tracker) *Handler {
	return &Handler{
		cfg:   cfg,
		um:    um,
		auth:  a,
		usage: ut,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS preflight
	if r.Method == "OPTIONS" {
		h.writeCORS(w, r)
		w.WriteHeader(204)
		return
	}

	// Auth
	if !h.auth.CheckAuth(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
			"error": map[string]string{"type": "auth_error", "message": "Missing or invalid token"},
		})
		return
	}

	// Rate limit
	clientIP := h.auth.GetClientIP(r)
	if !h.auth.CheckRateLimit(clientIP) {
		writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{
			"error": map[string]string{"type": "rate_limit_error", "message": "Rate limit exceeded (60 req/min per IP)"},
		})
		return
	}

	switch r.Method {
	case "GET":
		h.handleGET(w, r, clientIP)
	case "POST":
		h.handlePOST(w, r, clientIP)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{
			"error": map[string]string{"message": "method not allowed"},
		})
	}
}

func (h *Handler) handleGET(w http.ResponseWriter, r *http.Request, clientIP string) {
	path := r.URL.Path

	switch {
	case path == "/dashboard" || path == "/dashboard/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(200)
		fmt.Fprint(w, dashboard.HTML)

	case path == "/v1/models" || path == "/models":
		h.listModels(w)

	case path == "/v1/health" || path == "/health":
		h.healthStatus(w)

	case path == "/v1/health/upstream" || path == "/health/upstream":
		h.healthUpstream(w)

	case path == "/v1/usage" || path == "/usage":
		writeJSON(w, 200, h.usage.GetStats())

	case path == "/v1/requests" || path == "/requests":
		writeJSON(w, 200, h.usage.GetRecentRequests())

	default:
		writeJSON(w, 404, map[string]interface{}{
			"error": map[string]string{"message": fmt.Sprintf("unsupported path: %s", path)},
		})
	}
}

func (h *Handler) handlePOST(w http.ResponseWriter, r *http.Request, clientIP string) {
	// 请求体大小上限 8MB
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024*1024)

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]interface{}{
			"error": map[string]string{"message": "Request body exceeds limit (8MB)"},
		})
		return
	}

	var body map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		writeJSON(w, 400, map[string]interface{}{
			"error": map[string]string{"message": "invalid JSON body"},
		})
		return
	}

	// 并发上限
	if !h.auth.Acquire() {
		writeJSON(w, 503, map[string]interface{}{
			"error": map[string]string{
				"type":    "service_busy",
				"message": "Gateway concurrent request limit reached, try again later",
			},
		})
		return
	}
	defer h.auth.Release()

	path := r.URL.Path
	if strings.HasPrefix(path, "/v1/") {
		path = path[3:]
	}

	switch {
	case path == "/chat/completions":
		h.handleChat(w, r, body, clientIP)
	case path == "/responses":
		h.handleResponses(w, body, clientIP)
	default:
		writeJSON(w, 404, map[string]interface{}{
			"error": map[string]string{"message": fmt.Sprintf("unsupported path: %s", path)},
		})
	}
}

func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request, body map[string]interface{}, clientIP string) {
	role := router.ResolveRole(r, body)
	roleCfg := h.cfg.Roles[role]
	if roleCfg == nil {
		roleCfg = h.cfg.Roles["default"]
	}

	model, upstreamName, explicit := router.ResolveModel(r, body, roleCfg)

	fallbackChain := router.GetFallbackChain(roleCfg)

	// 构建尝试列表
	type attempt struct {
		model    string
		upstream string
	}
	attempts := []attempt{{model, upstreamName}}
	for _, fb := range fallbackChain {
		attempts = append(attempts, attempt{fb.Model, fb.Upstream})
	}

	// 如果是明确指定模型，不 fallback（除非上游不健康）
	if explicit {
		attempts = attempts[:1]
	}

	lastError := ""

	for i, a := range attempts {
		// 跳过不健康的上游
		if !h.um.IsHealthy(a.upstream) {
			lastError = fmt.Sprintf("upstream %s unhealthy", a.upstream)
			h.usage.Log(usage.NewEntry(role, a.model, a.upstream, clientIP, 0, 503, 0, 0, lastError))
			continue
		}

		status, data, latency, err := h.um.CallUpstream(a.upstream, a.model, body, 0)

		if err != nil {
			lastError = fmt.Sprintf("%s@%s: %v", a.model, a.upstream, err)
			h.usage.Log(usage.NewEntry(role, a.model, a.upstream, clientIP, latency, 503, 0, 0, lastError))
			continue
		}

		// 5xx / 配额类错误 → fallback
		if status >= 500 || status == 401 || status == 402 || status == 429 {
			errMsg := extractErrorMessage(data)
			lastError = fmt.Sprintf("%s@%s: %d %s", a.model, a.upstream, status, errMsg)
			h.usage.Log(usage.NewEntry(role, a.model, a.upstream, clientIP, latency, status, 0, 0, lastError))
			continue
		}

		// 成功 → 返回
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Gateway-Model", a.model)
		w.Header().Set("X-Gateway-Upstream", a.upstream)
		w.Header().Set("X-Gateway-Role", role)
		w.Header().Set("X-Gateway-Latency-Ms", fmt.Sprintf("%d", latency))
		if i > 0 {
			w.Header().Set("X-Gateway-Fallback-From", attempts[0].model)
		}
		h.writeCORS(w, r)
		w.WriteHeader(status)
		w.Write(data)

		// 记录用量
		tokensIn, tokensOut := extractTokens(data)
		h.usage.Log(usage.NewEntry(role, a.model, a.upstream, clientIP, latency, status, tokensIn, tokensOut, ""))
		return
	}

	// 所有都失败
	writeJSON(w, 503, map[string]interface{}{
		"error": map[string]interface{}{
			"message": fmt.Sprintf("All models failed. Last error: %s", lastError),
			"type":    "gateway_all_failed",
			"role":    role,
		},
	})
}

func (h *Handler) handleResponses(w http.ResponseWriter, body map[string]interface{}, clientIP string) {
	upstreamName := "opencode-go"
	status, data, latency, err := h.um.PassthroughResponses(upstreamName, body)
	if err != nil {
		writeJSON(w, 503, map[string]interface{}{
			"error": map[string]string{"message": err.Error()[:500]},
		})
		h.usage.Log(usage.NewEntry("default", "", upstreamName, clientIP, latency, 503, 0, 0, err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(status)
	w.Write(data)
	h.usage.Log(usage.NewEntry("default", extractModelName(body), upstreamName, clientIP, latency, status, 0, 0, ""))
}

func (h *Handler) listModels(w http.ResponseWriter) {
	seen := make(map[string]bool)
	var models []map[string]interface{}

	for upName, upCfg := range h.cfg.Upstreams {
		for _, m := range upCfg.AllModels() {
			if !seen[m] {
				seen[m] = true
				models = append(models, map[string]interface{}{
					"id":        m,
					"object":    "model",
					"created":   time.Now().Unix(),
					"owned_by":  upName,
				})
			}
		}
	}

	// 虚拟模型（角色名）
	for roleName := range h.cfg.Roles {
		models = append(models, map[string]interface{}{
			"id":          roleName,
			"object":      "model",
			"created":     time.Now().Unix(),
			"owned_by":    "gateway",
			"description": fmt.Sprintf("Role-based routing: %s", roleName),
		})
	}

	writeJSON(w, 200, map[string]interface{}{
		"object": "list",
		"data":   models,
	})
}

func (h *Handler) healthStatus(w http.ResponseWriter) {
	health := h.um.GetHealth()
	uptime := int(time.Since(time.Unix(config.GlobalState.StartTime, 0)).Seconds())
	writeJSON(w, 200, map[string]interface{}{
		"status":         "ok",
		"uptime_seconds": uptime,
		"upstreams":      health,
	})
}

func (h *Handler) healthUpstream(w http.ResponseWriter) {
	results := make(map[string]*upstream.HealthStatus)
	for name := range h.cfg.Upstreams {
		h.um.CheckHealth(name)
	}
	results = h.um.GetHealth()
	writeJSON(w, 200, results)
}

func writeJSON(w http.ResponseWriter, code int, data interface{}) {
	raw, _ := json.Marshal(data)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(raw)))
	w.WriteHeader(code)
	w.Write(raw)
}

func (h *Handler) writeCORS(w http.ResponseWriter, r *http.Request) {
	origins := h.cfg.Security.CORSOrigins
	reqOrigin := r.Header.Get("Origin")
	if reqOrigin != "" {
		for _, o := range origins {
			if o == reqOrigin || o == "*" {
				w.Header().Set("Access-Control-Allow-Origin", reqOrigin)
				w.Header().Set("Vary", "Origin")
				break
			}
		}
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers",
		"Authorization, Content-Type, X-Agent-Role, X-Reasoning, X-Task-Complexity, X-Content-Type")
}

func extractErrorMessage(data []byte) string {
	var resp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &resp); err == nil && resp.Error.Message != "" {
		return resp.Error.Message
	}
	return string(data[:min(len(data), 200)])
}

func extractTokens(data []byte) (int, int) {
	var resp struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &resp); err == nil {
		return resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	}
	return 0, 0
}

func extractModelName(body map[string]interface{}) string {
	if v, ok := body["model"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return "unknown"
}
