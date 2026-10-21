package platform

import (
	"context"
	"errors"
	"net"
	"strconv"
)

type ipResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// resolveNameServers 在启动阶段解析DNS，适配 Go SDK 2.1.2仅接受IP的限制。
// 地址按进程生命周期固定；NameServer地址变化后应重启客户端，不能将域名冒充HTTP发现URL。
func resolveNameServers(ctx context.Context, addresses []string, resolver ipResolver) ([]string, error) {
	if len(addresses) == 0 {
		return nil, errors.New("必须配置NameServer地址")
	}
	var resolved []string
	seen := make(map[string]bool)
	for _, address := range addresses {
		host, port, err := net.SplitHostPort(address)
		if err != nil || host == "" {
			return nil, errors.New("NameServer主机或端口无效")
		}
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, errors.New("NameServer端口无效")
		}
		ips := []net.IPAddr{{IP: net.ParseIP(host)}}
		if ips[0].IP == nil {
			ips, err = resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, errors.New("解析NameServer地址失败")
			}
		}
		found := false
		for _, ip := range ips {
			if ip.IP == nil {
				continue
			}
			endpoint := net.JoinHostPort(ip.IP.String(), port)
			if !seen[endpoint] {
				resolved = append(resolved, endpoint)
				seen[endpoint] = true
			}
			found = true
		}
		if !found {
			return nil, errors.New("NameServer未解析到IP地址")
		}
	}
	return resolved, nil
}

// ResolveNameServers 为显式消息职责复用启动期解析；地址变化后由装配方重启客户端。
func ResolveNameServers(ctx context.Context, addresses []string) ([]string, error) {
	return resolveNameServers(ctx, addresses, net.DefaultResolver)
}
