package router

// The ledger tap carries each lane's request and answer bodies to router.db's body store
// without a request ever waiting for the write: an offer takes a budget slot and a queue
// place or is refused at once, and one writer goroutine does every PutBody. The queue is
// never closed, so an offer that races a Close can never panic; Close stops new offers and
// writes what is still queued.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/safego"
)

// BodySink is where the tap's writer puts each body; router.db's body store in production.
type BodySink interface {
	PutBody(ctx context.Context, requestID, side string, body []byte, at time.Time) error
}

const (
	// TapBudget is the most body bytes the tap holds waiting for its writer: room for one
	// largest request body (BodyLimitBytes, 64 MiB) and a small share of the router's 1 GiB
	// RSS ceiling.
	TapBudget = 64 << 20

	// tapSlots is how many bodies the queue holds while the writer works through them.
	tapSlots = 256
)

type tapItem struct {
	id       string
	side     string
	encoding string // the answer's Content-Encoding; "" is plain
	body     []byte
	at       time.Time
}

type LedgerTap struct {
	sink      BodySink
	queue     chan tapItem
	queued    atomic.Int64 // bytes offered and not yet written
	budget    int64
	done      chan struct{} // closed by Close
	drained   chan struct{} // closed when the writer returns
	closeOnce sync.Once
	log       func(string)
}

// NewLedgerTap starts a tap whose writer writes every offered body to the sink. A budget of
// 0 or less means TapBudget.
func NewLedgerTap(sink BodySink, budget int64, log func(string)) *LedgerTap {
	if budget <= 0 {
		budget = TapBudget
	}
	if log == nil {
		log = func(line string) { fmt.Fprintln(os.Stderr, line) }
	}
	t := &LedgerTap{
		sink:    sink,
		queue:   make(chan tapItem, tapSlots),
		budget:  budget,
		done:    make(chan struct{}),
		drained: make(chan struct{}),
		log:     log,
	}
	safego.Go(log, func() { t.write() })
	return t
}

// Offer queues one plain body for the writer and answers whether it took it. It never blocks
// and never copies body: the caller hands the slice over and never changes it afterwards.
func (t *LedgerTap) Offer(id, side string, body []byte, at time.Time) bool {
	return t.OfferEncoded(id, side, "", body, at)
}

// OfferEncoded queues one body carried in the named content encoding ("" is plain) and answers
// whether it took it. The writer stores it decoded, so router.db holds plain bytes; a body the
// writer cannot decode is dropped with one log line. It never blocks and never copies body: the
// caller hands the slice over and never changes it afterwards.
func (t *LedgerTap) OfferEncoded(id, side, encoding string, body []byte, at time.Time) bool {
	select {
	case <-t.done:
		return false
	default:
	}
	if t.queued.Add(int64(len(body))) > t.budget {
		t.queued.Add(-int64(len(body)))
		return false
	}
	select {
	case t.queue <- tapItem{id: id, side: side, encoding: encoding, body: body, at: at}:
		return true
	default:
		t.queued.Add(-int64(len(body)))
		return false
	}
}

// write puts every queued body into the sink until done closes, then writes what is still
// queued and returns.
func (t *LedgerTap) write() {
	defer close(t.drained)
	for {
		select {
		case <-t.done:
			for {
				select {
				case item := <-t.queue:
					t.put(item)
				default:
					return
				}
			}
		case item := <-t.queue:
			t.put(item)
		}
	}
}

// put decodes one body into plain bytes and writes it, freeing its share of the budget either
// way; a failed write or an undecodable body costs one log line.
func (t *LedgerTap) put(item tapItem) {
	defer t.queued.Add(-int64(len(item.body)))
	body, ok := decodeBody(item.body, item.encoding)
	if !ok {
		t.log(fmt.Sprintf("ledger tap: body of %s (%s) not kept: cannot decode %s", item.id, item.side, item.encoding))
		return
	}
	if err := t.sink.PutBody(context.Background(), item.id, item.side, body, item.at); err != nil {
		t.log(fmt.Sprintf("ledger tap: body of %s (%s) not kept: %v", item.id, item.side, err))
	}
}

// decodeBody turns a body carried in encoding into plain bytes; ok is false for an encoding the
// router cannot read or bytes that do not decode.
func decodeBody(body []byte, encoding string) (plain []byte, ok bool) {
	if encoding == "" {
		return body, true
	}
	open := streamDecoder(encoding)
	if open == nil {
		return nil, false
	}
	reader, err := open(bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	defer reader.Close()
	plain, err = io.ReadAll(reader)
	if err != nil {
		return nil, false
	}
	return plain, true
}

// Close stops new offers, waits for the writer to write what is queued, and answers ctx.Err()
// when ctx ends first.
func (t *LedgerTap) Close(ctx context.Context) error {
	t.closeOnce.Do(func() { close(t.done) })
	select {
	case <-t.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// tapBuffer collects one streamed answer's chunks for the tap, up to limit bytes.
type tapBuffer struct {
	limit int64
	buf   []byte
	over  bool
}

func newTapBuffer(limit int64) *tapBuffer {
	return &tapBuffer{limit: limit}
}

// push appends a copy of one chunk; past the limit it frees what it held and keeps nothing
// more.
func (b *tapBuffer) push(chunk []byte) {
	if b.over {
		return
	}
	if int64(len(b.buf))+int64(len(chunk)) > b.limit {
		b.over, b.buf = true, nil
		return
	}
	b.buf = append(b.buf, chunk...)
}

// bytes answers the collected body and whether it is whole.
func (b *tapBuffer) bytes() ([]byte, bool) {
	return b.buf, !b.over
}
