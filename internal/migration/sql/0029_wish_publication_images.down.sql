DROP TABLE public.wish_version_images;
DROP TABLE public.wish_images;
ALTER TABLE public.wishes DROP COLUMN first_published_at, DROP COLUMN publish_intent, DROP COLUMN identity_mode, DROP COLUMN comments_open, DROP COLUMN comments_epoch;
ALTER TABLE public.wishes DROP CONSTRAINT wishes_duration_days_check;
ALTER TABLE public.wishes ADD CONSTRAINT wishes_duration_days_check CHECK (duration_days BETWEEN 1 AND 30);
COMMENT ON COLUMN public.wishes.duration_days IS '有效期整数天数，默认七天';
