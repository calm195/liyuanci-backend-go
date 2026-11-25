DROP INDEX public.assets_student_card_draft_slot;
ALTER TABLE public.assets
    DROP CONSTRAINT assets_student_card_slot_check,
    DROP COLUMN scanned_at,
    DROP COLUMN student_card_slot;
