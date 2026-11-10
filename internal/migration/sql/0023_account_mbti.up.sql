ALTER TABLE public.accounts
    ADD COLUMN mbti text NOT NULL DEFAULT '',
    ADD CONSTRAINT accounts_mbti_valid CHECK (mbti = '' OR mbti ~ '^[IE][NS][FT][JP]$');

COMMENT ON COLUMN public.accounts.mbti IS '本人填写的MBTI人格类型，空字符串表示未设置，仅本人资料和获授权的活动使用';
