package config

import "testing"

// TestTencentConfiguration 私有配置缺项、未启用残留、非HTTPS端点均在装配前拒绝。
func TestTencentConfiguration(t *testing.T) {
	valid := Tencent{Enabled: true, Bucket: "private-1234567890", Region: "ap-shanghai", SecretID: "synthetic-id", SecretKey: "synthetic-key", BizType: "synthetic-biz"}
	for _, tc := range []struct {
		name    string
		change  func(*Tencent)
		invalid bool
	}{
		{"完整配置", func(*Tencent) {}, false},
		{"缺少桶", func(c *Tencent) { c.Bucket = "" }, true},
		{"缺少地域", func(c *Tencent) { c.Region = "" }, true},
		{"缺少密钥", func(c *Tencent) { c.SecretKey = "" }, true},
		{"缺少策略", func(c *Tencent) { c.BizType = "" }, true},
		{"关闭但残留配置", func(c *Tencent) { c.Enabled = false }, true},
		{"明文代理", func(c *Tencent) { c.ObjectURL = "http://objects.invalid" }, true},
		{"携带账号", func(c *Tencent) { c.ImageURL = "https://user:secret@ci.invalid" }, true},
		{"携带查询", func(c *Tencent) { c.ImageURL = "https://ci.invalid?secret=x" }, true},
		{"携带路径", func(c *Tencent) { c.ImageURL = "https://ci.invalid/path" }, true},
		{"多行密钥", func(c *Tencent) { c.SecretKey = "secret\nvalue" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := valid
			tc.change(&c)
			if (validateTencent(c) != nil) != tc.invalid {
				t.Fatal("校验结果错误")
			}
		})
	}
	if validateTencent(Tencent{}) != nil {
		t.Fatal("未配置可启动其他业务")
	}
}
