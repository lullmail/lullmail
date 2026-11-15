-- Empties a DEMO database back to "freshly installed, owner exists" so the
-- made-up mail in dashboard/e2e/seed.sql can be loaded again. Run by
-- tools/demo/reset.sh on a timer. NEVER run this against a real install: it
-- deletes every mailbox, message, rule, note and session.
--
-- Everything goes except what makes the install an install: the owner row,
-- the two migration ledgers, and the installation settings. Truncating by
-- "all tables but these" means a table added by a later migration is reset
-- too, without this file having to learn its name.
DO $$
DECLARE t text;
BEGIN
  FOR t IN
    SELECT tablename FROM pg_tables
    WHERE schemaname = 'public'
      AND tablename NOT IN ('users', 'app_migrations', 'mail_migrations', 'app_settings')
  LOOP
    EXECUTE format('TRUNCATE TABLE %I CASCADE', t);
  END LOOP;
END $$;
