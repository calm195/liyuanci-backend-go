CREATE TABLE public.matchmaking_task_library (
    id uuid PRIMARY KEY,
    version bigint NOT NULL CHECK (version > 0),
    kind text NOT NULL CHECK (kind IN ('online','offline')),
    body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 1000 AND btrim(body) <> ''),
    min_day integer NOT NULL DEFAULT 0 CHECK (min_day BETWEEN 0 AND 30),
    enabled boolean NOT NULL DEFAULT true
);
COMMENT ON TABLE public.matchmaking_task_library IS '包分配独立每日任务库';
COMMENT ON COLUMN public.matchmaking_task_library.id IS '任务随机标识';
COMMENT ON COLUMN public.matchmaking_task_library.version IS '乐观版本';
COMMENT ON COLUMN public.matchmaking_task_library.kind IS '线上或线下类型';
COMMENT ON COLUMN public.matchmaking_task_library.body IS '任务正文';
COMMENT ON COLUMN public.matchmaking_task_library.min_day IS '最早可分配日，零表示无额外限制';
COMMENT ON COLUMN public.matchmaking_task_library.enabled IS '是否可供后续活动分配';
ALTER TABLE public.matchmaking_task_library ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.matchmaking_task_library FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.matchmaking_task_library USING (true) WITH CHECK (true);
REVOKE ALL ON public.matchmaking_task_library FROM PUBLIC;

ALTER TABLE public.matchmaking_activities
    ADD COLUMN first_task_id uuid REFERENCES public.matchmaking_task_library(id);
COMMENT ON COLUMN public.matchmaking_activities.first_task_id IS '任务库固定首日任务，空表示沿用活动模板';

ALTER TABLE public.matchmaking_templates
    ADD COLUMN min_day integer NOT NULL DEFAULT 0 CHECK (min_day BETWEEN 0 AND 30);
COMMENT ON COLUMN public.matchmaking_templates.min_day IS '任务库冻结时的最早可分配日，零表示无额外限制';
