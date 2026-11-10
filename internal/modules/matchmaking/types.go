// Package matchmaking 管理独立的包分配活动、报名审核、人工配对及每日任务。
package matchmaking

import "time"

// ActivityInput 是发布前可编辑的完整活动配置。
type ActivityInput struct {
	ExpectedVersion      int64     `json:"expected_version"`
	Title                string    `json:"title"`
	Description          string    `json:"description"`
	Campuses             []string  `json:"campuses"` // 历史兼容字段，不影响报名资格。
	RegistrationOpensAt  time.Time `json:"registration_opens_at"`
	RegistrationClosesAt time.Time `json:"registration_closes_at"`
	ResultsAt            time.Time `json:"results_at"`
	StartsAt             time.Time `json:"starts_at"`
	EndsAt               time.Time `json:"ends_at"`
	Days                 int       `json:"days"`
}

// Activity 是活动公开元数据，Phase按数据库时间计算。
type Activity struct {
	ActivityInput
	ID      string           `json:"id"`
	Version int64            `json:"version"`
	Status  string           `json:"status"`
	Phase   string           `json:"phase"`
	Summary *ActivitySummary `json:"summary,omitempty"`
}

// ActivitySummary 只为已结束活动返回汇总数量，不包含参与者标识。
type ActivitySummary struct {
	EnrollmentCount     int64 `json:"enrollment_count"`
	SuccessfulPairCount int64 `json:"successful_pair_count"`
}

// Profile 是报名资料；仅本人、授权运营及有效配对对象可以读取。
type Profile struct {
	Name   string `json:"name"`
	Gender string `json:"gender"`
	Grade  string `json:"grade"`
	Campus string `json:"campus"`
	MBTI   string `json:"mbti"`
	Preferences
	Consent bool `json:"consent"`
}

// Preferences 是单独维护并加密保存的活动专用资料。
type Preferences struct {
	Age          int    `json:"age"`
	AgeMin       int    `json:"age_min"`
	AgeMax       int    `json:"age_max"`
	Height       int    `json:"height"`
	HeightMin    int    `json:"height_min"`
	HeightMax    int    `json:"height_max"`
	Weight       int    `json:"weight"`
	WeightMin    int    `json:"weight_min"`
	WeightMax    int    `json:"weight_max"`
	Wechat       string `json:"wechat"`
	Introduction string `json:"introduction"`
}

// EnrollmentInput 新报名版本为零，修改必须绑定当前报名版本。
type EnrollmentInput struct {
	ExpectedVersion int64 `json:"expected_version"`
	ProfileVersion  int64 `json:"profile_version"`
	AccountVersion  int64 `json:"account_version"`
	Consent         bool  `json:"consent"`
}

// Enrollment 包含活动审核结论，不替代平台学生认证状态。
type Enrollment struct {
	ID       string   `json:"id"`
	Version  int64    `json:"version"`
	Status   string   `json:"status"`
	Reason   string   `json:"reason"`
	Profile  *Profile `json:"profile,omitempty"`
	Eligible bool     `json:"eligible"`
}

// VersionInput 绑定活动或配对的乐观版本。
type VersionInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason"`
}

// ReviewInput 人工审核始终指向当前资料版本。
type ReviewInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	Decision        string `json:"decision"`
	Reason          string `json:"reason"`
}

// Template 是任务编辑项；FixedDay零表示随机候选。
type Template struct {
	Kind     string `json:"kind"`
	Body     string `json:"body"`
	FixedDay int    `json:"fixed_day"`
	MinDay   int    `json:"-"`
}

// TemplatesInput 原子替换尚未用于发布的活动题库。
type TemplatesInput struct {
	ExpectedVersion int64      `json:"expected_version"`
	Items           []Template `json:"items"`
}

// PairInput 仅接受同活动报名标识，不接受平台用户标识。
type PairInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	First           string `json:"first"`
	Second          string `json:"second"`
}

// ScoreDetails 明确每项推荐分数，分数不代表关系保证。
type ScoreDetails struct {
	MBTI   float64 `json:"mbti"`
	Age    float64 `json:"age"`
	Campus float64 `json:"campus"`
	Height float64 `json:"height"`
	Weight float64 `json:"weight"`
}

// Pair 对学生只投影有效关系中的对方资料，后台可读取报名引用。
type Pair struct {
	ConversationID string       `json:"conversation_id,omitempty"`
	ID             string       `json:"id"`
	Version        int64        `json:"version"`
	Status         string       `json:"status"`
	Score          int          `json:"score"`
	ScoreDetails   ScoreDetails `json:"score_details"`
	Algorithm      string       `json:"algorithm"`
	Enrollments    []string     `json:"enrollments,omitempty"`
	MemberNames    []string     `json:"member_names,omitempty"`
	Peer           *Profile     `json:"peer,omitempty"`
	Accessible     bool         `json:"accessible"`
	Reason         string       `json:"reason,omitempty"`
}

// Task 未来任务绝不携带正文或类型，查看时间属于当前账号。
type Task struct {
	ID        string     `json:"id"`
	DayIndex  int        `json:"day_index"`
	UnlocksAt time.Time  `json:"unlocks_at"`
	CanOpen   bool       `json:"can_open"`
	OpenedAt  *time.Time `json:"opened_at,omitempty"`
	Kind      string     `json:"kind,omitempty"`
	Body      string     `json:"body,omitempty"`
}

// Detail 汇总当前账号活动状态，不返回其他报名者列表。
type Detail struct {
	Activity   Activity    `json:"activity"`
	Enrollment *Enrollment `json:"enrollment,omitempty"`
	Pair       *Pair       `json:"pair,omitempty"`
	Tasks      []Task      `json:"tasks"`
}

// Workspace 是后台同一活动的分页工作台。
type Workspace struct {
	TaskPlan        *TaskPlan    `json:"task_plan,omitempty"`
	TemplatesLocked bool         `json:"templates_locked"`
	Activity        Activity     `json:"activity"`
	Enrollments     []Enrollment `json:"enrollments"`
	Total           int64        `json:"total"`
	Pairs           []Pair       `json:"pairs"`
	PairTotal       int64        `json:"pair_total"`
	Templates       []Template   `json:"templates"`
}

// Candidate 包含合格候选及可解释分数。
type Candidate struct {
	Enrollment   Enrollment   `json:"enrollment"`
	Score        int          `json:"score"`
	ScoreDetails ScoreDetails `json:"score_details"`
}

// Page 是活动列表的固定上限分页。
type Page struct {
	Items []Activity `json:"items"`
	Total int64      `json:"total"`
}

// Receipt 仅返回已授权命令的技术引用，客户端随后重新读取当前状态。
type Receipt struct {
	ID string `json:"id"`
}
