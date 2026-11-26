// Package baidu 百度短文本语音合成（对齐 Translate.php text2audio）。
package baidu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"pet-translate-gateway/config"
	redisx "pet-translate-gateway/internal/redis"
)

// Client 百度 TTS 客户端。
type Client struct {
	cfg   config.BaiduConfig
	redis *redisx.Client
	http  *http.Client
	log   *zap.Logger
}

// New 创建客户端；enabled=false 返回 nil。
func New(cfg config.BaiduConfig, rdb *redisx.Client, log *zap.Logger) *Client {
	if !cfg.Enabled {
		return nil
	}
	return &Client{
		cfg:   cfg,
		redis: rdb,
		http:  &http.Client{Timeout: 30 * time.Second},
		log:   log,
	}
}

// Text2Audio 合成语音，返回音频二进制与扩展名（mp3/pcm/wav）。
func (c *Client) Text2Audio(ctx context.Context, text, cuid string) (audio []byte, ext string, err error) {
	return c.text2AudioWithAue(ctx, text, cuid, c.cfg.Aue)
}

// Text2AudioPCM16k 强制 pcm-16k，便于转 OPUS（设备 16kHz）。
func (c *Client) Text2AudioPCM16k(ctx context.Context, text, cuid string) (audio []byte, err error) {
	pcm, _, err := c.text2AudioWithAue(ctx, text, cuid, 4)
	return pcm, err
}

func (c *Client) text2AudioWithAue(ctx context.Context, text, cuid string, aue int) (audio []byte, ext string, err error) {
	if c == nil {
		return nil, "", fmt.Errorf("baidu 未启用")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, "", fmt.Errorf("合成文本为空")
	}

	token, err := c.fetchAccessToken(ctx)
	if err != nil {
		return nil, "", err
	}
	if cuid == "" {
		cuid = "backend-pet"
	}

	per, spd, pit, vol := c.cfg.Per, c.cfg.Spd, c.cfg.Pit, c.cfg.Vol
	if spd < 0 || spd > 15 {
		spd = 5
	}
	if pit < 0 || pit > 15 {
		pit = 5
	}
	if vol < 0 || vol > 15 {
		vol = 5
	}
	if aue != 3 && aue != 4 && aue != 5 && aue != 6 {
		aue = 4
	}

	form := url.Values{}
	form.Set("tex", url.QueryEscape(text))
	form.Set("tok", token)
	form.Set("cuid", cuid)
	form.Set("ctp", "1")
	form.Set("lan", "zh")
	form.Set("spd", strconv.Itoa(spd))
	form.Set("pit", strconv.Itoa(pit))
	form.Set("vol", strconv.Itoa(vol))
	form.Set("per", strconv.Itoa(per))
	form.Set("aue", strconv.Itoa(aue))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://tsn.baidu.com/text2audio", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(ct), "audio") {
		c.log.Warn("百度TTS失败", zap.String("body", string(body)))
		return nil, "", fmt.Errorf("百度TTS失败: %s", string(body))
	}

	extMap := map[int]string{3: "mp3", 4: "pcm", 5: "pcm", 6: "wav"}
	return body, extMap[aue], nil
}

func (c *Client) fetchAccessToken(ctx context.Context) (string, error) {
	key := c.cfg.TokenRedisKey
	if c.redis != nil {
		if tok, err := c.redis.GetString(ctx, key); err == nil && tok != "" {
			return tok, nil
		}
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", c.cfg.ClientID)
	form.Set("client_secret", c.cfg.ClientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://aip.baidubce.com/oauth/2.0/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("获取百度 token 失败: %s", string(body))
	}
	if c.redis != nil {
		_ = c.redis.SetString(ctx, key, out.AccessToken, 15*24*time.Hour)
	}
	return out.AccessToken, nil
}
