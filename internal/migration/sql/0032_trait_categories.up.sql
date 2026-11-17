ALTER TABLE public.matchmaking_traits ADD COLUMN category text NOT NULL DEFAULT 'uncategorized' CHECK (category IN ('personality','interest','lifestyle','uncategorized'));
COMMENT ON COLUMN public.matchmaking_traits.category IS '共享特质分类：性格、兴趣、生活或待分类';
CREATE INDEX matchmaking_traits_category_idx ON public.matchmaking_traits(category,status);
ALTER TABLE public.matchmaking_traits ADD COLUMN enabled boolean NOT NULL DEFAULT true;
COMMENT ON COLUMN public.matchmaking_traits.enabled IS '是否允许新增选用，独立于审核结论';
