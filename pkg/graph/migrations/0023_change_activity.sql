-- A change may be scoped to one Activity (architecture plan "Activity concept": a methodology@Process/Step/
-- Method/MethodStep node key): "Request -> Create Change -> Define scope -> Execute". Empty: no
-- activity-relative gating beyond a node type's own lifecycle.
ALTER TABLE change ADD COLUMN activity_ref text NOT NULL DEFAULT '';
