// Package util 提供时间、CRC、编解码与 TLS 证书热加载等轻量工具。
package util

import (
	"crypto/tls"
	"fmt"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"
)

// HotCert 支持运行时热加载的证书持有者。
type HotCert struct {
	mu          sync.RWMutex
	cert        *tls.Certificate
	certFile    string
	keyFile     string
	modTimeSum  int64
	log         *zap.Logger
	reloadEvery time.Duration
	stopCh      chan struct{}
}

// NewHotCert 加载初始证书并启动后台热加载协程。
func NewHotCert(certFile, keyFile string, reloadEvery time.Duration, log *zap.Logger) (*HotCert, error) {
	h := &HotCert{
		certFile:    certFile,
		keyFile:     keyFile,
		log:         log,
		reloadEvery: reloadEvery,
		stopCh:      make(chan struct{}),
	}
	if err := h.reload(); err != nil {
		return nil, err
	}
	if reloadEvery > 0 {
		go h.loop()
	}
	return h, nil
}

// GetCertificate 供 tls.Config.GetCertificate 回调使用。
func (h *HotCert) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.cert == nil {
		return nil, fmt.Errorf("证书未加载")
	}
	return h.cert, nil
}

// Stop 停止热加载协程。
func (h *HotCert) Stop() {
	select {
	case <-h.stopCh:
	default:
		close(h.stopCh)
	}
}

func (h *HotCert) loop() {
	ticker := time.NewTicker(h.reloadEvery)
	defer ticker.Stop()
	for {
		select {
		case <-h.stopCh:
			return
		case <-ticker.C:
			if err := h.reloadIfChanged(); err != nil {
				h.log.Warn("证书热加载失败", zap.Error(err))
			}
		}
	}
}

func (h *HotCert) reloadIfChanged() error {
	infoC, err1 := os.Stat(h.certFile)
	infoK, err2 := os.Stat(h.keyFile)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("stat 证书失败: %v %v", err1, err2)
	}
	sum := infoC.ModTime().UnixNano() + infoK.ModTime().UnixNano()
	h.mu.RLock()
	same := sum == h.modTimeSum
	h.mu.RUnlock()
	if same {
		return nil
	}
	if err := h.reload(); err != nil {
		return err
	}
	h.log.Info("TLS 证书已热加载",
		zap.String("cert", h.certFile),
		zap.String("key", h.keyFile),
	)
	return nil
}

func (h *HotCert) reload() error {
	cert, err := tls.LoadX509KeyPair(h.certFile, h.keyFile)
	if err != nil {
		return fmt.Errorf("加载证书失败: %w", err)
	}
	infoC, _ := os.Stat(h.certFile)
	infoK, _ := os.Stat(h.keyFile)
	var sum int64
	if infoC != nil {
		sum += infoC.ModTime().UnixNano()
	}
	if infoK != nil {
		sum += infoK.ModTime().UnixNano()
	}

	h.mu.Lock()
	h.cert = &cert
	h.modTimeSum = sum
	h.mu.Unlock()
	return nil
}
