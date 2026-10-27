package notify

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	maxSendAttempts = 3
	dispatchBatch   = 100
)

// Source reads notifications from storage.
type Source interface {
	Since(ctx context.Context, after int64, limit int) ([]domain.Notification, int64, error)
}

// Cursor tracks the last delivered sequence.
type Cursor interface {
	Load(ctx context.Context) (int64, bool, error)
	Store(ctx context.Context, seq int64) error
}

// Sink delivers a notification.
type Sink interface {
	Name() string
	Send(ctx context.Context, n domain.Notification) error
}

// Dispatcher delivers notifications from source to sinks.
type Dispatcher struct {
	src      Source
	cur      Cursor
	sinks    []Sink
	attempts map[int64]int
	done     map[int64]map[string]bool
}

// NewDispatcher creates a dispatcher.
func NewDispatcher(src Source, cur Cursor, sinks ...Sink) *Dispatcher {
	return &Dispatcher{
		src:      src,
		cur:      cur,
		sinks:    sinks,
		attempts: map[int64]int{},
		done:     map[int64]map[string]bool{},
	}
}

// Tick delivers one batch.
func (d *Dispatcher) Tick(ctx context.Context) error {
	seq, ok, err := d.cur.Load(ctx)
	if err != nil {
		return err
	}
	if !ok {
		_, last, err := d.src.Since(ctx, -1, 0)
		if err != nil {
			return err
		}
		if err := d.cur.Store(ctx, last); err != nil {
			return err
		}
		return nil
	}
	entries, _, err := d.src.Since(ctx, seq, dispatchBatch)
	if err != nil {
		return err
	}
	for _, n := range entries {
		if d.attempts[n.Seq] >= maxSendAttempts {
			name := ""
			if d.done[n.Seq] == nil {
				d.done[n.Seq] = map[string]bool{}
			}
			for _, s := range d.sinks {
				if !d.done[n.Seq][s.Name()] {
					name = s.Name()
					break
				}
			}
			if name == "" && len(d.sinks) > 0 {
				name = d.sinks[len(d.sinks)-1].Name()
			}
			log.Printf("notify: giving up on seq %d for sink %s", n.Seq, name)
			if err := d.cur.Store(ctx, n.Seq); err != nil {
				return err
			}
			d.clear(n.Seq)
			continue
		}
		if d.done[n.Seq] == nil {
			d.done[n.Seq] = map[string]bool{}
		}
		allDone := true
		for _, s := range d.sinks {
			if d.done[n.Seq][s.Name()] {
				continue
			}
			if err := s.Send(ctx, n); err != nil {
				allDone = false
				d.attempts[n.Seq]++
				break
			}
			d.done[n.Seq][s.Name()] = true
		}
		if !allDone {
			break
		}
		if err := d.cur.Store(ctx, n.Seq); err != nil {
			return err
		}
		d.clear(n.Seq)
	}
	return nil
}

func (d *Dispatcher) clear(seq int64) {
	delete(d.attempts, seq)
	delete(d.done, seq)
}

// Run runs the dispatcher periodically.
func (d *Dispatcher) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.Tick(ctx); err != nil {
				log.Printf("notify: tick: %v", err)
			}
		}
	}
}

// FileCursor stores cursor in a file.
type FileCursor struct{ Path string }

// Load reads the cursor.
func (f FileCursor) Load(ctx context.Context) (int64, bool, error) {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0, false, nil
	}
	seq, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false, err
	}
	return seq, true, nil
}

// Store writes the cursor atomically.
func (f FileCursor) Store(ctx context.Context, seq int64) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(seq, 10)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// StoreSource adapts a store to Source.
type StoreSource struct {
	Store interface {
		NotificationsSince(ctx context.Context, after int64, limit int) ([]domain.Notification, error)
		LastNotificationSeq(ctx context.Context) (int64, error)
	}
}

// Since implements Source.
func (s StoreSource) Since(ctx context.Context, after int64, limit int) ([]domain.Notification, int64, error) {
	if after == -1 && limit == 0 {
		last, err := s.Store.LastNotificationSeq(ctx)
		return nil, last, err
	}
	entries, err := s.Store.NotificationsSince(ctx, after, limit)
	if err != nil {
		return nil, 0, err
	}
	last, err := s.Store.LastNotificationSeq(ctx)
	if err != nil {
		return nil, 0, err
	}
	return entries, last, nil
}

// StoreCursor adapts a store to Cursor.
type StoreCursor struct {
	Name string
	DB   interface {
		NotifyCursor(ctx context.Context, name string) (int64, bool, error)
		SetNotifyCursor(ctx context.Context, name string, seq int64) error
	}
}

// Load implements Cursor.
func (c StoreCursor) Load(ctx context.Context) (int64, bool, error) {
	return c.DB.NotifyCursor(ctx, c.Name)
}

// Store implements Cursor.
func (c StoreCursor) Store(ctx context.Context, seq int64) error {
	return c.DB.SetNotifyCursor(ctx, c.Name, seq)
}
