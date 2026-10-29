package moderation

import (
	"context"
	"errors"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// screeningProvider 故意失败，证明只有命中/重审才需要原供应商，不能把故障当成批准。
type screeningProvider struct {
	calls int
	allow bool
}

var errScreeningProvider = errors.New("测试供应商不可用")

func (p *screeningProvider) Analyze(_ context.Context, input wish.Content) (Assessment, error) {
	p.calls++
	if p.allow {
		return Assessment{Body: input.Body, Tags: input.IdentityTags, DetectorVersion: "passing-provider"}, nil
	}
	return Assessment{}, errScreeningProvider
}
func (p *screeningProvider) AnalyzeMessage(context.Context, string) (MessageAssessment, error) {
	p.calls++
	if p.allow {
		return MessageAssessment{DetectorVersion: "passing-provider"}, nil
	}
	return MessageAssessment{}, errScreeningProvider
}
func (p *screeningProvider) AnalyzeWhisper(_ context.Context, _ string, body string, identity whisper.IdentityInput) (WhisperAssessment, error) {
	p.calls++
	if p.allow {
		return WhisperAssessment{Body: body, Identity: identity, DetectorVersion: "passing-provider"}, nil
	}
	return WhisperAssessment{}, errScreeningProvider
}
func (p *screeningProvider) ClassifyWhisperCapsule(context.Context, string) (MessageAssessment, error) {
	p.calls++
	if p.allow {
		return MessageAssessment{DetectorVersion: "passing-provider"}, nil
	}
	return MessageAssessment{}, errScreeningProvider
}

// TestWordlistFirstAllContentKinds 验证两个worker所有文字种类的筛选和故障边界。
func TestWordlistFirstAllContentKinds(t *testing.T) {
	snapshot := rules{crisisEntries: []string{"我不想活了"}, schoolEntries: []string{"学校"}, ordinaryEntries: []string{"傻逼"}}
	for _, kind := range []string{"wish", "application", "message", "story", "whisper_post", "whisper_comment", "whisper_nickname", "whisper_capsule"} {
		for _, tc := range []struct {
			name, body      string
			privacy, crisis bool
		}{
			{"未命中原文通过", "一起练习英语", false, false},
			{"危机保持原流程", "我不想活了需要帮助", false, false},
			{"涉校保持原流程", "学校相关反馈", false, false},
			{"违规保持原流程", "傻逼", false, false},
			{"隐私重审不能跳过", "一起练习英语", true, false},
			{"危机重审不能跳过", "一起练习英语", false, true},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				p := &screeningProvider{}
				var err error
				if whisperKind(kind) {
					w := WhisperWorker{public: p, private: p}
					r := whisperSource{registry: entity.ContentRegistry{Kind: kind}, task: entity.ModerationTask{PrivacyRecheck: tc.privacy, CrisisRecheck: tc.crisis}}
					a, e := w.analyze(context.Background(), r, snapshot, tc.body, whisper.IdentityInput{Mode: "anonymous"})
					err = e
					if p.calls == 0 && (a.DetectorVersion != wordlistDetectorVersion || a.CrisisHit || a.OrdinaryHit || a.SchoolRelated || kind == "whisper_capsule" && a.Body != "" || kind != "whisper_capsule" && (a.Body != tc.body || a.Identity.Mode != "anonymous")) {
						t.Fatal("未命中投影错误或私密胶囊生成了公开正文")
					}
				} else {
					w := Worker{analyzer: p, cfg: WorkerConfig{MessageAnalyzer: p}}
					r := current{task: entity.ModerationTask{PrivacyRecheck: tc.privacy, CrisisRecheck: tc.crisis}}
					if kind == "message" {
						r.message = &entity.Message{}
					}
					campus := "wushan"
					input := wish.Content{Body: tc.body, IdentityTags: wish.IdentityTags{Campus: &campus}}
					a, e := w.analyze(context.Background(), r, snapshot, input)
					err = e
					if p.calls == 0 && (a.Body != tc.body || a.Tags.Campus != &campus || a.DetectorVersion != wordlistDetectorVersion || a.CrisisHit || a.OrdinaryHit || a.SchoolRelated) {
						t.Fatal("未命中未保留原文或用户显式标签")
					}
				}
				if tc.name == "未命中原文通过" {
					if err != nil || p.calls != 0 {
						t.Fatal("未命中仍调用供应商或不能自动通过")
					}
				} else if !errors.Is(err, errScreeningProvider) || p.calls != 1 {
					t.Fatal("命中或重审绕过了原供应商及其失败关闭")
				}
			})
		}
	}
}

func TestWordlistFirstCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &screeningProvider{}
	w := Worker{analyzer: p}
	if _, err := w.analyze(ctx, current{}, rules{}, wish.Content{Body: "正常文字"}); !errors.Is(err, context.Canceled) || p.calls != 0 {
		t.Fatal("取消预算仍生成通过结果")
	}
	private := WhisperWorker{public: p, private: p}
	if _, err := private.analyze(ctx, whisperSource{}, rules{}, "正常文字", whisper.IdentityInput{}); !errors.Is(err, context.Canceled) || p.calls != 0 {
		t.Fatal("取消预算仍生成树洞通过结果")
	}
}

// TestDictionaryHitsCannotBeClearedByProvider 验证供应商返回全无命中也不能清除词库分流。
func TestDictionaryHitsCannotBeClearedByProvider(t *testing.T) {
	snapshot := rules{crisisEntries: []string{"危机词"}, schoolEntries: []string{"涉校词"}, ordinaryEntries: []string{"违规词"}}
	for _, kind := range []string{"wish", "application", "message", "story", "whisper_post", "whisper_comment", "whisper_nickname", "whisper_capsule"} {
		for _, body := range []string{"危机词", "涉校词", "违规词"} {
			t.Run(kind+"/"+body, func(t *testing.T) {
				p := &screeningProvider{allow: true}
				var school, ordinary, crisis bool
				if whisperKind(kind) {
					w := WhisperWorker{public: p, private: p}
					a, e := w.analyze(context.Background(), whisperSource{registry: entity.ContentRegistry{Kind: kind}}, snapshot, body, whisper.IdentityInput{Mode: "anonymous"})
					if e != nil {
						t.Fatal(e)
					}
					school, ordinary, crisis = a.SchoolRelated, a.OrdinaryHit, a.CrisisHit
				} else {
					w := Worker{analyzer: p, cfg: WorkerConfig{MessageAnalyzer: p}}
					r := current{}
					if kind == "message" {
						r.message = &entity.Message{}
					}
					a, e := w.analyze(context.Background(), r, snapshot, wish.Content{Body: body})
					if e != nil {
						t.Fatal(e)
					}
					school, ordinary, crisis = a.SchoolRelated, a.OrdinaryHit, a.CrisisHit
				}
				if p.calls != 1 || !school && !ordinary && !crisis {
					t.Fatal("词库命中被供应商清除，可能错误自动通过")
				}
				if body == "违规词" && !ordinary {
					t.Fatal("普通违规被错分流")
				}
				if body == "涉校词" && kind != "message" && kind != "whisper_capsule" && !school {
					t.Fatal("公开涉校被错分流")
				}
				if body == "危机词" && !crisis {
					t.Fatal("危机未进入优先隔离")
				}
				if (kind == "message" || kind == "whisper_capsule") && body == "涉校词" && (!ordinary || school) {
					t.Fatal("私密涉校未继续复核或污染公开涉校标签")
				}
			})
		}
	}
}
