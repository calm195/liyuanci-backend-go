DELETE FROM public.wordlist_heads WHERE kind = 'builtin';
ALTER TABLE public.wordlist_heads
    DROP CONSTRAINT wordlist_heads_kind_check,
    ADD CONSTRAINT wordlist_heads_kind_check CHECK (kind IN ('crisis', 'school', 'ordinary_violation'));
COMMENT ON TABLE public.wordlist_heads IS '三类词库的并发基准与生效指针，草案创建和发布均推进代次';

ALTER TABLE public.wordlist_entries
    DROP CONSTRAINT wordlist_entries_upstream_check,
    DROP CONSTRAINT wordlist_entries_version_entry_label_key,
    DROP COLUMN upstream_label,
    DROP COLUMN upstream_action,
    ADD CONSTRAINT wordlist_entries_version_id_entry_key UNIQUE (version_id, entry);

ALTER TABLE public.wordlist_versions
    DROP CONSTRAINT wordlist_versions_upstream_check,
    DROP COLUMN upstream_version,
    DROP COLUMN upstream_digest;
