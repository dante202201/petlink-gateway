// Package aiclient 宠物语音翻译 AI 服务（对齐 Translate.php translateAll）。
package aiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"pet-translate-gateway/config"
)

// Result AI 翻译原始结果。
type Result struct {
	PetSays     string          `json:"pet_says"`
	IsSupported bool            `json:"is_supported"`
	Message     string          `json:"message"`
	Raw         json.RawMessage `json:"-"`
}

// Client AI 翻译客户端。
type Client struct {
	endpoint string // 完整 URL，如 http://host:6001/translate
	http     *http.Client
	log      *zap.Logger
}

// New 创建客户端；enabled=false 返回 nil。
func New(cfg config.AIConfig, log *zap.Logger) *Client {
	if !cfg.Enabled {
		return nil
	}
	endpoint := cfg.TranslateEndpoint()
	if endpoint == "" {
		return nil
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{
		endpoint: endpoint,
		http:     &http.Client{Timeout: timeout},
		log:      log,
	}
}

// Endpoint 返回实际请求地址（排障用）。
func (c *Client) Endpoint() string {
	if c == nil {
		return ""
	}
	return c.endpoint
}

// TranslateByURL 传音频公网 URL 给 AI（与 PHP audio_url 模式一致）。
// petType / audioURL 必填；duration 可选（秒数字符串，对齐 PHP audio_duration）。
func (c *Client) TranslateByURL(ctx context.Context, petType, audioURL, duration string) (*Result, error) {
	if c == nil {
		return nil, fmt.Errorf("ai 未启用")
	}
	petType = strings.TrimSpace(petType)
	audioURL = strings.TrimSpace(audioURL)
	if audioURL == "" {
		return nil, fmt.Errorf("audio_url 为空")
	}
	if petType == "" {
		petType = "dog"
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("pet_type", petType)
	_ = w.WriteField("audio_url", audioURL)
	if d := strings.TrimSpace(duration); d != "" {
		_ = w.WriteField("audio_duration", d)
	}
	_ = w.Close()

	c.log.Info("⏳ [AI翻译] 请求",
		zap.String("url", c.endpoint),
		zap.String("pet_type", petType),
		zap.String("audio_url", audioURL),
		zap.String("audio_duration", duration),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("AI请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	c.log.Info("✅ [AI翻译] 响应",
		zap.Int("status", resp.StatusCode),
		zap.Duration("dur", time.Since(start)),
		zap.ByteString("body", body),
	)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("AI HTTP %d: %s", resp.StatusCode, truncate(string(body), 512))
	}

	var res Result
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("AI响应非JSON: %s", truncate(string(body), 512))
	}
	res.Raw = append(json.RawMessage(nil), body...)
	return &res, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
