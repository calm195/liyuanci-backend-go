package config

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/calm195/liyuanci-backend-go/internal/platform/yamlconfig"
)

// TestDatabaseFieldsRoundTrip 用实际驱动解析结果验证特殊字符不能注入参数，IPv6和公共默认仍可用。
func TestDatabaseFieldsRoundTrip(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "db.internal", "::1"} {
		d := Defaults().Database
		d.Driver, d.Host, d.Port = "postgres", host, 15432
		d.Name, d.User = "库名 /?'\\", "应用 user'"
		d.Password = "secret ' host=evil TimeZone=invalid \\ $TOKEN %?#@"
		d.TimeZone, d.ConnectTimeout = "Asia/Shanghai", 12*time.Second
		for _, name := range []string{
			"PGHOST",
			"PGPORT",
			"PGDATABASE",
			"PGUSER",
			"PGPASSWORD",
			"PGSSLMODE",
			"PGCONNECT_TIMEOUT",
			"PGSSLROOTCERT",
			"PGSSLCERT",
			"PGSSLKEY",
		} {
			t.Setenv(name, "ignored")
		}
		if err := d.resolve(); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(d.DSN, "TimeZone=Asia/Shanghai ") {
			t.Fatal("GORM时区解析可能误读口令或引号")
		}
		parsed, err := pgconn.ParseConfig(d.DSN)
		if err != nil {
			t.Fatal("生成的连接无法解析")
		}
		if parsed.Host != host || parsed.Port != 15432 || parsed.Database != d.Name || parsed.User != d.User || parsed.Password != d.Password ||
			parsed.ConnectTimeout != d.ConnectTimeout ||
			parsed.RuntimeParams["TimeZone"] != d.TimeZone ||
			parsed.TLSConfig != nil ||
			len(parsed.Fallbacks) != 0 {
			t.Fatal("连接字段改变或被环境覆盖")
		}
		raw, err := yamlconfig.Marshal(d)
		if err != nil || strings.Contains(string(raw), "dsn:") {
			t.Fatal("内部连接串被序列化")
		}
	}
}

// TestDatabaseFieldsValidation 验证分项配置失败时不返回含凭据的驱动错误。
func TestDatabaseFieldsValidation(t *testing.T) {
	for _, change := range []func(*Database){
		func(d *Database) { d.Driver = "mysql" },
		func(d *Database) { d.Host = "" },
		func(d *Database) { d.Host = "db1,db2" },
		func(d *Database) { d.Host = " db " },
		func(d *Database) { d.Port = 0 },
		func(d *Database) { d.Port = 65536 },
		func(d *Database) { d.Name = "" },
		func(d *Database) { d.User = " " },
		func(d *Database) { d.Password = "" },
		func(d *Database) { d.Password += "\x00" },
		func(d *Database) { d.SSLMode = "invalid" },
		func(d *Database) { d.TimeZone = "" },
		func(d *Database) { d.TimeZone = "invalid" },
		func(d *Database) { d.TimeZone = "Local" },
		func(d *Database) { d.ConnectTimeout = 0 },
		func(d *Database) { d.ConnectTimeout = 1500 * time.Millisecond },
		func(d *Database) { d.ConnectTimeout = 61 * time.Second },
	} {
		d := Defaults().Database
		d.Driver, d.Host, d.Name, d.User, d.Password = "postgres", "localhost", "synthetic", "application", "private-marker"
		change(&d)
		err := d.resolve()
		if err == nil || strings.Contains(err.Error(), "private-marker") || d.DSN != "" {
			t.Fatal("非法连接配置被接受或泄漏凭据")
		}
	}
	for _, mode := range []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"} {
		d := Defaults().Database
		d.Driver, d.Host, d.Name, d.User, d.Password = "postgres", "localhost", "synthetic", "application", "synthetic"
		d.SSLMode = mode
		if err := d.resolve(); err != nil {
			t.Fatal("拒绝有效TLS模式")
		}
		p, err := pgconn.ParseConfig(d.DSN)
		if err != nil ||
			(mode == "verify-full" && (p.TLSConfig == nil || p.TLSConfig.InsecureSkipVerify || p.TLSConfig.ServerName != d.Host)) {
			t.Fatal("TLS模式装配不符")
		}
	}
}
