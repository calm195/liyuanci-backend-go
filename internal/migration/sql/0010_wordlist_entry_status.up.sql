ALTER TABLE public.wordlist_entries
    ADD COLUMN disabled boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN public.wordlist_entries.disabled IS '是否从该版本的扫描规则中停用，存量词条默认启用';
