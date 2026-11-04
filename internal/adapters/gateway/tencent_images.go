package gateway

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"regexp"

	"go.uber.org/zap"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// Scan 仅发送规范化图片的Base64，不上传明文对象；疑似与违规均拒绝，故障保留任务重试。
func (t *Tencent) Scan(ctx context.Context, image []byte, mime string) (approved bool, err error) {
	start := time.Now()
	stage, providerCode := "input", ""
	httpStatus := 0
	defer func() {
		t.complete("ci_scan", start, &err,
			zap.String("stage", stage),
			zap.Int("http_status", httpStatus),
			zap.String("provider_code", providerCode),
			zap.Bool("context_deadline_exceeded", errors.Is(ctx.Err(), context.DeadlineExceeded)),
			zap.Bool("context_cancelled", errors.Is(ctx.Err(), context.Canceled)),
		)
	}()
	if len(image) == 0 || len(image) > 8<<20 || mime != "image/png" && mime != "image/jpeg" {
		return false, command.ErrInvalid
	}
	dataID := uuid.NewString()
	input := struct {
		XMLName xml.Name `xml:"Request"`
		Input   struct {
			DataID           string `xml:"DataId"`
			Content          string
			LargeImageDetect int
		}
		Conf struct {
			BizType string
			Async   int
		}
	}{}
	input.Input.DataID = dataID
	input.Input.Content = base64.StdEncoding.EncodeToString(image)
	if len(image) > 5<<20 {
		input.Input.LargeImageDetect = 1
	}
	input.Conf.BizType = t.bizType
	data, e := xml.Marshal(input)
	input.Input.Content = ""
	if e != nil {
		return false, command.ErrInvalid
	}
	defer clear(data)
	stage = "request"
	status, _, raw, e := t.request(ctx, http.MethodPost, t.images+"/image/auditing", data, http.Header{"Content-Type": {"application/xml"}}, 1<<20)
	defer clear(raw)
	httpStatus = status
	if e != nil {
		return false, command.ErrUnavailable
	}
	if status != http.StatusOK {
		stage = "http_status"
		providerCode = tencentImageErrorCode(raw)
		return false, command.ErrUnavailable
	}
	var response struct {
		XMLName xml.Name `xml:"Response"`
		Jobs    []struct {
			DataID      []string `xml:"DataId"`
			Code, State []string
			Result      []int
			Scenes      []struct {
				XMLName xml.Name
				Code    *int
				HitFlag *int
			} `xml:",any"`
		} `xml:"JobsDetail"`
	}
	stage = "response_decode"
	if decodeTencentXML(raw, &response) != nil || len(response.Jobs) != 1 {
		return false, command.ErrUnavailable
	}
	job := response.Jobs[0]
	stage = "job_validation"
	if len(job.Code) == 1 {
		providerCode = safeTencentCode(job.Code[0])
	}
	// Result必须显式存在，防止缺失字段被Go零值解释为通过；请求标识用于核对单张响应。
	if len(job.DataID) != 1 || job.DataID[0] != dataID || len(job.Code) != 0 || len(job.State) != 1 || job.State[0] != "Success" || len(job.Result) != 1 {
		return false, command.ErrUnavailable
	}
	stage = "scene_validation"
	for _, scene := range job.Scenes {
		// 聚合结果不能掩盖已返回场景的失败或不完整状态。
		if strings.HasSuffix(scene.XMLName.Local, "Info") && scene.XMLName.Local != "UserInfo" && scene.XMLName.Local != "ListInfo" && (scene.Code == nil || scene.HitFlag == nil) {
			return false, command.ErrUnavailable
		}
		if scene.Code != nil && *scene.Code != 0 {
			return false, command.ErrUnavailable
		}
		if scene.HitFlag != nil && *scene.HitFlag != 0 {
			if *scene.HitFlag == 1 || *scene.HitFlag == 2 {
				return false, nil
			}
			return false, command.ErrUnavailable
		}
	}
	stage = "result"
	switch job.Result[0] {
	case 0:
		return true, nil
	case 1, 2:
		return false, nil
	default:
		return false, command.ErrUnavailable
	}
}

// safeTencentCode 仅记录有界技术错误码，拒绝供应商消息、OCR正文及其它自由文本。
func safeTencentCode(value string) string {
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`).MatchString(value) {
		return ""
	}
	return value
}

func tencentImageErrorCode(raw []byte) string {
	var response struct {
		XMLName xml.Name `xml:"Error"`
		Code    string
	}
	if decodeTencentXML(raw, &response) != nil {
		return ""
	}
	return safeTencentCode(response.Code)
}
