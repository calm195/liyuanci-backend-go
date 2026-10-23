LOCK TABLE public.moderation_tasks,evidence.crisis_cases,evidence.evidence_actions,evidence.crisis_deliveries,public.care_cards IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM public.moderation_tasks WHERE crisis_recheck) OR EXISTS(SELECT 1 FROM evidence.crisis_deliveries) OR EXISTS(SELECT 1 FROM public.care_cards) OR EXISTS(SELECT 1 FROM evidence.crisis_cases WHERE source_task_id IS NOT NULL) OR EXISTS(SELECT 1 FROM evidence.evidence_actions WHERE reason_cipher IS NOT NULL) THEN
 RAISE EXCEPTION '存在危机服务数据，拒绝丢失案件或待办的回退';
 END IF;
END $$;
ALTER TABLE public.moderation_tasks DROP COLUMN crisis_recheck;
DROP TABLE public.care_cards;
DROP TABLE evidence.crisis_deliveries;
ALTER TABLE evidence.evidence_actions DROP COLUMN reason_cipher;
ALTER TABLE evidence.crisis_cases DROP COLUMN version,DROP COLUMN source_task_id,DROP COLUMN handoff_at;
