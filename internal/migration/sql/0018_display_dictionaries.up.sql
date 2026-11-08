CREATE TABLE public.display_dictionaries (
    id bigint PRIMARY KEY CHECK (id = 1),
    version bigint NOT NULL CHECK (version > 0)
);
CREATE TABLE public.display_dictionary_items (
    type text NOT NULL,
    code text NOT NULL,
    label text NOT NULL CHECK (length(label) BETWEEN 1 AND 80),
    sort_order integer NOT NULL CHECK (sort_order BETWEEN 0 AND 100000),
    enabled boolean NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    PRIMARY KEY (type, code)
);
CREATE TABLE public.display_dictionary_actions (
    version bigint PRIMARY KEY,
    actor_id uuid NOT NULL REFERENCES public.admin_accounts(id),
    before jsonb NOT NULL,
    after jsonb NOT NULL,
    at timestamptz NOT NULL
);
COMMENT ON TABLE public.display_dictionaries IS '展示字典全局发布版本';
COMMENT ON COLUMN public.display_dictionaries.id IS '唯一字典版本行';
COMMENT ON COLUMN public.display_dictionaries.version IS '全局乐观发布版本';
ALTER TABLE public.display_dictionaries ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.display_dictionaries FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.display_dictionaries USING (true) WITH CHECK (true);
REVOKE ALL ON public.display_dictionaries FROM PUBLIC;
COMMENT ON TABLE public.display_dictionary_items IS '固定业务代码的可维护展示信息';
COMMENT ON COLUMN public.display_dictionary_items.type IS '字典类型';
COMMENT ON COLUMN public.display_dictionary_items.code IS '稳定业务代码';
COMMENT ON COLUMN public.display_dictionary_items.label IS '中文展示名称';
COMMENT ON COLUMN public.display_dictionary_items.sort_order IS '展示排序';
COMMENT ON COLUMN public.display_dictionary_items.enabled IS '选项是否启用，不改变业务权限';
COMMENT ON COLUMN public.display_dictionary_items.version IS '最近修改的全局版本';
ALTER TABLE public.display_dictionary_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.display_dictionary_items FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.display_dictionary_items USING (true) WITH CHECK (true);
REVOKE ALL ON public.display_dictionary_items FROM PUBLIC;
COMMENT ON TABLE public.display_dictionary_actions IS '展示字典修改审计';
COMMENT ON COLUMN public.display_dictionary_actions.version IS '对应全局发布版本';
COMMENT ON COLUMN public.display_dictionary_actions.actor_id IS '执行修改的管理员';
COMMENT ON COLUMN public.display_dictionary_actions.before IS '修改前字典项';
COMMENT ON COLUMN public.display_dictionary_actions.after IS '修改后字典项';
COMMENT ON COLUMN public.display_dictionary_actions.at IS '事务修改时间';
ALTER TABLE public.display_dictionary_actions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.display_dictionary_actions FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.display_dictionary_actions USING (true) WITH CHECK (true);
REVOKE ALL ON public.display_dictionary_actions FROM PUBLIC;
INSERT INTO public.display_dictionaries VALUES (1,1);
INSERT INTO public.display_dictionary_items(type,code,label,sort_order,enabled,version) VALUES
('resource_feedback','valid','有用',0,true,1),
('resource_feedback','link_invalid','链接失效',1,true,1),
('resource_feedback','outdated','年代久远',2,true,1),
('resource_feedback','incomplete','不全',3,true,1),
('verification_status','unverified','未认证',0,true,1),
('verification_status','pending','审核中',1,true,1),
('verification_status','verified','已认证',2,true,1),
('verification_status','rejected','未通过',3,true,1),
('restriction_status','normal','正常',0,true,1),
('restriction_status','muted','禁言',1,true,1),
('restriction_status','banned','封禁',2,true,1),
('admin_role','review_operator','审核运营',0,true,1),
('admin_role','crisis_operator','危机处置人员',1,true,1),
('admin_role','super_admin','超级管理员',2,true,1),
('builtin_wordlist_label','ad_compliance','广告法违规',0,true,1),
('builtin_wordlist_label','aigc','AI生成特征',1,true,1),
('builtin_wordlist_label','contraband_act','违禁行为',2,true,1),
('builtin_wordlist_label','contraband_drug','毒品',3,true,1),
('builtin_wordlist_label','contraband_entity','违禁物品',4,true,1),
('builtin_wordlist_label','contraband_forgery','伪造证件',5,true,1),
('builtin_wordlist_label','contraband_fraud','诈骗话术',6,true,1),
('builtin_wordlist_label','contraband_gambling','赌博',7,true,1),
('builtin_wordlist_label','contraband_privacy','买卖个人信息',8,true,1),
('builtin_wordlist_label','inappropriate_discrimination','偏见歧视',9,true,1),
('builtin_wordlist_label','inappropriate_ethics','不良价值观',10,true,1),
('builtin_wordlist_label','inappropriate_nonsense','无意义灌水',11,true,1),
('builtin_wordlist_label','inappropriate_oral','低俗口头语',12,true,1),
('builtin_wordlist_label','inappropriate_profanity','攻击辱骂',13,true,1),
('builtin_wordlist_label','inappropriate_superstition','封建迷信',14,true,1),
('builtin_wordlist_label','political_entity','涉政组织',15,true,1),
('builtin_wordlist_label','political_figure','涉政人物',16,true,1),
('builtin_wordlist_label','political_sensitive','敏感政治内容',17,true,1),
('builtin_wordlist_label','pornographic_adult','色情内容',18,true,1),
('builtin_wordlist_label','pt_by_recruitment','网赚兼职广告',19,true,1),
('builtin_wordlist_label','pt_to_contact','引流联系方式',20,true,1),
('builtin_wordlist_label','pt_to_sites','站外引流',21,true,1),
('builtin_wordlist_label','religion_general','宗教内容',22,true,1),
('builtin_wordlist_label','sexual_suggestive','低俗性暗示',23,true,1),
('builtin_wordlist_label','sexual_terms','性健康内容',24,true,1),
('builtin_wordlist_label','violent_extremist','极端组织',25,true,1),
('builtin_wordlist_label','violent_incidents','暴力伤害',26,true,1),
('builtin_wordlist_label','violent_weapons','武器弹药',27,true,1);
INSERT INTO public.display_dictionary_items(type,code,label,sort_order,enabled,version) VALUES
('resource_status','draft','草稿',0,true,1),
('resource_status','published','已上架',1,true,1),
('resource_status','offline','已下架',2,true,1),
('whisper_review_status','pending','待审核',0,true,1),
('whisper_review_status','approved','内容已通过',1,true,1),
('whisper_review_status','rejected','内容已驳回',2,true,1),
('whisper_review_status','cancelled','案件已取消',3,true,1),
('whisper_report_status','pending','待处理',0,true,1),
('whisper_report_status','confirmed','举报已确认',1,true,1),
('whisper_report_status','dismissed','举报已驳回',2,true,1),
('whisper_crisis_status','open','待接手',0,true,1),
('whisper_crisis_status','in_progress','跟进中',1,true,1),
('whisper_crisis_status','resolved','已结案',2,true,1),
('whisper_crisis_status','false_positive','已标记误报',3,true,1),
('whisper_delivery_status','pending','待投递',0,true,1),
('whisper_delivery_status','delivered','已投递',1,true,1),
('whisper_delivery_status','cancelled','已取消',2,true,1);
INSERT INTO public.display_dictionary_items(type,code,label,sort_order,enabled,version) VALUES
('wish_state','draft','草稿',0,true,1),
('wish_state','pending_review','审核中',1,true,1),
('wish_state','ready','审核通过，待上架',2,true,1),
('wish_state','published','已上架',3,true,1),
('wish_state','matching','匹配中',4,true,1),
('wish_state','realized','已实现',5,true,1),
('wish_state','fulfilled','已还愿',6,true,1),
('wish_state','withdrawn','已撤回',7,true,1),
('wish_state','expired','已过期',8,true,1),
('wish_state','rejected','已驳回',9,true,1);
