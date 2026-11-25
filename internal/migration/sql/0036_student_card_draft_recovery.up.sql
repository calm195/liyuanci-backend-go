ALTER TABLE public.assets ADD COLUMN student_card_cleanup_at timestamptz;
COMMENT ON COLUMN public.assets.student_card_cleanup_at IS '已移除学生卡密文的下次清理时间或本次清理租约截止时间';

CREATE INDEX assets_student_card_cleanup_due ON public.assets (student_card_cleanup_at, id)
WHERE student_card_slot = 'removed' AND student_card_cleanup_at IS NOT NULL;

WITH eligible AS (
    SELECT a.id, a.scan_status, count(*) OVER (PARTITION BY a.owner_id) AS photos
    FROM public.assets a
    JOIN public.accounts u ON u.id = a.owner_id AND u.lifecycle = 'active'
    LEFT JOIN LATERAL (
        SELECT r.status FROM public.identity_reviews r
        WHERE r.account_id = a.owner_id ORDER BY r.version DESC LIMIT 1
    ) review ON true
    WHERE a.purpose = 'student_card' AND a.student_card_slot IS NULL
      AND u.verification IN ('unverified', 'rejected')
      AND COALESCE(review.status, 'rejected') = 'rejected'
      AND ((a.status = 'pending' AND a.scan_status = 'pending')
        OR (a.status = 'active' AND a.scan_status = 'approved'))
      AND NOT EXISTS (SELECT 1 FROM public.identity_reviews r WHERE r.asset_id = a.id)
      AND NOT EXISTS (
          SELECT 1 FROM public.assets draft
          WHERE draft.owner_id = a.owner_id AND draft.purpose = 'student_card'
            AND (draft.student_card_slot IN ('candidate', 'removed')
              OR (draft.student_card_slot = 'current'
                AND (a.scan_status = 'approved' OR draft.created_at >= a.created_at)))
      )
)
UPDATE public.assets a
SET student_card_slot = CASE WHEN eligible.scan_status = 'approved' THEN 'current' ELSE 'candidate' END
FROM eligible WHERE a.id = eligible.id AND eligible.photos = 1;

UPDATE public.assets a SET student_card_cleanup_at = clock_timestamp()
WHERE a.student_card_slot = 'removed' AND a.status NOT IN ('deleting', 'deleted')
  AND NOT EXISTS (SELECT 1 FROM public.identity_reviews r WHERE r.asset_id = a.id);
