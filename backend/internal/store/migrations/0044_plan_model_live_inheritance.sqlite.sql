-- Migrate legacy plan-model snapshots to live inheritance. Keys previously had
-- their plan's allowed models copied into api_key_model_access at assign time,
-- which froze them against later plan edits. Going forward, keys without an
-- explicit override follow the plan live.
--
-- Clear rows only for keys whose stored model set is exactly the plan's current
-- model list (a pure snapshot). Genuine per-key overrides differ and survive.
-- A key qualifies when every stored model is a token in the plan list and the
-- row count matches the plan's token count.
DELETE FROM api_key_model_access
WHERE api_key_id IN (
    SELECT k.id
    FROM api_keys k
    JOIN plans p ON p.id = k.plan_id
    WHERE p.allowed_models <> ''
      AND (SELECT COUNT(*) FROM api_key_model_access amo WHERE amo.api_key_id = k.id)
          = (LENGTH(p.allowed_models) - LENGTH(REPLACE(p.allowed_models, ',', '')) + 1)
      AND NOT EXISTS (
          SELECT 1 FROM api_key_model_access amo
          WHERE amo.api_key_id = k.id
            AND instr(',' || replace(p.allowed_models, ' ', '') || ',',
                      ',' || replace(amo.model, ' ', '') || ',') = 0
      )
);
