ALTER TABLE evidence.crisis_cases ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version>0), ADD COLUMN source_task_id uuid, ADD COLUMN handoff_at timestamptz;
COMMENT ON COLUMN evidence.crisis_cases.version IS '危机案件独立乐观版本';
COMMENT ON COLUMN evidence.crisis_cases.source_task_id IS '检测时审核任务技术引用，不依赖普通数据存续';
COMMENT ON COLUMN evidence.crisis_cases.handoff_at IS '北京时间人工接手窗口起点，不表示首次响应承诺';
ALTER TABLE evidence.evidence_actions ADD COLUMN reason_cipher jsonb;
COMMENT ON COLUMN evidence.evidence_actions.reason_cipher IS '处置理由信封密文，绑定本动作';
CREATE TABLE evidence.crisis_deliveries (
 id uuid PRIMARY KEY,
 case_id uuid NOT NULL REFERENCES evidence.crisis_cases(id),
 kind text NOT NULL CHECK (kind IN ('care','operator')),
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','leased','sent','cancelled')),
 attempts bigint NOT NULL DEFAULT 0 CHECK (attempts>=0),
 next_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
 lease_id uuid,
 lease_until timestamptz,
 UNIQUE(case_id,kind)
);
COMMENT ON TABLE evidence.crisis_deliveries IS '危机关怀和授权人员通知持久重试队列，不保存正文身份';
COMMENT ON COLUMN evidence.crisis_deliveries.id IS '随机投递标识，也是外部通知去重键';
COMMENT ON COLUMN evidence.crisis_deliveries.case_id IS '限定危机案件';
COMMENT ON COLUMN evidence.crisis_deliveries.kind IS '关怀或授权人员通知用途';
COMMENT ON COLUMN evidence.crisis_deliveries.status IS '持久投递状态，失败不删除待办';
COMMENT ON COLUMN evidence.crisis_deliveries.attempts IS '实际取得租约次数';
COMMENT ON COLUMN evidence.crisis_deliveries.next_at IS '下次可尝试时间';
COMMENT ON COLUMN evidence.crisis_deliveries.lease_id IS '本次租约令牌，防止晚到确认';
COMMENT ON COLUMN evidence.crisis_deliveries.lease_until IS '租约到期后允许崩溃补偿';
CREATE INDEX crisis_deliveries_pending_idx ON evidence.crisis_deliveries(next_at,id) WHERE status IN ('pending','leased');
CREATE TABLE public.care_cards (
 id uuid PRIMARY KEY,
 recipient_id uuid NOT NULL REFERENCES public.accounts(id),
 resources jsonb NOT NULL CHECK(jsonb_typeof(resources)='object'),
 created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
COMMENT ON TABLE public.care_cards IS '本人关怀资料投影，注销清理，不携带危机案件正文';
COMMENT ON COLUMN public.care_cards.id IS '关怀通知目标随机标识';
COMMENT ON COLUMN public.care_cards.recipient_id IS '仅本人可读取的普通账号关联';
COMMENT ON COLUMN public.care_cards.resources IS '已显式配置的版本化援助资料，不含危机身份正文';
COMMENT ON COLUMN public.care_cards.created_at IS '关怀创建时间';

ALTER TABLE public.moderation_tasks ADD COLUMN crisis_recheck boolean NOT NULL DEFAULT false;
COMMENT ON COLUMN public.moderation_tasks.crisis_recheck IS '危机误报后新建普通人工复核标志，不创建新的交互或送达序号';
