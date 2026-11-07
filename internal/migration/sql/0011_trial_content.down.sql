DROP FUNCTION public.read_whisper_review_title(bytea,uuid);
ALTER TABLE public.wish_versions DROP COLUMN public_title, DROP COLUMN public_reward_text;
ALTER TABLE public.whisper_post_versions DROP COLUMN title_cipher, DROP COLUMN public_title;
ALTER TABLE public.accounts DROP COLUMN avatar_url, DROP COLUMN identity_tags;
ALTER TABLE public.wish_versions DROP CONSTRAINT wish_versions_reward_method_check;
ALTER TABLE public.wish_versions ADD CONSTRAINT wish_versions_reward_method_check CHECK (reward_method IN ('milk_tea','meal','reciprocal_gift','skill_exchange','sincere_thanks'));
