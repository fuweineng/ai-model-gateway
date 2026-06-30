package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fuweineng/ai-model-gateway/internal/auth"
	"github.com/fuweineng/ai-model-gateway/internal/config"
	"github.com/fuweineng/ai-model-gateway/internal/handler"
	"github.com/fuweineng/ai-model-gateway/internal/upstream"
	"github.com/fuweineng/ai-model-gateway/internal/usage"
)

func main() {
	// 解析参数
	configPath := "ai-model-gateway.yaml"
	if len(os.Args) > 1 {
		for i, arg := range os.Args {
			if arg == "--config" && i+1 < len(os.Args) {
				configPath = os.Args[i+1]
			}
		}
	}

	fmt.Println("🚀 AI Model Gateway starting...")

	// 加载配置
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("❌ Load config: %v", err)
	}

	// 初始化全局状态
	config.GlobalState = &config.AppState{
		Config:    cfg,
		StartTime: time.Now().Unix(),
	}

	// 加载网关 token
	token := config.LoadGatewayToken(cfg)

	// 初始化认证
	a := auth.NewAuth(auth.Config{
		Token:           token,
		PublicPaths:     cfg.Security.PublicPaths,
		AllowLocalhost:  cfg.Security.AllowLocalhostNoAuth,
		TrustCloudflare: cfg.Security.TrustCloudflare,
		MaxPerMinute:    cfg.Security.RateLimit.MaxPerMinute,
		MaxConcurrent:   cfg.Security.RateLimit.MaxConcurrent,
	})

	// 初始化上游管理器
	um := upstream.NewUpstreamManager(cfg)

	// 初始化用量追踪
	ut := usage.NewTracker(cfg.Logging.Dir, cfg.Logging.UsageLog)

	// 初始化 HTTP 处理器
	h := handler.New(cfg, um, a, ut)

	// 启动健康检查
	stopCh := make(chan struct{})
	go um.HealthCheckLoop(stopCh)

	// 启动时做一次健康检查
	go func() {
		time.Sleep(500 * time.Millisecond)
		for name := range cfg.Upstreams {
			um.CheckHealth(name)
		}
		fmt.Printf("✅ 上游健康检查完成 (%d 个)\n", len(cfg.Upstreams))
	}()

	// 创建 HTTP 服务器
	addr := cfg.Listen.Addr()
	server := &http.Server{
		Addr:         addr,
		Handler:      h,
		ReadTimeout:  120 * time.Second,
		WriteTimeout: 240 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// 优雅关闭
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n🛑 收到关闭信号，正在停止...")
		close(stopCh)
		server.Close()
	}()

	fmt.Printf("📡 监听 %s\n", addr)
	fmt.Printf("🔗 http://%s/v1/health\n", addr)
	fmt.Printf("📊 http://%s/dashboard\n", addr)

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("❌ Server error: %v", err)
	}

	fmt.Println("👋 服务已停止")
}
