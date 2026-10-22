package config

import (
	"errors"

	"github.com/spf13/viper"
)

// LoadCardWorkerMQ 解析显式消息配置到调用方类型，不加载探针配置或生成凭据。
// 具体通道与预算由工作进程在启动客户端前验证；错误不包含路径或凭据。
func LoadCardWorkerMQ(path string, target any) error {
	if path == "" || target == nil {
		return errors.New("缺少消息配置")
	}
	v := viper.New()
	v.SetConfigFile(path)
	if v.ReadInConfig() != nil || v.UnmarshalExact(target) != nil {
		return errors.New("消息配置无效")
	}
	return nil
}
