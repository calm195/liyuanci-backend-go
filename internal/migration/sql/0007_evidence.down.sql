-- 仅由迁移器在锁定并确认全部业务表为空后执行。
DROP TABLE evidence.assets,
    evidence.crisis_cases,
    evidence.crisis_deliveries,
    evidence.evidence_actions,
    evidence.evidence_cases,
    evidence.evidence_items;
DROP SCHEMA evidence;
