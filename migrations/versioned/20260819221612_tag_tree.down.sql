DO $$ BEGIN RAISE NOTICE '[Migration 000084 down] Dropping tag_tree'; END $$;

DROP TABLE IF EXISTS tag_tree;
