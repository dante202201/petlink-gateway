package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
	"go.uber.org/zap"
)

// LiveFirmware 固件 OTA 配置热加载：改 yaml 后无需重启，按间隔检测文件变更。
type LiveFirmware struct {
	mu          sync.RWMutex
	fw          FirmwareConfig
	path        string
	modTime     int64
	reloadEvery time.Duration
	log         *zap.Logger
	stopCh      chan struct{}
}

// NewLiveFirmware 用初始值创建并启动后台轮询（reloadEvery<=0 时默认 10s）。
func NewLiveFirmware(path string, initial FirmwareConfig, reloadEvery time.Duration, log *zap.Logger) *LiveFirmware {
	if reloadEvery <= 0 {
		reloadEvery = 10 * time.Second
	}
	l := &LiveFirmware{
		fw:          initial,
		path:        path,
		reloadEvery: reloadEvery,
		log:         log,
		stopCh:      make(chan struct{}),
	}
	if info, err := os.Stat(path); err == nil {
		l.modTime = info.ModTime().UnixNano()
	}
	go l.loop()
	return l
}

// Get 返回当前固件配置快照。
func (l *LiveFirmware) Get() FirmwareConfig {
	if l == nil {
		return FirmwareConfig{}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.fw
}

// ConfigPath 返回配置文件路径。
func (l *LiveFirmware) ConfigPath() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Update 更新内存并写回 config.yaml 的 firmware 段（管理后台用）。
func (l *LiveFirmware) Update(fw FirmwareConfig) error {
	// 后台上传只改包元数据时保留运维向 download_mode
	if strings.TrimSpace(fw.DownloadMode) == "" {
		cur := l.Get()
		fw.DownloadMode = cur.DownloadMode
	}
	if l == nil {
		return fmt.Errorf("live firmware 未启用")
	}
	if err := persistFirmwareSection(l.path, fw); err != nil {
		return err
	}
	info, err := os.Stat(l.path)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.fw = fw
	l.modTime = info.ModTime().UnixNano()
	l.mu.Unlock()
	l.log.Info("固件配置已更新并写回",
		zap.String("version", fw.Version),
		zap.String("url", fw.URL),
		zap.Bool("force", fw.Force),
		zap.Int64("size", fw.Size),
		zap.String("md5", fw.MD5),
	)
	return nil
}

// Stop 停止热加载。
func (l *LiveFirmware) Stop() {
	if l == nil {
		return
	}
	select {
	case <-l.stopCh:
	default:
		close(l.stopCh)
	}
}

func (l *LiveFirmware) loop() {
	ticker := time.NewTicker(l.reloadEvery)
	defer ticker.Stop()
	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			if err := l.reloadIfChanged(); err != nil {
				l.log.Warn("固件配置热加载失败", zap.Error(err))
			}
		}
	}
}

func (l *LiveFirmware) reloadIfChanged() error {
	info, err := os.Stat(l.path)
	if err != nil {
		return err
	}
	mod := info.ModTime().UnixNano()
	l.mu.RLock()
	same := mod == l.modTime
	l.mu.RUnlock()
	if same {
		return nil
	}

	fw, err := loadFirmwareSection(l.path)
	if err != nil {
		return err
	}

	l.mu.Lock()
	old := l.fw
	l.fw = fw
	l.modTime = mod
	l.mu.Unlock()

	if old != fw {
		l.log.Info("固件配置已热加载",
			zap.String("version", fw.Version),
			zap.String("url", fw.URL),
			zap.Bool("force", fw.Force),
		)
	}
	return nil
}

func loadFirmwareSection(path string) (FirmwareConfig, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	if err := v.ReadInConfig(); err != nil {
		return FirmwareConfig{}, fmt.Errorf("读取配置失败: %w", err)
	}
	var wrap struct {
		Firmware FirmwareConfig `mapstructure:"firmware"`
	}
	if err := v.Unmarshal(&wrap); err != nil {
		return FirmwareConfig{}, fmt.Errorf("解析 firmware 失败: %w", err)
	}
	return wrap.Firmware, nil
}

var firmwareBlockRE = regexp.MustCompile(`(?m)^firmware:\r?\n(?:[ \t]+[^\r\n]*\r?\n)*`)

func persistFirmwareSection(path string, fw FirmwareConfig) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取配置失败: %w", err)
	}
	block := formatFirmwareYAML(fw)
	var out string
	if firmwareBlockRE.Match(raw) {
		out = firmwareBlockRE.ReplaceAllString(string(raw), block)
	} else {
		trimmed := strings.TrimRight(string(raw), "\r\n") + "\n\n" + block
		out = trimmed
	}
	return os.WriteFile(path, []byte(out), 0o644)
}

func formatFirmwareYAML(fw FirmwareConfig) string {
	force := "false"
	if fw.Force {
		force = "true"
	}
	esc := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return s
	}
	var b strings.Builder
	mode := strings.TrimSpace(fw.DownloadMode)
	if mode == "" {
		mode = "direct"
	}
	b.WriteString("firmware:\n")
	b.WriteString(fmt.Sprintf("  version: \"%s\"\n", esc(fw.Version)))
	b.WriteString(fmt.Sprintf("  url: \"%s\"\n", esc(fw.URL)))
	b.WriteString(fmt.Sprintf("  force: %s\n", force))
	b.WriteString(fmt.Sprintf("  size: %d\n", fw.Size))
	b.WriteString(fmt.Sprintf("  md5: \"%s\"\n", esc(fw.MD5)))
	b.WriteString(fmt.Sprintf("  changelog: \"%s\"\n", esc(fw.Changelog)))
	b.WriteString(fmt.Sprintf("  download_mode: \"%s\"\n", esc(mode)))
	return b.String()
}
