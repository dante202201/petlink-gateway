// 宠物翻译硬件 WebSocket 网关入口。
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"pet-translate-gateway/config"
	aiclient "pet-translate-gateway/internal/ai"
	"pet-translate-gateway/internal/api"
	"pet-translate-gateway/internal/baidu"
	"pet-translate-gateway/internal/gateway"
	kafkax "pet-translate-gateway/internal/kafka"
	"pet-translate-gateway/internal/locate"
	ossx "pet-translate-gateway/internal/oss"
	"pet-translate-gateway/internal/pipeline"
	redisx "pet-translate-gateway/internal/redis"
	"pet-translate-gateway/internal/store"
	"pet-translate-gateway/internal/util"
	"pet-translate-gateway/pkg/logger"
)

func main() {
	cfgPath := flag.String("config", "./config/config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	log, err := logger.Init(cfg.Log.Level, cfg.Log.Filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化日志失败: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync() //nolint:errcheck

	rdb, err := redisx.New(cfg.Redis)
	if err != nil {
		log.Fatal("初始化 Redis 失败", zap.Error(err))
	}
	defer rdb.Close()

	pub, err := kafkax.NewPublisher(cfg.Kafka, log)
	if err != nil {
		log.Fatal("初始化消息投递失败", zap.Error(err))
	}
	defer pub.Close()

	dbStore, err := store.New(cfg.Database)
	if err != nil {
		log.Fatal("初始化数据库失败", zap.Error(err))
	}
	store.SetLogger(log)
	if dbStore != nil {
		defer dbStore.Close()
		log.Info("音频入库已启用", zap.String("driver", cfg.Database.Driver), zap.String("dsn", cfg.Database.DSN))
	}

	ossClient, err := ossx.New(cfg.OSS)
	if err != nil {
		log.Fatal("初始化 OSS 失败", zap.Error(err))
	}
	aiClient := aiclient.New(cfg.AI, log)
	bdClient := baidu.New(cfg.Baidu, rdb, log)
	pipe := pipeline.New(cfg.Translate, dbStore, ossClient, aiClient, bdClient, log)
	pipe.SetRedis(rdb)
	if aiClient != nil {
		log.Info("AI翻译已启用", zap.String("endpoint", aiClient.Endpoint()))
	}
	if bdClient != nil {
		log.Info("百度TTS已启用", zap.Int("aue", cfg.Baidu.Aue), zap.Int("per", cfg.Baidu.Per))
	}

	locSvc := locate.New(cfg.Locate, rdb, log)
	liveFW := config.NewLiveFirmware(*cfgPath, cfg.Firmware, 10*time.Second, log)
	defer liveFW.Stop()
	deviceAPI := api.New(cfg, rdb, locSvc, log, liveFW, dbStore)
	appAPI := api.NewAppHandler(dbStore, rdb, log)
	adminAPI := api.NewAdminHandler(cfg, liveFW, ossClient, dbStore, rdb, log)
	mgr := gateway.NewConnManager(cfg, rdb, pub, dbStore, pipe, log)
	pipe.SetDevicePusher(mgr)
	appAPI.SetDevicePusher(mgr)
	adminAPI.SetDeviceHub(mgr)

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.Server.WSPath, mgr.HandleWS)
	mgr.RegisterAdminRoutes(mux)
	deviceAPI.Register(mux)
	appAPI.Register(mux)
	adminAPI.Register(mux)

	// WebSocket 长连接不能设 http.Server 的 ReadTimeout/WriteTimeout：
	// 二者会在 handler 期间给底层连接打 deadline，导致握手写 101 失败或连上后被误杀。
	// 慢速攻击防护只用 ReadHeaderTimeout；帧级超时由 websocket.read/write_timeout 管。
	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           util.CORSMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}

	behindProxy := cfg.Server.BehindProxy
	if !behindProxy {
		hotCert, err := util.NewHotCert(
			cfg.Server.CertFile,
			cfg.Server.KeyFile,
			cfg.Server.CertReloadInterval,
			log,
		)
		if err != nil {
			log.Fatal("加载 TLS 证书失败", zap.Error(err))
		}
		defer hotCert.Stop()
		srv.TLSConfig = &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: hotCert.GetCertificate,
		}
	}

	go func() {
		log.Info("网关启动",
			zap.String("addr", cfg.Server.Addr),
			zap.String("ws_path", cfg.Server.WSPath),
			zap.String("server_url", cfg.WSServerURL()),
			zap.Bool("behind_proxy", behindProxy),
			zap.Bool("kafka_enabled", cfg.Kafka.Enabled),
			zap.Bool("database_enabled", cfg.Database.Enabled),
			zap.Bool("emb_translate_persist", pipe.Enabled()),
			zap.Bool("oss_upload_origin", pipe.UploadOriginEnabled()),
			zap.Bool("full_translate", pipe.FullTranslateEnabled()),
		)
		var err error
		if behindProxy {
			log.Info("behind_proxy=true：明文 HTTP 监听，TLS/WSS 由 Nginx 终结")
			err = srv.ListenAndServe()
		} else {
			err = srv.ListenAndServeTLS("", "")
		}
		if err != nil && err != http.ErrServerClosed {
			log.Fatal("HTTP/WSS 服务异常退出", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Info("收到退出信号，开始优雅关闭", zap.String("signal", sig.String()))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	mgr.CloseAll()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("HTTP 关闭异常", zap.Error(err))
	}
	log.Info("网关已退出")
}
