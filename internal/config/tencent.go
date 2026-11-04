package config

import (
	"net/url"
	"regexp"
)

// Tencent 保存COS与CI的私有YAML参数；本包仅验证，不依赖供应商或业务包。
type Tencent struct {
	Enabled                                                    bool
	Bucket, Region, SecretID, SecretKey, SessionToken, BizType string
	ObjectURL, ImageURL, CA                                    string
}

func validateTencent(c Tencent) error {
	if !c.Enabled {
		if c.Bucket != "" || c.Region != "" || c.SecretID != "" || c.SecretKey != "" || c.SessionToken != "" || c.BizType != "" || c.ObjectURL != "" || c.ImageURL != "" || c.CA != "" {
			return ErrInvalid
		}
		return nil
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,49}-[0-9]{5,20}$`).MatchString(c.Bucket) ||
		!regexp.MustCompile(`^[a-z]{2}-[a-z]+(?:-[a-z]+)?$`).MatchString(c.Region) ||
		!regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`).MatchString(c.BizType) {
		return ErrInvalid
	}
	for _, endpoint := range []string{c.ObjectURL, c.ImageURL} {
		if endpoint == "" {
			continue
		}
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return ErrInvalid
		}
	}
	for _, value := range []string{c.SecretID, c.SecretKey} {
		if len(value) < 1 || len(value) > 4096 || !regexp.MustCompile(`^[\x21-\x7e]+$`).MatchString(value) {
			return ErrInvalid
		}
	}
	if c.SessionToken != "" && (len(c.SessionToken) > 4096 || !regexp.MustCompile(`^[\x21-\x7e]+$`).MatchString(c.SessionToken)) {
		return ErrInvalid
	}
	return nil
}
