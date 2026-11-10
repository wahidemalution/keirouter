-- Token package keys follow the token plan's allowed_models. Drop the
-- per-key copies written at key creation so later plan edits reach every
-- existing token key.
DELETE FROM api_key_model_access WHERE api_key_id IN (SELECT key_id FROM token_subscriptions);
