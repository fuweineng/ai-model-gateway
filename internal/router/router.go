package router

import (
	"net/http"
	"strings"

	"github.com/fuweineng/ai-model-gateway/internal/config"
)

// ResolveRole 从请求中解析角色
func ResolveRole(r *http.Request, body map[string]interface{}) string {
	// 1. x-agent-role header
	role := r.Header.Get("X-Agent-Role")
	if role != "" {
		return strings.ToLower(role)
	}
	// 2. body role 字段
	if body != nil {
		if v, ok := body["role"]; ok {
			if s, ok := v.(string); ok && s != "" {
				return strings.ToLower(s)
			}
		}
	}
	return "default"
}

// ResolveModel 决定使用的模型和上游
// 返回 (model, upstream, isExplicit)
func ResolveModel(r *http.Request, body map[string]interface{}, roleCfg *config.RoleEntry) (string, string, bool) {
	requestedModel := ""
	if body != nil {
		if v, ok := body["model"]; ok {
			if s, ok := v.(string); ok {
				requestedModel = s
			}
		}
	}

	// 如果明确指定了一个实际模型名（不是角色名），直接使用
	if requestedModel != "" && !IsRoleName(requestedModel) {
		upstream := FindUpstreamForModel(requestedModel)
		return requestedModel, upstream, true
	}

	// 检查自动升级标记
	cfg := config.GlobalConfig
	if cfg != nil {
		for _, rule := range cfg.AutoUpgrade {
			hVal := r.Header.Get(rule.TriggerHeader)
			if strings.EqualFold(hVal, rule.TriggerValue) {
				tierModels := cfg.Tiers[rule.TargetTier]
				if len(tierModels) > 0 {
					model := tierModels[0]
					return model, FindUpstreamForModel(model), false
				}
			}
		}
	}

	// 用角色默认
	if roleCfg != nil {
		return roleCfg.Model, roleCfg.Upstream, false
	}
	return "deepseek-v4-flash", "opencode-go", false
}

// IsRoleName 判断模型名是否为角色名
func IsRoleName(name string) bool {
	switch name {
	case "hermes", "coder", "tester", "decision", "compression", "default",
		"openclaw-main", "opencode-default", "vision":
		return true
	}
	return false
}

// FindUpstreamForModel 找到模型所属的 upstream
func FindUpstreamForModel(modelName string) string {
	cfg := config.GlobalConfig
	if cfg == nil {
		return "opencode-go"
	}
	for name, up := range cfg.Upstreams {
		for _, m := range up.AllModels() {
			if m == modelName {
				return name
			}
		}
	}
	return "opencode-go"
}

// IsModelAvailable 检查模型是否在任意 upstream 中
func IsModelAvailable(modelName string) bool {
	cfg := config.GlobalConfig
	if cfg == nil {
		return false
	}
	for _, up := range cfg.Upstreams {
		for _, m := range up.AllModels() {
			if m == modelName {
				return true
			}
		}
	}
	return false
}

// GetFallbackChain 返回角色的 fallback 链
func GetFallbackChain(roleCfg *config.RoleEntry) []config.FallbackEntry {
	if roleCfg == nil {
		return nil
	}
	return roleCfg.Fallback
}
