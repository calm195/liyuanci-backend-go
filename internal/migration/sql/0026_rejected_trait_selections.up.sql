-- 已驳回标签退出个人资料；推进账号版本使旧报名预览重新确认。
UPDATE public.accounts
SET version = version + 1
WHERE id IN (
    SELECT s.account_id
    FROM public.matchmaking_trait_selections s
    JOIN public.matchmaking_traits t ON t.id = s.trait_id
    WHERE t.status = 'rejected'
);
DELETE FROM public.matchmaking_trait_selections s
USING public.matchmaking_traits t
WHERE s.trait_id = t.id AND t.status = 'rejected';
