package server

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
)

// Admission defaults, used for every zero field of host.load.
const (
	DefaultMaxInflight        = 64
	DefaultPerCallerPerMinute = 300
	DefaultMaxCaptures        = 4
	// callerBurst is the token bucket's capacity, whatever the rate.
	callerBurst = 60
	// admitWait is how long a request waits for a slot before server_busy.
	admitWait = 50 * time.Millisecond
	// busyRetryMS is the retry_after_ms of a server_busy refusal.
	busyRetryMS = 1000
	// statsWindow is the accounting window in seconds, one ring bucket each.
	statsWindow = 60
	// statsTop is how many methods and callers RPCStats lists.
	statsTop = 10
	// statsKeysPerSecond bounds the distinct names one second records; the
	// rest are counted under statsOverflow, so made-up method names cannot
	// grow the ring.
	statsKeysPerSecond = 256
	statsOverflow      = "(other)"
	// operatorKey is the caller key of a process outside every holder that
	// names no caller.
	operatorKey = "operator"
	// ownerTTL is how long the pid-to-session map of running holders is
	// reused before it is rebuilt.
	ownerTTL = 2 * time.Second
	// admitLogEvery spaces the log lines about one caller, and the in-flight
	// warning.
	admitLogEvery = time.Minute
)

// RPCCount is one method's or caller's share of the window.
type RPCCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	AvgUS int64  `json:"avg_us"`
}

// RPCStats is the last minute of requests, as host.stats returns it under rpc.
type RPCStats struct {
	WindowSeconds int        `json:"window_seconds"`
	Total         int        `json:"total"`
	Inflight      int        `json:"inflight"`
	Waiting       int        `json:"waiting"`
	Rejected      int        `json:"rejected"`
	Limited       int        `json:"limited"`
	SweepSkipped  int        `json:"sweep_skipped"`
	ByMethod      []RPCCount `json:"by_method"`
	ByCaller      []RPCCount `json:"by_caller"`
}

// RetryAfterData is the data of a server_busy or rate_limited refusal.
type RetryAfterData struct {
	RetryAfterMS int `json:"retry_after_ms"`
}

// unlimitedMethods are counted but never limited: hooks keep steering and
// notifications live, and a tool loop must not silence them.
var unlimitedMethods = map[string]bool{
	"session.report_agent_session": true,
	"todo.mirror":                  true,
}

// admission is the gate every request passes: a slot semaphore, a token
// bucket per caller, the capture semaphore and the accounting ring.
type admission struct {
	mu          sync.Mutex
	slots       chan struct{}
	captures    chan struct{}
	maxInflight int
	perMinute   int
	buckets     map[string]*tokenBucket
	prunedAt    time.Time
	ring        [statsWindow]statSecond
	logged      map[string]time.Time
	warnedAt    time.Time
	owners      map[int]string
	ownersAt    time.Time
	// rebuildMu lets one request rebuild a stale owner map while the others
	// wait for it instead of each rebuilding their own.
	rebuildMu sync.Mutex

	inflight atomic.Int64
	waiting  atomic.Int64
}

func newAdmission() *admission {
	a := &admission{buckets: map[string]*tokenBucket{}, logged: map[string]time.Time{}}
	a.configure(config.HostLoadConfig{})
	return a
}

// configure applies host.load; a zero field keeps its default. Slots held
// under the old limits go back to the semaphore they came from.
func (a *admission) configure(c config.HostLoadConfig) {
	maxInflight := orDefault(c.MaxInflight, DefaultMaxInflight)
	perMinute := orDefault(c.PerCallerPerMinute, DefaultPerCallerPerMinute)
	maxCaptures := orDefault(c.MaxCaptures, DefaultMaxCaptures)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.slots == nil || cap(a.slots) != maxInflight {
		a.slots = make(chan struct{}, maxInflight)
	}
	if a.captures == nil || cap(a.captures) != maxCaptures {
		a.captures = make(chan struct{}, maxCaptures)
	}
	if perMinute != a.perMinute {
		a.buckets = map[string]*tokenBucket{}
	}
	a.maxInflight, a.perMinute = maxInflight, perMinute
}

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// admission returns the server's gate, made on first use so a Server built
// without New still has one.
func (s *Server) admission() *admission {
	s.admOnce.Do(func() {
		if s.adm == nil {
			s.adm = newAdmission()
		}
	})
	return s.adm
}

// SetHostLoad applies host.load. Zero fields keep the defaults: 64 in flight,
// 300 per caller per minute with a burst of 60, and 4 captures at once.
func (s *Server) SetHostLoad(c config.HostLoadConfig) { s.admission().configure(c) }

// slot is one request's place in the in-flight semaphore. A request that waits
// for something gives it back meanwhile (pause) and takes one again before it
// works (resume). Only the request's own goroutine uses it.
type slot struct {
	a      *admission
	ch     chan struct{}
	held   bool
	paused bool
	// end finishes the request: the slot goes back and the request is
	// recorded. It runs once.
	end func()
}

// endSetup finishes the admitted request on ctx early, for a stream that
// holds its slot only while it is set up.
func endSetup(ctx context.Context) {
	if sl := slotFrom(ctx); sl != nil && sl.end != nil {
		sl.end()
	}
}

type slotKey struct{}

// slotFrom returns the request's slot, or nil for work no request admitted.
func slotFrom(ctx context.Context) *slot {
	sl, _ := ctx.Value(slotKey{}).(*slot)
	return sl
}

// release gives the slot back; a released slot stays released.
func (sl *slot) release() {
	if sl == nil || !sl.held {
		return
	}
	sl.held = false
	<-sl.ch
	sl.a.inflight.Add(-1)
}

// pause releases the slot while the request waits, counted in Waiting.
func (sl *slot) pause() {
	if sl == nil || !sl.held {
		return
	}
	sl.release()
	sl.paused = true
	sl.a.waiting.Add(1)
}

// resume takes a slot back after pause, waiting as long as ctx allows.
func (sl *slot) resume(ctx context.Context) {
	if sl == nil || !sl.paused {
		return
	}
	sl.paused = false
	sl.a.waiting.Add(-1)
	ch := sl.a.slotChan()
	select {
	case ch <- struct{}{}:
		sl.ch, sl.held = ch, true
		sl.a.inflight.Add(1)
	case <-ctx.Done():
	}
}

// done ends the request's hold, paused or not.
func (sl *slot) done() {
	if sl == nil {
		return
	}
	if sl.paused {
		sl.paused = false
		sl.a.waiting.Add(-1)
	}
	sl.release()
}

func (a *admission) slotChan() chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.slots
}

// acquire takes a slot, waiting at most admitWait.
func (a *admission) acquire(ctx context.Context) (*slot, bool) {
	ch := a.slotChan()
	t := time.NewTimer(admitWait)
	defer t.Stop()
	select {
	case ch <- struct{}{}:
	case <-t.C:
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
	a.inflight.Add(1)
	return &slot{a: a, ch: ch, held: true}, true
}

// captureSlot waits for one of the capture semaphore's places and returns
// its release.
func (a *admission) captureSlot() func() {
	a.mu.Lock()
	ch := a.captures
	a.mu.Unlock()
	ch <- struct{}{}
	return func() { <-ch }
}

// tokenBucket refills perMinute tokens a minute up to callerBurst.
type tokenBucket struct {
	tokens float64
	at     time.Time
}

// bucketIdle is how long a full bucket stays untouched before it is dropped;
// a new one starts full, so dropping it changes nothing for the caller.
const bucketIdle = time.Minute

// refund gives back the token a request spent before it was refused for
// want of a slot, so server_busy and its retry cost one token.
func (a *admission) refund(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if b := a.buckets[key]; b != nil {
		b.tokens = math.Min(callerBurst, b.tokens+1)
	}
}

// pruneBuckets drops buckets that are full and untouched for bucketIdle.
// The caller holds a.mu.
func (a *admission) pruneBuckets(now time.Time, rate float64) {
	if now.Sub(a.prunedAt) < bucketIdle {
		return
	}
	a.prunedAt = now
	for key, b := range a.buckets {
		idle := now.Sub(b.at)
		if idle >= bucketIdle && b.tokens+idle.Seconds()*rate >= callerBurst {
			delete(a.buckets, key)
		}
	}
}

// take spends one token, or returns how long until one is there.
func (a *admission) take(key string, now time.Time) (time.Duration, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rate := float64(a.perMinute) / 60
	a.pruneBuckets(now, rate)
	b := a.buckets[key]
	if b == nil {
		b = &tokenBucket{tokens: callerBurst, at: now}
		a.buckets[key] = b
	}
	if el := now.Sub(b.at).Seconds(); el > 0 {
		b.tokens = math.Min(callerBurst, b.tokens+el*rate)
	}
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	return time.Duration((1 - b.tokens) / rate * float64(time.Second)), false
}

// statSecond is one second of the accounting ring.
type statSecond struct {
	sec      int64
	total    int
	rejected int
	limited  int
	methods  map[string]*statAgg
	callers  map[string]*statAgg
}

type statAgg struct {
	count int
	sumUS int64
}

// outcome is how admission ended a request.
type outcome int

const (
	outcomeServed outcome = iota
	outcomeRejected
	outcomeLimited
)

// record adds one finished request to the ring.
func (a *admission) record(method, caller string, took time.Duration, how outcome, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sec := now.Unix()
	b := &a.ring[sec%statsWindow]
	if b.sec != sec {
		*b = statSecond{sec: sec, methods: map[string]*statAgg{}, callers: map[string]*statAgg{}}
	}
	b.total++
	switch how {
	case outcomeRejected:
		b.rejected++
	case outcomeLimited:
		b.limited++
	}
	us := took.Microseconds()
	addAgg(b.methods, method, us)
	addAgg(b.callers, caller, us)
}

func addAgg(m map[string]*statAgg, name string, us int64) {
	agg := m[name]
	if agg == nil {
		if len(m) >= statsKeysPerSecond {
			name = statsOverflow
			agg = m[name]
		}
		if agg == nil {
			agg = &statAgg{}
			m[name] = agg
		}
	}
	agg.count++
	agg.sumUS += us
}

// snapshot sums the ring's last statsWindow seconds.
func (a *admission) snapshot(now time.Time) RPCStats {
	st := RPCStats{
		WindowSeconds: statsWindow,
		Inflight:      int(a.inflight.Load()),
		Waiting:       int(a.waiting.Load()),
	}
	methods, callers := map[string]*statAgg{}, map[string]*statAgg{}
	oldest := now.Unix() - statsWindow
	a.mu.Lock()
	for i := range a.ring {
		b := &a.ring[i]
		if b.sec <= oldest || b.total == 0 {
			continue
		}
		st.Total += b.total
		st.Rejected += b.rejected
		st.Limited += b.limited
		mergeAgg(methods, b.methods)
		mergeAgg(callers, b.callers)
	}
	a.mu.Unlock()
	st.ByMethod, st.ByCaller = topCounts(methods), topCounts(callers)
	return st
}

func mergeAgg(dst, src map[string]*statAgg) {
	for name, agg := range src {
		d := dst[name]
		if d == nil {
			d = &statAgg{}
			dst[name] = d
		}
		d.count += agg.count
		d.sumUS += agg.sumUS
	}
}

// topCounts is the statsTop names with the most requests, ties by name.
func topCounts(m map[string]*statAgg) []RPCCount {
	out := make([]RPCCount, 0, len(m))
	for name, agg := range m {
		out = append(out, RPCCount{Name: name, Count: agg.count, AvgUS: agg.sumUS / int64(agg.count)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > statsTop {
		out = out[:statsTop]
	}
	return out
}

// RPCStats is the last minute of requests, plus what is in flight now.
func (s *Server) RPCStats() RPCStats {
	st := s.admission().snapshot(time.Now())
	// The sweep guard's counter, when this build has one.
	if sk, ok := any(s).(interface{ SweepSkipped() int }); ok {
		st.SweepSkipped = sk.SweepSkipped()
	}
	return st
}

// admitKey is the caller key of a request: the session whose running holder
// owns the peer pid, whatever the request names. A process outside every
// holder is its named caller, or the operator, and is never limited.
func (s *Server) admitKey(ctx context.Context, req Request) (key string, limited bool) {
	if pid, ok := ctx.Value(peerPIDKey{}).(int); ok {
		lookup := s.sessionOfPID
		if lookup == nil {
			lookup = s.cachedHolderSessionOf
		}
		if id, found := lookup(pid); found {
			return id, true
		}
	}
	if req.Caller != "" {
		return req.Caller, false
	}
	return operatorKey, false
}

// cachedHolderSessionOf is holderSessionOf over a pid-to-session map rebuilt
// at most every ownerTTL, so admission does not read every holder's /proc
// entries per request.
func (s *Server) cachedHolderSessionOf(pid int) (string, bool) {
	a := s.admission()
	owners, fresh := a.ownerMap(time.Now())
	if !fresh {
		a.rebuildMu.Lock()
		owners, fresh = a.ownerMap(time.Now())
		if !fresh {
			owners = s.holderOwners()
			a.mu.Lock()
			a.owners, a.ownersAt = owners, time.Now()
			a.mu.Unlock()
		}
		a.rebuildMu.Unlock()
	}
	return ownerOf(pid, owners, parentPID)
}

// ownerMap returns the cached pid-to-session map and whether it is fresh.
func (a *admission) ownerMap(now time.Time) (map[int]string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.owners, a.owners != nil && now.Sub(a.ownersAt) <= ownerTTL
}

// admit runs the gate for req: the caller's budget, then a slot. It returns
// the context carrying the slot and the func that ends the request, or the
// refusal.
func (s *Server) admit(ctx context.Context, req Request) (context.Context, func(), *Response) {
	a := s.admission()
	start := time.Now()
	key, limited := s.admitKey(ctx, req)
	if limited && !unlimitedMethods[req.Method] {
		if wait, ok := a.take(key, start); !ok {
			a.record(req.Method, key, time.Since(start), outcomeLimited, start)
			a.logRefusal(key, fmt.Sprintf("rpc: caller %s limited: %d/min, method %s", key, a.perMinuteNow(), req.Method), start)
			r := retryResp(req.ID, CodeRateLimited,
				fmt.Sprintf("rate limited: caller %s over budget of %d/min", key, a.perMinuteNow()), wait)
			return ctx, nil, &r
		}
	}
	sl, ok := a.acquire(ctx)
	if !ok {
		if limited && !unlimitedMethods[req.Method] {
			a.refund(key)
		}
		a.record(req.Method, key, time.Since(start), outcomeRejected, start)
		a.logRefusal(key, fmt.Sprintf("rpc: caller %s rejected: %d in flight, method %s", key, a.inflight.Load(), req.Method), start)
		r := retryResp(req.ID, CodeServerBusy, "server busy: too many requests in flight", busyRetryMS*time.Millisecond)
		return ctx, nil, &r
	}
	a.warnInflight(start)
	var once sync.Once
	sl.end = func() {
		once.Do(func() {
			sl.done()
			a.record(req.Method, key, time.Since(start), outcomeServed, time.Now())
		})
	}
	return context.WithValue(ctx, slotKey{}, sl), sl.end, nil
}

func (a *admission) perMinuteNow() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.perMinute
}

func retryResp(id, code, msg string, wait time.Duration) Response {
	ms := max(1, int(math.Ceil(float64(wait)/float64(time.Millisecond))))
	return Response{ID: id, Error: &Error{Code: code, Message: msg, Data: RetryAfterData{RetryAfterMS: ms}}}
}

// logRefusal logs line at most once per caller per admitLogEvery.
func (a *admission) logRefusal(key, line string, now time.Time) {
	a.mu.Lock()
	last, seen := a.logged[key]
	due := !seen || now.Sub(last) >= admitLogEvery
	if due {
		a.logged[key] = now
		for k, at := range a.logged {
			if now.Sub(at) >= admitLogEvery {
				delete(a.logged, k)
			}
		}
	}
	a.mu.Unlock()
	if due {
		log.Print(line)
	}
}

// warnInflight logs once when in flight first passes half the cap, and again
// only after a quiet admitLogEvery.
func (a *admission) warnInflight(now time.Time) {
	n := a.inflight.Load()
	a.mu.Lock()
	over := n*2 > int64(a.maxInflight)
	due := over && now.Sub(a.warnedAt) >= admitLogEvery
	if due {
		a.warnedAt = now
	}
	a.mu.Unlock()
	if !due {
		return
	}
	if load, ok := loadAverage(); ok {
		log.Printf("rpc: %d in flight, host load %.1f", n, load)
		return
	}
	log.Printf("rpc: %d in flight", n)
}

// loadAverage is the one-minute load average from /proc/loadavg.
func loadAverage() (float64, bool) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	return v, err == nil
}
