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

// resolveNameServers 在启动阶段解析DNS，适配 T004/REQ-020 的Go SDK 2.1.2仅接受IP的限制。
// 地址按进程生命周期固定；NameServer地址变化后应重启客户端，不能将域名冒充HTTP发现URL。
func resolveNameServers(ctx context.Context, addresses []string, resolver ipResolver) ([]string, error) {
	if len(addresses) == 0 {
		return nil, errors.New("nameserver addresses required")
	}
	var resolved []string
	seen := make(map[string]bool)
	for _, address := range addresses {
		host, port, err := net.SplitHostPort(address)
		if err != nil || host == "" {
			return nil, errors.New("invalid nameserver host/port")
		}
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, errors.New("invalid nameserver port")
		}
		ips := []net.IPAddr{{IP: net.ParseIP(host)}}
		if ips[0].IP == nil {
			ips, err = resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, errors.New("resolve nameserver failed")
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
			return nil, errors.New("nameserver resolved no IP addresses")
		}
	}
	return resolved, nil
}
