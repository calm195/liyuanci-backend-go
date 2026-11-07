ALTER TABLE public.wordlist_heads
    DROP CONSTRAINT wordlist_heads_kind_check,
    ADD CONSTRAINT wordlist_heads_kind_check CHECK (kind IN ('crisis', 'school', 'ordinary_violation', 'builtin'));

INSERT INTO public.wordlist_heads(kind) VALUES ('builtin');

ALTER TABLE public.wordlist_versions
    ADD COLUMN upstream_version text NOT NULL DEFAULT '',
    ADD COLUMN upstream_digest text NOT NULL DEFAULT '',
    ADD CONSTRAINT wordlist_versions_upstream_check CHECK (
        (type = 'builtin' AND upstream_version <> '' AND upstream_digest ~ '^[0-9a-f]{64}$')
        OR (type <> 'builtin' AND upstream_version = '' AND upstream_digest = '')
    );

ALTER TABLE public.wordlist_entries
    ADD COLUMN upstream_label text NOT NULL DEFAULT '',
    ADD COLUMN upstream_action text NOT NULL DEFAULT '',
    DROP CONSTRAINT wordlist_entries_version_id_entry_key,
    ADD CONSTRAINT wordlist_entries_version_entry_label_key UNIQUE (version_id, entry, upstream_label),
    ADD CONSTRAINT wordlist_entries_upstream_check CHECK (
        (upstream_label = '' AND upstream_action = '')
        OR (upstream_label <> '' AND upstream_action IN ('review', 'hint') AND action IN ('review', 'hint'))
    );

COMMENT ON TABLE public.wordlist_heads IS '自定义三类及内置词库的并发基准与生效指针';
COMMENT ON COLUMN public.wordlist_versions.upstream_version IS '内置词库源制品版本，自定义词库为空';
COMMENT ON COLUMN public.wordlist_versions.upstream_digest IS '内置源词库 SHA-256 摘要，自定义词库为空';
COMMENT ON COLUMN public.wordlist_entries.upstream_label IS '内置词条原始上游标签，自定义词条为空';
COMMENT ON COLUMN public.wordlist_entries.upstream_action IS '导入时的默认处置，用于在更新上游时保留本地覆盖';
