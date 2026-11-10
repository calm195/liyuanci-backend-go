package migration

import "github.com/calm195/liyuanci-backend-go/internal/entity"

// businessModels 是结构校验与空库回退保护使用的业务实体清单；每次返回新实例，注册顺序保持稳定。
// 恢复日志和集成探针由各自入口管理，不能混入业务库结构校验；本清单不会创建或修改表结构。
func businessModels() []any {
	return []any{
		&entity.MatchmakingActivity{},
		&entity.MatchmakingEnrollment{},
		&entity.MatchmakingProfile{},
		&entity.MatchmakingPair{},
		&entity.MatchmakingMember{},
		&entity.MatchmakingTemplate{},
		&entity.MatchmakingLibraryTask{},
		&entity.MatchmakingTask{},
		&entity.MatchmakingOpen{},
		&entity.MatchmakingAudit{},

		&entity.DisplayDictionary{},
		&entity.DisplayDictionaryItem{},
		&entity.DisplayDictionaryAction{},
		&entity.Account{},

		&entity.AccountAvatar{},

		&entity.ModuleGuide{},

		&entity.ModuleGuideAcknowledgement{},

		&entity.WhisperPreference{},

		&entity.WhisperNicknameVersion{},

		&entity.WhisperPost{},

		&entity.WhisperPostVersion{},

		&entity.WhisperAlias{},

		&entity.WhisperComment{},

		&entity.WhisperCommentVersion{},

		&entity.WhisperHug{},

		&entity.WhisperCapsule{},

		&entity.WhisperNotificationRef{},

		&entity.IdentityLookupKey{},

		&entity.IdentitySecret{},

		&entity.IdentityLookupClaim{},

		&entity.IdentityName{},

		&entity.AdminAccount{},

		&entity.AdminRoleBinding{},

		&entity.Session{},

		&entity.Asset{},

		&entity.StudentCardPayload{},

		&entity.IdentityReview{},

		&entity.Block{},
		&entity.InteractionBlockPolicy{},

		&entity.Penalty{},

		&entity.Appeal{},

		&entity.Wish{},

		&entity.WishVersion{},

		&entity.Application{},

		&entity.Match{},

		&entity.Conversation{},

		&entity.Message{},

		&entity.StoryVersion{},

		&entity.StoryView{},
		&entity.WishDetailView{},

		&entity.ShareLink{},

		&entity.ContentRegistry{},

		&entity.WordlistVersion{},

		&entity.WordlistEntry{},

		&entity.WordlistHead{},

		&entity.ModerationTask{},

		&entity.ModerationScan{},

		&entity.Report{},

		&entity.ReportType{},

		&entity.ContentRestriction{},

		&entity.PrivacyHold{},

		&entity.WorkItem{},

		&entity.OutboxEvent{},

		&entity.ConsumerReceipt{},

		&entity.Notification{},

		&entity.DeliveryAttempt{},

		&entity.CommandReceipt{},

		&entity.ResourceCategory{},
		&entity.ResourceMaterial{},
		&entity.ResourceFile{},
		&entity.ResourceFeedback{},
		&entity.ResourceAudit{},

		&entity.AccessAudit{},

		&entity.DeletionJob{},

		&entity.AdminAccountAction{},

		&entity.StudentLoginExchange{},

		&entity.SchemaMigration{},

		&entity.CareCard{},

		&entity.CrisisDelivery{},

		&entity.EvidenceCase{},

		&entity.EvidenceCrisisCase{},

		&entity.EvidenceAsset{},

		&entity.EvidenceItem{},

		&entity.EvidenceAction{},
	}
}
