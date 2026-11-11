CREATE TABLE public.matchmaking_traits (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 20),
    status text NOT NULL CHECK (status IN ('pending','approved','rejected')),
    version bigint NOT NULL CHECK (version > 0),
    evidence jsonb NOT NULL,
    reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    reviewed_at timestamptz,
    reviewed_by uuid REFERENCES public.admin_accounts(id) ON DELETE SET NULL
);

COMMENT ON TABLE public.matchmaking_traits IS '理想型特质词条及审核案件';

COMMENT ON COLUMN public.matchmaking_traits.id IS '词条标识';

COMMENT ON COLUMN public.matchmaking_traits.name IS '规范化的特质词条';

COMMENT ON COLUMN public.matchmaking_traits.status IS '词条审核状态';

COMMENT ON COLUMN public.matchmaking_traits.version IS '词条乐观版本';

COMMENT ON COLUMN public.matchmaking_traits.evidence IS '创建时词库扫描依据';

COMMENT ON COLUMN public.matchmaking_traits.reason IS '人工审核说明';

COMMENT ON COLUMN public.matchmaking_traits.created_at IS '词条创建时间';

COMMENT ON COLUMN public.matchmaking_traits.reviewed_at IS '人工审核时间';

COMMENT ON COLUMN public.matchmaking_traits.reviewed_by IS '审核管理员';

ALTER TABLE public.matchmaking_traits ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.matchmaking_traits FORCE ROW LEVEL SECURITY;

CREATE POLICY application_account ON public.matchmaking_traits USING (true) WITH CHECK (true);

REVOKE ALL ON public.matchmaking_traits FROM PUBLIC;

CREATE TABLE public.matchmaking_trait_selections (
    account_id uuid NOT NULL REFERENCES public.accounts(id) ON DELETE CASCADE,
    trait_id uuid NOT NULL REFERENCES public.matchmaking_traits(id),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (account_id, trait_id)
);

COMMENT ON TABLE public.matchmaking_trait_selections IS '账号当前选用的理想型特质';

COMMENT ON COLUMN public.matchmaking_trait_selections.account_id IS '选用者账号';

COMMENT ON COLUMN public.matchmaking_trait_selections.trait_id IS '所选特质';

COMMENT ON COLUMN public.matchmaking_trait_selections.created_at IS '本次选用时间';

ALTER TABLE public.matchmaking_trait_selections ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.matchmaking_trait_selections FORCE ROW LEVEL SECURITY;

CREATE POLICY application_account ON public.matchmaking_trait_selections USING (true) WITH CHECK (true);

REVOKE ALL ON public.matchmaking_trait_selections FROM PUBLIC;

CREATE INDEX matchmaking_trait_selections_trait_idx ON public.matchmaking_trait_selections(trait_id);

CREATE INDEX matchmaking_traits_status_idx ON public.matchmaking_traits(status, created_at, id);
