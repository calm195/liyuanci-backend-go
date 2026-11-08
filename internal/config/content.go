package config

import (
	"errors"
	"net/netip"
	"time"
)

// Content 配置公开内容访问、后台导出和共享候选缓存的资源预算。
type Content struct {
	ReadLimit, ExportLimit int
	Window, CacheTTL       time.Duration
	TrustedProxies         []string
}

// DefaultContent 返回已有环境可直接使用的保守默认值。
func DefaultContent() Content {
	return Content{
		ReadLimit: 120, ExportLimit: 12,
		Window: time.Minute, CacheTTL: 45 * time.Second,
	}
}

func (c Content) validate() error {
	if c.ReadLimit < 1 || c.ExportLimit < 1 ||
		c.Window < time.Second || c.Window > time.Hour ||
		c.CacheTTL < 5*time.Second || c.CacheTTL > 5*time.Minute {
		return errors.New("内容接口限流或缓存预算无效")
	}
	for _, value := range c.TrustedProxies {
		prefix, err := netip.ParsePrefix(value)
		if err == nil && prefix.Bits() > 0 {
			continue
		}
		if addr, err := netip.ParseAddr(value); err != nil || addr.Zone() != "" {
			return errors.New("内容接口可信代理地址无效")
		}
	}
	return nil
}
