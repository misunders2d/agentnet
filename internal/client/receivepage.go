package client

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A device coming back to a backlog takes it in pages, as TDLib's MessageDb
// batches its writes (MessageDb.cpp:1192-1221) and acknowledges in pages
// (SessionConnection.cpp:897-910): the stream reader hands over what is
// already buffered, up to receivePageMax messages (readStream); each is
// verified, decrypted and checked as before, outside any transaction, and
// the page's plain turns are then stored in ONE transaction (a savepoint
// each), with one change and one receipt pass for the page.
//
// Only a plain turn of a two-person chat is staged (pageTurn). Anything
// else, and a turn replying to one staged in the same page (its checks
// could not see it), first stores the page so far: stream order holds for
// every write, and every check reads what serial admission would have read.
// A copy that fails verification is held as today and the page goes on.
const receivePageMax = 100

type receivePage struct {
	cur   envelope.Envelope
	rows  []pageRow
	names map[string]bool // the ids and logical ids staged in this page
}

type pageRow struct {
	env    envelope.Envelope
	store  func(*sql.Tx, txStmts) (string, bool, error)
	settle func(string, error) error
}

type receivePageKey struct{}

// pageFault, set only by tests, fails a page just before its commit.
var pageFault func() error

// pageRowFault, set only by tests, fails one staged turn's write.
var pageRowFault func(envelope.Envelope) error

func receivingPage(ctx context.Context) *receivePage {
	p, _ := ctx.Value(receivePageKey{}).(*receivePage)
	return p
}

func (p *receivePage) staged() []pageRow {
	if p == nil {
		return nil
	}
	return p.rows
}

func (p *receivePage) stage(in envelope.Inner, store func(*sql.Tx, txStmts) (string, bool, error), settle func(string, error) error) {
	p.rows = append(p.rows, pageRow{env: p.cur, store: store, settle: settle})
	p.names[in.ID], p.names[in.LID] = true, true
}

// pageTurn reports whether in may be staged: a plain message of a
// two-person chat, replying to nothing staged in this page.
func (p *receivePage) pageTurn(in envelope.Inner) bool {
	if in.V != envelope.Version2 || in.Kind != envelope.KindMessage || in.Sub != "" || in.Human != nil || in.PID != "" || in.Target != nil || envelope.TopicOrganization(in.TopicEvent) || p.names[in.ReplyTo] {
		return false
	}
	root, err := protocol.ParseConvRoot(in.Root)
	return err == nil && root.Kind != protocol.ConvKindGroup
}

// openedKey carries an envelope the page opened to classify it, so that
// verifyAndStore does not open it again.
type openedKey struct{}

type opened struct {
	id     string
	sender identity.Public
	in     envelope.Inner
}

func (a *Agent) open(ctx context.Context, env envelope.Envelope, sender identity.Public) (envelope.Inner, error) {
	if o, ok := ctx.Value(openedKey{}).(opened); ok && o.id == env.ID && o.sender.Fingerprint() == sender.Fingerprint() {
		return o.in, nil
	}
	return envelope.Open(env, a.id, a.Address, sender)
}

// receivePage admits envs in stream order, a page at a time. An error is
// what ends the stream today: the page stored up to it stays stored.
func (a *Agent) receivePage(ctx context.Context, envs []envelope.Envelope) error {
	p := &receivePage{names: map[string]bool{}}
	pctx := context.WithValue(ctx, receivePageKey{}, p)
	for _, env := range envs {
		in, sender, ok := a.classify(env)
		if !ok || !p.pageTurn(in) {
			if err := a.storePage(ctx, p); err != nil {
				return err
			}
			if err := a.storeReceived(ctx, env); err != nil {
				return notProcessed(env, err)
			}
			continue
		}
		p.cur = env
		if err := a.storeReceived(context.WithValue(pctx, openedKey{}, opened{env.ID, sender, in}), env); err != nil {
			if e := a.storePage(ctx, p); e != nil {
				return e
			}
			return notProcessed(env, err)
		}
	}
	return a.storePage(ctx, p)
}

func notProcessed(env envelope.Envelope, err error) error {
	return errors.Join(errors.New("message "+env.ID+" not processed yet"), err)
}

// classify opens env with its pinned sender key; ok is false for anything
// admission must look up or hold first (the serial path does that).
func (a *Agent) classify(env envelope.Envelope) (envelope.Inner, identity.Public, bool) {
	if env.To != a.Address {
		return envelope.Inner{}, identity.Public{}, false
	}
	sender, _, found, err := a.store.peer(env.From)
	if err != nil || !found {
		return envelope.Inner{}, identity.Public{}, false
	}
	in, err := envelope.Open(env, a.id, a.Address, sender)
	return in, sender, err == nil
}

// storePage stores the staged turns in one transaction, then settles each
// as serial admission would have. The first that fails is rolled back to
// its savepoint and settled with its error (held, or the stream ends); the
// turns staged after it are admitted again one at a time.
func (a *Agent) storePage(ctx context.Context, p *receivePage) error {
	rows := p.rows
	p.rows, p.names = nil, map[string]bool{}
	if len(rows) == 0 {
		return nil
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st := txStmts{}
	res := make([]string, len(rows))
	kept, failed := len(rows), error(nil)
	for i, r := range rows {
		if _, err := tx.Exec(`SAVEPOINT receive_page`); err != nil {
			return err
		}
		var keep bool
		if pageRowFault != nil {
			failed = pageRowFault(r.env)
		}
		if failed == nil {
			res[i], keep, failed = r.store(tx, st)
		}
		if failed != nil || !keep {
			if _, err := tx.Exec(`ROLLBACK TO receive_page`); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`RELEASE receive_page`); err != nil {
			return err
		}
		if failed != nil {
			kept = i
			break
		}
	}
	if pageFault != nil {
		if err := pageFault(); err != nil {
			return err // as a crash before the commit (receivepage_test.go)
		}
	}
	if err := a.store.done(tx.Commit()); err != nil {
		return err // nothing stored: the Hub pushes them again
	}
	settle := func(r pageRow, res string, err error) error {
		if err = r.settle(res, err); err == nil {
			return nil
		}
		if err = a.admissionFailed(withRelayIngress(ctx, r.env.ID), r.env, err); err == nil {
			return nil
		}
		return notProcessed(r.env, err)
	}
	for i := range kept {
		if err := settle(rows[i], res[i], nil); err != nil {
			return err
		}
		a.receiveFails.clear(rows[i].env.ID) // settled durably: its transient failures start again from none
	}
	if kept == len(rows) {
		return nil
	}
	if err := settle(rows[kept], "", failed); err != nil {
		return err
	}
	for _, r := range rows[kept+1:] {
		if err := a.storeReceived(ctx, r.env); err != nil {
			return notProcessed(r.env, err)
		}
	}
	return nil
}

// txStmts prepares each statement once for one transaction (a page):
// parsing an insert and compiling its triggers costs more than running it.
// A nil txStmts runs each directly. Statements close with the transaction.
type txStmts map[string]*sql.Stmt

func (c txStmts) exec(tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	if c == nil {
		return tx.Exec(query, args...)
	}
	st := c[query]
	if st == nil {
		var err error
		if st, err = tx.Prepare(query); err != nil {
			return nil, fmt.Errorf("prepare: %w", err)
		}
		c[query] = st
	}
	return st.Exec(args...)
}
