package platform

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/config"
)

type testResolver struct {
	addresses []net.IPAddr
	err       error
}

func (r testResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addresses, r.err
}

// TestNameServerResolution 验证 T004/REQ-020：Compose DNS转换、去重、IPv6与失效地址失败关闭。
func TestNameServerResolution(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    []string
		resolver testResolver
		want     []string
		fail     bool
	}{
		{name: "compose DNS", input: []string{"namesrv:9876"}, resolver: testResolver{addresses: []net.IPAddr{{IP: net.ParseIP("10.1.0.2")}}}, want: []string{"10.1.0.2:9876"}},
		{name: "literal dedup", input: []string{"127.0.0.1:9876", "127.0.0.1:9876"}, want: []string{"127.0.0.1:9876"}},
		{name: "IPv6", input: []string{"[::1]:9876"}, want: []string{"[::1]:9876"}},
		{name: "empty", fail: true}, {name: "missing port", input: []string{"namesrv"}, fail: true},
		{name: "invalid port", input: []string{"127.0.0.1:0"}, fail: true},
		{name: "DNS failure", input: []string{"namesrv:9876"}, resolver: testResolver{err: errors.New("private DNS details")}, fail: true},
		{name: "no IP", input: []string{"namesrv:9876"}, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveNameServers(context.Background(), tc.input, tc.resolver)
			if (err != nil) != tc.fail {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.fail && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

// TestInvalidMQConfigurationDoesNotPanic 验证 T004/REQ-018、020：构造失败清理不调用typed-nil Producer，不掩盖错误。
func TestInvalidMQConfigurationDoesNotPanic(t *testing.T) {
	c := config.Config{}
	c.RocketMQ.Enabled = true
	c.RocketMQ.NameServers = []string{"invalid-name-without-port"}
	r, err := Open(context.Background(), c)
	if err == nil || r != nil {
		t.Fatal("invalid MQ configuration must fail closed")
	}
}
