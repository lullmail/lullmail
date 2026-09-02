package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/neutron-build/neutron/mail"
)

type missingBody struct {
	account mail.AccountID
	message mail.MessageID
	mailbox mail.MailboxID
}

func backfillBodies(args []string) error {
	flags := flag.NewFlagSet("backfill-bodies", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	account := flags.String("account", "", "only backfill one mirror account ID")
	limit := flags.Int("limit", 0, "maximum messages to process (0 means all)")
	interval := flags.Duration("interval", 250*time.Millisecond, "minimum delay between provider requests")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *limit < 0 {
		return fmt.Errorf("limit must be at least 0")
	}
	if *interval <= 0 {
		return fmt.Errorf("interval must be greater than 0")
	}

	cfg := loadConfig()
	if cfg.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL not set")
	}
	if err := resolveSecretKey(cfg); err != nil {
		return fmt.Errorf("resolve secret key: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	store, err := mail.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	query := `SELECT m.account_id, m.id,
		COALESCE((SELECT min(mm.mailbox_id) FROM mail_message_mailboxes mm
			WHERE mm.account_id = m.account_id AND mm.message_id = m.id), '')
		FROM mail_messages m
		LEFT JOIN mail_bodies b ON b.account_id = m.account_id AND b.message_id = m.id
		WHERE b.message_id IS NULL AND ($1 = '' OR m.account_id = $1)
		ORDER BY m.account_id, m.received_at DESC NULLS LAST, m.id`
	queryArgs := []any{*account}
	if *limit > 0 {
		query += " LIMIT $2"
		queryArgs = append(queryArgs, *limit)
	}
	rows, err := db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return err
	}
	var pending []missingBody
	for rows.Next() {
		var item missingBody
		if err := rows.Scan(&item.account, &item.message, &item.mailbox); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	app := &App{
		cfg:           cfg,
		db:            db,
		store:         store,
		eng:           mail.NewEngine(store, nil),
		accountStates: map[mail.AccountID]*accountLifecycle{},
	}
	resolve := app.accountResolver()
	var (
		adapter        mail.Adapter
		release        func()
		currentAccount mail.AccountID
		accountErr     error
		lastRequest    time.Time
		fetched        int
		failed         int
	)
	prepared := map[mail.MailboxID]bool{}
	defer func() {
		if release != nil {
			release()
		}
	}()

	fmt.Printf("backfill-bodies: %d missing bodies\n", len(pending))
	for i, item := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.account != currentAccount {
			if release != nil {
				release()
				release = nil
			}
			currentAccount = item.account
			prepared = map[mail.MailboxID]bool{}
			adapter, release, accountErr = resolve(ctx, item.account, mail.Credential{})
			if accountErr != nil {
				fmt.Fprintf(os.Stderr, "[%d/%d] account=%s message=%s error=%v\n", i+1, len(pending), item.account, item.message, accountErr)
				failed++
				continue
			}
		}
		if accountErr != nil {
			fmt.Fprintf(os.Stderr, "[%d/%d] account=%s message=%s error=%v\n", i+1, len(pending), item.account, item.message, accountErr)
			failed++
			continue
		}

		if adapter.Provider() == mail.ProviderIMAP && !prepared[item.mailbox] {
			if item.mailbox == "" {
				fmt.Fprintf(os.Stderr, "[%d/%d] account=%s message=%s error=no mailbox membership\n", i+1, len(pending), item.account, item.message)
				failed++
				continue
			}
			if err := waitForProvider(ctx, lastRequest, *interval); err != nil {
				return err
			}
			cur, err := store.Cursor(ctx, item.account, item.mailbox)
			if err == nil {
				_, err = adapter.Sync(ctx, item.mailbox, cur)
			}
			lastRequest = time.Now()
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%d/%d] account=%s message=%s error=select mailbox %s: %v\n", i+1, len(pending), item.account, item.message, item.mailbox, err)
				failed++
				continue
			}
			prepared[item.mailbox] = true
		}

		if err := waitForProvider(ctx, lastRequest, *interval); err != nil {
			return err
		}
		_, err := app.eng.Body(ctx, item.account, item.message, adapter)
		lastRequest = time.Now()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%d/%d] account=%s message=%s error=%v\n", i+1, len(pending), item.account, item.message, err)
			failed++
			continue
		}
		fetched++
		fmt.Printf("[%d/%d] account=%s message=%s cached\n", i+1, len(pending), item.account, item.message)
	}

	fmt.Printf("backfill-bodies: complete cached=%d errors=%d\n", fetched, failed)
	if failed > 0 {
		return fmt.Errorf("%d message(s) failed; rerun to retry them", failed)
	}
	return nil
}

func waitForProvider(ctx context.Context, last time.Time, interval time.Duration) error {
	if last.IsZero() || interval <= 0 {
		return nil
	}
	wait := time.Until(last.Add(interval))
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
