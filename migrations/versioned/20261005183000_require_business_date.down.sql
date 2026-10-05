-- fork migration 20261005183000_require_business_date (down)
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS require_business_date;
