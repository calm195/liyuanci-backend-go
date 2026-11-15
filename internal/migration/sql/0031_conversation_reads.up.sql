CREATE TABLE public.conversation_reads (
 conversation_id uuid NOT NULL REFERENCES public.conversations(id) ON DELETE CASCADE,
 account_id uuid NOT NULL REFERENCES public.accounts(id) ON DELETE CASCADE,
 last_read_seq bigint NOT NULL CHECK(last_read_seq>0),
 updated_at timestamptz NOT NULL,
 PRIMARY KEY(conversation_id, account_id)
);
CREATE INDEX conversation_reads_account ON public.conversation_reads(account_id);
COMMENT ON TABLE public.conversation_reads IS '会话参与者各自的阅读位置，不向对方提供已读回执';
COMMENT ON COLUMN public.conversation_reads.conversation_id IS '所属会话';
COMMENT ON COLUMN public.conversation_reads.account_id IS '阅读者账号';
COMMENT ON COLUMN public.conversation_reads.last_read_seq IS '本人已展示的最大送达序号，只增不减';
COMMENT ON COLUMN public.conversation_reads.updated_at IS '阅读位置最近推进时间';
