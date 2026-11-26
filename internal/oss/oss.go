// Package ossx 封装阿里云 OSS 上传。
package ossx

import (
	"bytes"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"

	"pet-translate-gateway/config"
)

// Client OSS 上传客户端。
type Client struct {
	bucket     *oss.Bucket
	publicBase string
	prefix     string
}

// New 创建 OSS 客户端；enabled=false 返回 nil。
func New(cfg config.OSSConfig) (*Client, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	cli, err := oss.New(cfg.Endpoint, cfg.AccessKeyID, cfg.AccessKeySecret)
	if err != nil {
		return nil, fmt.Errorf("初始化 OSS 失败: %w", err)
	}
	b, err := cli.Bucket(cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("获取 Bucket 失败: %w", err)
	}
	return &Client{
		bucket:     b,
		publicBase: strings.TrimRight(cfg.PublicBase, "/"),
		prefix:     strings.Trim(cfg.ObjectPrefix, "/"),
	}, nil
}

// UploadBytes 上传二进制，返回公网 URL。
func (c *Client) UploadBytes(objectKey string, data []byte, contentType string) (string, error) {
	if c == nil || c.bucket == nil {
		return "", fmt.Errorf("oss 未启用")
	}
	opts := []oss.Option{}
	if contentType != "" {
		opts = append(opts, oss.ContentType(contentType))
	}
	if err := c.bucket.PutObject(objectKey, bytes.NewReader(data), opts...); err != nil {
		return "", err
	}
	return c.publicBase + "/" + strings.TrimLeft(objectKey, "/"), nil
}

// BuildObjectKey 生成对象路径：prefix/kind/YYYYMM/sn/filename
func (c *Client) BuildObjectKey(kind, sn, filename string) string {
	ym := time.Now().Format("200601")
	parts := []string{c.prefix, kind, ym, sn, filename}
	return path.Join(parts...)
}

// BuildFirmwareKey 固件包路径：firmware/ota/{version}/{filename}
func (c *Client) BuildFirmwareKey(version, filename string) string {
	ver := strings.Trim(version, "/")
	if ver == "" {
		ver = "unknown"
	}
	name := path.Base(strings.ReplaceAll(filename, "\\", "/"))
	if name == "" || name == "." || name == "/" {
		name = "firmware.bin"
	}
	return path.Join("firmware", "ota", ver, name)
}
