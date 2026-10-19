-- The catalog-wide context-tier pricing boundaries (modelsdev.BandBounds),
-- as one row per boundary. Usage-aggregation queries group each LLM call into
-- a pricing band by counting how many of these its total prompt exceeds; the
-- server prices each band at that model's tiered rates. Refreshed from the
-- embedded models.dev snapshot at startup (see db.DB.refreshTierBounds);
-- the table is derived data, never written elsewhere.
CREATE TABLE IF NOT EXISTS tier_bounds (
  bound INTEGER PRIMARY KEY
);
