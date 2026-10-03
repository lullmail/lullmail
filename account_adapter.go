package main

// Resolver consumers retain their own context after dialing. Binding each
// operation to the account lease closes that gap without changing the
// engine's provider interface or losing optional provider capabilities.

import (
	"context"
	"io"

	"github.com/neutron-build/neutron/mail"
)

type accountAdapter struct {
	mail.Adapter
	lifetime context.Context
}

func bindAccountAdapter(adapter mail.Adapter, lifetime context.Context) mail.Adapter {
	base := &accountAdapter{Adapter: adapter, lifetime: lifetime}
	selector, selects := adapter.(mail.MailboxSelector)
	appender, appends := adapter.(mail.Appender)
	switch {
	case selects && appends:
		return &accountSelectingAppendingAdapter{accountAdapter: base, selector: selector, appender: appender}
	case selects:
		return &accountSelectingAdapter{accountAdapter: base, selector: selector}
	case appends:
		return &accountAppendingAdapter{accountAdapter: base, appender: appender}
	default:
		return base
	}
}

func (a *accountAdapter) Mailboxes(ctx context.Context) ([]mail.Mailbox, error) {
	ctx, done := joinAccountContext(ctx, a.lifetime)
	defer done()
	return a.Adapter.Mailboxes(ctx)
}

func (a *accountAdapter) Sync(ctx context.Context, box mail.MailboxID, cursor mail.Cursor) (*mail.Changes, error) {
	ctx, done := joinAccountContext(ctx, a.lifetime)
	defer done()
	return a.Adapter.Sync(ctx, box, cursor)
}

func (a *accountAdapter) Envelopes(ctx context.Context, ids []mail.MessageID) ([]mail.Envelope, error) {
	ctx, done := joinAccountContext(ctx, a.lifetime)
	defer done()
	return a.Adapter.Envelopes(ctx, ids)
}

func (a *accountAdapter) Body(ctx context.Context, id mail.MessageID) (*mail.Body, error) {
	ctx, done := joinAccountContext(ctx, a.lifetime)
	defer done()
	return a.Adapter.Body(ctx, id)
}

func (a *accountAdapter) Apply(ctx context.Context, op mail.Operation) error {
	ctx, done := joinAccountContext(ctx, a.lifetime)
	defer done()
	return a.Adapter.Apply(ctx, op)
}

// Raw and Attachment return live streams. Their joined context must last
// until Close, rather than being canceled as soon as the method returns.
func (a *accountAdapter) Raw(ctx context.Context, id mail.MessageID) (io.ReadCloser, error) {
	ctx, done := joinAccountContext(ctx, a.lifetime)
	stream, err := a.Adapter.Raw(ctx, id)
	if err != nil {
		done()
		return nil, err
	}
	return &accountStream{ReadCloser: stream, done: done}, nil
}

func (a *accountAdapter) Attachment(ctx context.Context, id mail.MessageID, part string) (io.ReadCloser, error) {
	ctx, done := joinAccountContext(ctx, a.lifetime)
	stream, err := a.Adapter.Attachment(ctx, id, part)
	if err != nil {
		done()
		return nil, err
	}
	return &accountStream{ReadCloser: stream, done: done}, nil
}

type accountStream struct {
	io.ReadCloser
	done func()
}

func (r *accountStream) Close() error {
	defer r.done()
	return r.ReadCloser.Close()
}

type accountSelectingAdapter struct {
	*accountAdapter
	selector mail.MailboxSelector
}

func (a *accountSelectingAdapter) SelectMailbox(ctx context.Context, box mail.MailboxID) error {
	ctx, done := joinAccountContext(ctx, a.lifetime)
	defer done()
	return a.selector.SelectMailbox(ctx, box)
}

type accountAppendingAdapter struct {
	*accountAdapter
	appender mail.Appender
}

func (a *accountAppendingAdapter) Append(ctx context.Context, box mail.MailboxID, raw []byte) error {
	ctx, done := joinAccountContext(ctx, a.lifetime)
	defer done()
	return a.appender.Append(ctx, box, raw)
}

type accountSelectingAppendingAdapter struct {
	*accountAdapter
	selector mail.MailboxSelector
	appender mail.Appender
}

func (a *accountSelectingAppendingAdapter) SelectMailbox(ctx context.Context, box mail.MailboxID) error {
	ctx, done := joinAccountContext(ctx, a.lifetime)
	defer done()
	return a.selector.SelectMailbox(ctx, box)
}

func (a *accountSelectingAppendingAdapter) Append(ctx context.Context, box mail.MailboxID, raw []byte) error {
	ctx, done := joinAccountContext(ctx, a.lifetime)
	defer done()
	return a.appender.Append(ctx, box, raw)
}
