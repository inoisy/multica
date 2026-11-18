-- Back to upstream 667's list. Fails while issues with origin_type
-- 'express_chat' exist, the same as reverting 9004.
ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
    CHECK (origin_type IN ('autopilot', 'quick_create', 'lark_chat', 'slack_chat', 'agent_create', 'dingtalk_chat', 'wecom_chat', 'telegram_chat', 'weixin_chat'))
    NOT VALID;
ALTER TABLE issue VALIDATE CONSTRAINT issue_origin_type_check;
