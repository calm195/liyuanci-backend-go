CREATE TABLE public.matchmaking_trait_maintenance_audits (
    id uuid PRIMARY KEY,
    actor_id uuid NOT NULL REFERENCES public.admin_accounts(id),
    trait_id uuid NOT NULL REFERENCES public.matchmaking_traits(id),
    action text NOT NULL CHECK (action IN ('create','category','enabled','category_enabled','confirm')),
    old_category text CHECK (old_category IN ('personality','interest','lifestyle','uncategorized')),
    new_category text NOT NULL CHECK (new_category IN ('personality','interest','lifestyle','uncategorized')),
    old_enabled boolean,
    new_enabled boolean NOT NULL,
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 500),
    version bigint NOT NULL CHECK (version > 0),
    at timestamptz NOT NULL,
    UNIQUE (trait_id, version),
    CHECK ((action='create' AND old_category IS NULL AND old_enabled IS NULL AND version=1)
        OR (action<>'create' AND old_category IS NOT NULL AND old_enabled IS NOT NULL AND version>1))
);
COMMENT ON TABLE public.matchmaking_trait_maintenance_audits IS '共享特质后台维护留迹，独立保留原审核依据';
COMMENT ON COLUMN public.matchmaking_trait_maintenance_audits.id IS '审计随机标识';
COMMENT ON COLUMN public.matchmaking_trait_maintenance_audits.actor_id IS '后台操作主体';
COMMENT ON COLUMN public.matchmaking_trait_maintenance_audits.trait_id IS '共享词条标识';
COMMENT ON COLUMN public.matchmaking_trait_maintenance_audits.action IS '维护动作：新增、分类、启停、分类及启停或原值确认';
COMMENT ON COLUMN public.matchmaking_trait_maintenance_audits.old_category IS '维护前分类，新建为空';
COMMENT ON COLUMN public.matchmaking_trait_maintenance_audits.new_category IS '维护后分类';
COMMENT ON COLUMN public.matchmaking_trait_maintenance_audits.old_enabled IS '维护前启用状态，新建为空';
COMMENT ON COLUMN public.matchmaking_trait_maintenance_audits.new_enabled IS '维护后启用状态';
COMMENT ON COLUMN public.matchmaking_trait_maintenance_audits.reason IS '本次维护理由，独立于审核理由';
COMMENT ON COLUMN public.matchmaking_trait_maintenance_audits.version IS '操作完成后的词条版本';
COMMENT ON COLUMN public.matchmaking_trait_maintenance_audits.at IS '维护提交时间';
CREATE INDEX matchmaking_trait_maintenance_actor_idx ON public.matchmaking_trait_maintenance_audits(actor_id,at,id);
ALTER TABLE public.matchmaking_trait_maintenance_audits ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.matchmaking_trait_maintenance_audits FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.matchmaking_trait_maintenance_audits USING (true) WITH CHECK (true);
REVOKE ALL ON public.matchmaking_trait_maintenance_audits FROM PUBLIC;
