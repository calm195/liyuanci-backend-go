ALTER TABLE public.assets
    ADD COLUMN student_card_slot text,
    ADD COLUMN scanned_at timestamptz,
    ADD CONSTRAINT assets_student_card_slot_check CHECK (
        student_card_slot IS NULL OR (
            purpose = 'student_card' AND student_card_slot IN ('current', 'candidate', 'removed')
        )
    );
COMMENT ON COLUMN public.assets.student_card_slot IS '本人认证草稿中的位置，当前图、候选图或已移出';
COMMENT ON COLUMN public.assets.scanned_at IS '安全扫描实际结束时间，历史未知记录为空';

CREATE UNIQUE INDEX assets_student_card_draft_slot
    ON public.assets (owner_id, student_card_slot)
    WHERE student_card_slot IN ('current', 'candidate');

WITH eligible AS (
    SELECT a.id, count(*) OVER (PARTITION BY a.owner_id) AS photos
    FROM public.assets a
    WHERE a.purpose = 'student_card' AND a.status = 'active' AND a.scan_status = 'approved'
      AND NOT EXISTS (
          SELECT 1 FROM public.identity_reviews r WHERE r.account_id = a.owner_id
      )
)
UPDATE public.assets a SET student_card_slot = 'current'
FROM eligible WHERE a.id = eligible.id AND eligible.photos = 1;
