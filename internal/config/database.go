package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Database 保存唯一数据库的分项配置；DSN 仅在加载后生成，不能从 YAML 设置。
type Database struct {
	Driver, Host, Name, User, Password string
	Port                               int
	SSLMode, TimeZone                  string
	ConnectTimeout                     time.Duration
	MaxOpen, MaxIdle                   int
	MaxLifetime                        time.Duration
	DSN                                string `json:"-"`
}

// resolve 先验证字段，再按 PostgreSQL 关键字格式转义；原口令中的空格、引号等不能成为连接参数。
func (d *Database) resolve() error {
	d.DSN = ""
	if d.Driver == "" {
		return nil // foundation 可不启用数据库。
	}
	if d.Driver != "postgres" {
		return fmt.Errorf("数据库仅支持postgres")
	}
	if d.Host == "" || strings.TrimSpace(d.Host) != d.Host || strings.ContainsAny(d.Host, ",/ \t\r\n") ||
		d.Port < 1 || d.Port > 65535 || strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.User) == "" || d.Password == "" {
		return fmt.Errorf("database必须填写单个主机、有效端口、库名、账号和口令")
	}
	for _, value := range []string{d.Host, d.Name, d.User, d.Password, d.TimeZone} {
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("database字段不能包含空字符")
		}
	}
	switch d.SSLMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		return fmt.Errorf("database.sslmode无效")
	}
	if _, err := time.LoadLocation(d.TimeZone); d.TimeZone == "" || err != nil {
		return fmt.Errorf("database.timezone必须是有效时区")
	}
	if d.TimeZone == "Local" || strings.ContainsFunc(d.TimeZone, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/_+-", r))
	}) {
		return fmt.Errorf("database.timezone必须是明确的标准时区名")
	}
	if d.ConnectTimeout < time.Second || d.ConnectTimeout > time.Minute || d.ConnectTimeout%time.Second != 0 {
		return fmt.Errorf("database.connecttimeout必须是1s至60s的整秒时长")
	}
	escape := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	var out strings.Builder
	// GORM二次匹配时区时不解析引号；已限制字符的时区放在首项，避免误匹配口令中的同名文本。
	out.WriteString("TimeZone=" + d.TimeZone + " ")
	for _, item := range [][2]string{
		{"host", d.Host}, {"port", strconv.Itoa(d.Port)}, {"dbname", d.Name},
		{"user", d.User}, {"password", d.Password}, {"sslmode", d.SSLMode},
		{"connect_timeout", strconv.FormatInt(int64(d.ConnectTimeout/time.Second), 10)},
		// 不从用户目录补充口令和客户端证书；验证TLS时使用系统信任根。
		{"passfile", ""}, {"sslrootcert", ""}, {"sslcert", ""}, {"sslkey", ""},
		{"application_name", "liyuanci"}, {"target_session_attrs", "read-write"},
	} {
		out.WriteString(item[0] + "='" + escape.Replace(item[1]) + "' ")
	}
	d.DSN = strings.TrimSpace(out.String())
	return nil
}
