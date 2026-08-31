-- Representative, deterministic content for visual and browser audits.
-- Run only against a disposable database after the app has created its owner.
\set ON_ERROR_STOP on

INSERT INTO email_accounts (
  user_id, mirror_account_id, provider, address, label, username, host, port,
  smtp_host, smtp_port, cred_ciphertext, backfill_days, retention_days,
  sync_enabled, last_sync_at
)
SELECT id, 'audit-primary', 'imap', 'owner@example.test', 'Personal',
       'owner@example.test', 'mail.example.test', 993,
       'smtp.example.test', 587, 'visual-audit-only', 90, 0, false,
       now() - interval '4 minutes'
FROM users WHERE email = 'owner@owner.local'
ON CONFLICT (mirror_account_id) DO NOTHING;

INSERT INTO mail_mailboxes (account_id, id, name, role, native) VALUES
  ('audit-primary', 'inbox', 'Inbox', 'inbox', 'INBOX'),
  ('audit-primary', 'sent', 'Sent', 'sent', 'Sent'),
  ('audit-primary', 'archive', 'Archive', 'archive', 'Archive'),
  ('audit-primary', 'receipts', 'Receipts', NULL, 'Receipts')
ON CONFLICT DO NOTHING;

INSERT INTO mail_messages (
  account_id, id, thread_id, fingerprint, subject, sent_at, received_at,
  from_addrs, to_addrs, keywords, has_attachment, size, preview,
  message_id_header
) VALUES
  ('audit-primary','m-001','th-studio','fp-001','Studio review: a calmer first screen',now()-interval '48 minutes',now()-interval '48 minutes','[{"name":"Mara Lin","email":"mara@fieldwork.test"}]','[{"name":"Tyler","email":"owner@example.test"}]','[]',true,18450,'I tightened the opening screen and left two decisions for you near the bottom.','<m-001@fieldwork.test>'),
  ('audit-primary','m-002','th-dinner','fp-002','Dinner on Thursday?',now()-interval '3 hours',now()-interval '3 hours','[{"name":"Jon Bell","email":"jon@example.test"}]','[{"name":"Tyler","email":"owner@example.test"}]','[]',false,2400,'I booked the little place on Main for seven. Does that still work?','<m-002@example.test>'),
  ('audit-primary','m-003','th-project','fp-003','Re: launch checklist',now()-interval '1 day',now()-interval '1 day','[{"name":"Ari Patel","email":"ari@northstar.test"}]','[{"email":"owner@example.test"}]','[]',false,3900,'The accessibility notes are resolved. The last open item is the export copy.','<m-003@northstar.test>'),
  ('audit-primary','m-004','th-project','fp-004','Re: launch checklist',now()-interval '18 hours',now()-interval '18 hours','[{"name":"Tyler","email":"owner@example.test"}]','[{"name":"Ari Patel","email":"ari@northstar.test"}]','["$seen"]',false,2600,'Perfect. I will give the export one final read and send it over.','<m-004@example.test>'),
  ('audit-primary','m-005','th-newsletter','fp-005','The small-web issue',now()-interval '5 hours',now()-interval '5 hours','[{"name":"Dense Discovery","email":"hello@dense.test"}]','[{"email":"owner@example.test"}]','[]',false,12500,'A thoughtful collection of tools, type, and independent internet projects.','<m-005@dense.test>'),
  ('audit-primary','m-006','th-essay','fp-006','Against dashboards',now()-interval '2 days',now()-interval '2 days','[{"name":"The Marginalian","email":"letters@marginalian.test"}]','[{"email":"owner@example.test"}]','["$seen"]',false,22100,'Why a good tool should disappear into the work instead of narrating itself.','<m-006@marginalian.test>'),
  ('audit-primary','m-007','th-receipt','fp-007','Your train receipt · 2418',now()-interval '7 hours',now()-interval '7 hours','[{"name":"VIA Rail","email":"receipts@viarail.test"}]','[{"email":"owner@example.test"}]','[]',true,83210,'Vancouver to Seattle · September 18 · Coach 4, seat 12A.','<m-007@viarail.test>'),
  ('audit-primary','m-008','th-invoice','fp-008','Invoice paid — August',now()-interval '3 days',now()-interval '3 days','[{"name":"Fathom Hosting","email":"billing@fathom.test"}]','[{"email":"owner@example.test"}]','["$seen"]',false,4050,'Payment received. No action is needed.','<m-008@fathom.test>'),
  ('audit-primary','m-009','th-screener-one','fp-009','A quick partnership idea',now()-interval '26 minutes',now()-interval '26 minutes','[{"name":"Nora James","email":"nora@unknown.test"}]','[{"email":"owner@example.test"}]','[]',false,5100,'I have been following your work and wanted to share one specific idea.','<m-009@unknown.test>'),
  ('audit-primary','m-010','th-screener-two','fp-010','Your weekly growth report',now()-interval '1 hour',now()-interval '1 hour','[{"name":"Metric Pilot","email":"reports@metricpilot.test"}]','[{"email":"owner@example.test"}]','[]',false,6340,'Traffic increased twelve percent. Open the report for the complete breakdown.','<m-010@metricpilot.test>'),
  ('audit-primary','m-011','th-snooze-one','fp-011','Renew the library card',now()-interval '1 day',now()-interval '1 day','[{"name":"VPL","email":"hello@vpl.test"}]','[{"email":"owner@example.test"}]','["$seen"]',false,1800,'Your card expires soon. Renew online in less than two minutes.','<m-011@vpl.test>'),
  ('audit-primary','m-012','th-snooze-two','fp-012','Cabin check-in details',now()-interval '4 days',now()-interval '4 days','[{"name":"Lena Hart","email":"lena@weekend.test"}]','[{"email":"owner@example.test"}]','["$seen"]',false,3100,'The key is in the lockbox. I will send the code the morning you arrive.','<m-012@weekend.test>'),
  ('audit-primary','m-013','th-read','fp-013','A practical guide to local-first software',now()-interval '6 days',now()-interval '6 days','[{"name":"Ink & Switch","email":"research@ink.test"}]','[{"email":"owner@example.test"}]','["$seen"]',false,17400,'Seven principles for software that keeps user data close and resilient.','<m-013@ink.test>'),
  ('audit-primary','m-014','th-family','fp-014','Photos from the island',now()-interval '9 days',now()-interval '9 days','[{"name":"Mom","email":"mom@example.test"}]','[{"email":"owner@example.test"}]','["$seen"]',true,3284000,'The rain stopped right before the ferry. These are the good ones.','<m-014@example.test>')
ON CONFLICT DO NOTHING;

INSERT INTO mail_message_mailboxes (account_id, message_id, mailbox_id)
SELECT 'audit-primary', id,
  CASE WHEN id IN ('m-004') THEN 'sent' WHEN id IN ('m-007','m-008') THEN 'receipts' ELSE 'inbox' END
FROM mail_messages WHERE account_id = 'audit-primary'
ON CONFLICT DO NOTHING;

INSERT INTO mail_bodies (account_id, message_id, text_body, html_body, parts, fetched_at) VALUES
  ('audit-primary','m-001','Hi Tyler,\n\nI tightened the opening screen and left two decisions for you near the bottom. The whole thing should feel quieter now.\n\nMara','<p>Hi Tyler,</p><p>I tightened the opening screen and left two decisions for you near the bottom. The whole thing should feel quieter now.</p><img src="https://tracker.example.test/open/abc" width="1" height="1"><p>Mara</p>','[{"part_id":"2","filename":"studio-review.pdf","type":"application/pdf","size":18450}]',now()),
  ('audit-primary','m-002','I booked the little place on Main for seven. Does that still work?','', '[]',now()),
  ('audit-primary','m-003','The accessibility notes are resolved. The last open item is the export copy.','', '[]',now()),
  ('audit-primary','m-004','Perfect. I will give the export one final read and send it over.','', '[]',now()),
  ('audit-primary','m-005','A thoughtful collection of tools, type, and independent internet projects.','', '[]',now()),
  ('audit-primary','m-006','Why a good tool should disappear into the work instead of narrating itself.','', '[]',now()),
  ('audit-primary','m-007','Vancouver to Seattle\nSeptember 18\nCoach 4 · Seat 12A\nTotal: $84.00','', '[{"part_id":"2","filename":"receipt-2418.pdf","type":"application/pdf","size":83210}]',now()),
  ('audit-primary','m-008','Payment received. No action is needed.','', '[]',now()),
  ('audit-primary','m-009','I have been following your work and wanted to share one specific idea.','', '[]',now()),
  ('audit-primary','m-010','Traffic increased twelve percent. Open the report for the complete breakdown.','', '[]',now()),
  ('audit-primary','m-011','Your card expires soon. Renew online in less than two minutes.','', '[]',now()),
  ('audit-primary','m-012','The key is in the lockbox. I will send the code the morning you arrive.','', '[]',now()),
  ('audit-primary','m-013','Seven principles for software that keeps user data close and resilient.','', '[]',now()),
  ('audit-primary','m-014','The rain stopped right before the ferry. These are the good ones.','', '[]',now())
ON CONFLICT DO NOTHING;

INSERT INTO hey_messages (user_id, account_id, message_id, bucket, read_at, set_aside_until)
SELECT u.id, 'audit-primary', v.message_id, v.bucket, v.read_at, v.set_aside_until
FROM users u CROSS JOIN (VALUES
  ('m-001','imbox',NULL::timestamptz,NULL::timestamptz),
  ('m-002','imbox',NULL,NULL),
  ('m-003','imbox',now()-interval '20 hours',NULL),
  ('m-004','imbox',now()-interval '17 hours',NULL),
  ('m-005','feed',NULL,NULL),
  ('m-006','feed',now()-interval '1 day',NULL),
  ('m-007','paper_trail',NULL,NULL),
  ('m-008','paper_trail',now()-interval '2 days',NULL),
  ('m-009','screener',NULL,NULL),
  ('m-010','screener',NULL,NULL),
  ('m-011','set_aside',now()-interval '1 day',now()+interval '2 days'),
  ('m-012','set_aside',now()-interval '3 days',now()+interval '9 days'),
  ('m-013','later',now()-interval '5 days',NULL),
  ('m-014','imbox',now()-interval '8 days',NULL)
) AS v(message_id,bucket,read_at,set_aside_until)
WHERE u.email='owner@owner.local'
ON CONFLICT (user_id,account_id,message_id) DO NOTHING;

INSERT INTO hey_senders (user_id, sender_key, allowed, route, decided_at)
SELECT u.id, v.sender_key, true, v.route, now()-interval '14 days'
FROM users u CROSS JOIN (VALUES
  ('mara@fieldwork.test','imbox'), ('jon@example.test','imbox'),
  ('ari@northstar.test','imbox'), ('hello@dense.test','feed'),
  ('letters@marginalian.test','feed'), ('receipts@viarail.test','paper_trail'),
  ('billing@fathom.test','paper_trail'), ('hello@vpl.test','imbox'),
  ('lena@weekend.test','imbox'), ('research@ink.test','feed'),
  ('mom@example.test','imbox')
) AS v(sender_key,route)
WHERE u.email='owner@owner.local'
ON CONFLICT DO NOTHING;

INSERT INTO board_cards (user_id, thread_key, title, note, done_at, created_at)
SELECT u.id, v.thread_key, v.title, v.note, v.done_at, v.created_at
FROM users u CROSS JOIN (VALUES
  (NULL::text,'Write the launch note','Keep it short. Explain the promise before the machinery.',NULL::timestamptz,now()-interval '2 days'),
  ('th-family','Photos from the island','Choose three for the print.',NULL,now()-interval '1 day'),
  (NULL,'Renew domain','Moved to the new registrar.',now()-interval '3 hours',now()-interval '4 days')
) AS v(thread_key,title,note,done_at,created_at)
WHERE u.email='owner@example.test'
ON CONFLICT DO NOTHING;

INSERT INTO sticky_notes (user_id,x,y,text,color,created_at,updated_at)
SELECT u.id,v.x,v.y,v.text,v.color,now(),now()
FROM users u CROSS JOIN (VALUES
  (48,46,'What would make opening email feel lighter?',0),
  (342,82,'Launch\n\n• final export copy\n• mobile pass\n• invite two friends',2),
  (154,292,'Friday\nWalk the seawall before the calls.',4),
  (486,330,'Keep the product small enough to understand.',1)
) AS v(x,y,text,color)
WHERE u.email='owner@example.test';
