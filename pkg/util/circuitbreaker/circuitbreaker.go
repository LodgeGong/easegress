package circuitbreaker

import (
	"fmt"
	"sync"
	"time"
)

var (
	// for unit testing cases to mock 'time.Now' only
	nowFunc = time.Now

	// ErrRejected is returned by 'Execute' if the function call is
	// rejected by the CircuitBreaker
	ErrRejected = fmt.Errorf("call rejected")
)

// sliding window types
const (
	CountBased = iota
	TimeBased
)

type (
	// CallResult is the result (success/failure/slow) of a call
	CallResult uint8

	// Window defines the interface of a window
	Window interface {
		Total() uint32
		Reset()
		Push(result CallResult)
		FailureRate() uint8
		SlowRate() uint8
	}

	// CountBasedWindow defines the count based window
	CountBasedWindow struct {
		total     uint32
		slow      uint32
		failure   uint32
		bucketIdx int
		bucket    []CallResult
	}

	timeBasedWindowBucket struct {
		total   uint32
		slow    uint32
		failure uint32
	}

	// TimeBasedWindow defines the time based window
	TimeBasedWindow struct {
		total       uint32
		slow        uint32
		failure     uint32
		beginAt     time.Time
		firstBucket int
		bucket      []timeBasedWindowBucket
	}
)

// call results
const (
	CallResultUnknown CallResult = iota
	CallResultSuccess
	CallResultSlow
	CallResultFailure
)

// NewCountBasedWindow creates a new count based window with `size` buckets
func NewCountBasedWindow(size uint32) *CountBasedWindow {
	cbw := &CountBasedWindow{
		bucket: make([]CallResult, size),
	}
	return cbw
}

// Reset resets the count based window to initial state
func (cbw *CountBasedWindow) Reset() {
	size := len(cbw.bucket)
	*cbw = CountBasedWindow{
		bucket: make([]CallResult, size),
	}
}

// Total returns the total number of results currently recorded
func (cbw *CountBasedWindow) Total() uint32 {
	return cbw.total
}

// Push pushes a new result into the window and may evict existing
// results if needed
func (cbw *CountBasedWindow) Push(result CallResult) {
	// evict existing bucket, note bucket default value is 'CallResultUnknown',
	// so evict does not happen when there are free buckets.
	switch cbw.bucket[cbw.bucketIdx] {
	case CallResultSuccess:
		cbw.total--
	case CallResultSlow:
		cbw.slow--
		cbw.total--
	case CallResultFailure:
		cbw.failure--
		cbw.total--
	}

	cbw.total++
	switch result {
	case CallResultSlow:
		cbw.slow++
	case CallResultFailure:
		cbw.failure++
	}

	cbw.bucket[cbw.bucketIdx] = result
	cbw.bucketIdx++
	if cbw.bucketIdx >= len(cbw.bucket) {
		cbw.bucketIdx = 0
	}
}

// FailureRate returns the failure rate of recorded results
func (cbw *CountBasedWindow) FailureRate() uint8 {
	return uint8(cbw.failure * 100 / cbw.total)
}

// SlowRate returns the slow rate of recorded results
func (cbw *CountBasedWindow) SlowRate() uint8 {
	return uint8(cbw.slow * 100 / cbw.total)
}

// NewTimeBasedWindow creates a new time based window with `size` buckets
func NewTimeBasedWindow(size uint32) *TimeBasedWindow {
	tbw := &TimeBasedWindow{
		bucket:  make([]timeBasedWindowBucket, size),
		beginAt: nowFunc().Truncate(time.Second),
	}
	return tbw
}

// Reset resets the time based window to initial state
func (tbw *TimeBasedWindow) Reset() {
	size := len(tbw.bucket)
	*tbw = TimeBasedWindow{
		bucket:  make([]timeBasedWindowBucket, size),
		beginAt: nowFunc().Truncate(time.Second),
	}
}

// Total returns the total number of results currently recorded
func (tbw *TimeBasedWindow) Total() uint32 {
	return tbw.total
}

func (tbw *TimeBasedWindow) evict(now time.Time) {
	// check how many seconds has passed since the beginning of the window
	seconds := int(now.Sub(tbw.beginAt) / time.Second)

	// no bucket need to be evicted if seconds is less than the window size
	if seconds < len(tbw.bucket) {
		return
	}

	// evicts is how many buckets need to be evicted
	evicts := seconds - len(tbw.bucket) + 1

	// the begin time of the window need to be adjusted according to evicts
	tbw.beginAt = tbw.beginAt.Add(time.Duration(evicts) * time.Second)

	// evicts may be very large, but at most len(tbw.bucket) buckets need to
	// be evicted
	if evicts > len(tbw.bucket) {
		evicts = len(tbw.bucket)
	}

	// evict all of them
	for i := 0; i < evicts; i++ {
		// get the bucket
		b := &tbw.bucket[tbw.firstBucket]

		// deduct from window
		tbw.total -= b.total
		tbw.slow -= b.slow
		tbw.failure -= b.failure

		// reset bucket to zero
		*b = timeBasedWindowBucket{}

		// adjust the index of first bucket
		tbw.firstBucket = (tbw.firstBucket + 1) % len(tbw.bucket)
	}
}

// Push pushes a new result into the window and may evict existing
// results if needed
func (tbw *TimeBasedWindow) Push(result CallResult) {
	now := nowFunc()

	tbw.evict(now)

	idx := tbw.firstBucket
	idx += int(now.Sub(tbw.beginAt) / time.Second)
	idx %= len(tbw.bucket)
	bucket := &tbw.bucket[idx]

	tbw.total++
	bucket.total++

	if result == CallResultSlow {
		tbw.slow++
		bucket.slow++
	} else if result == CallResultFailure {
		tbw.failure++
		bucket.failure++
	}
}

// FailureRate returns the failure rate of recorded results
func (tbw *TimeBasedWindow) FailureRate() uint8 {
	return uint8(tbw.failure * 100 / tbw.total)
}

// SlowRate returns the slow rate of recorded results
func (tbw *TimeBasedWindow) SlowRate() uint8 {
	return uint8(tbw.slow * 100 / tbw.total)
}

type (
	// State is circuit breaker state
	State uint8

	// Policy defines the policy of a circuit breaker
	Policy struct {
		FailureRateThreshold             uint8
		SlowCallRateThreshold            uint8
		SlidingWindowType                uint8
		SlidingWindowSize                uint32
		PermittedNumberOfCallsInHalfOpen uint32
		MinimumNumberOfCalls             uint32
		SlowCallDurationThreshold        time.Duration
		MaxWaitDurationInHalfOpen        time.Duration
		WaitDurationInOpen               time.Duration
	}

	// StateListenerFunc is a listener function to listen state transit event
	StateListenerFunc func(oldState, newState State)

	// CircuitBreaker defines a circuit breaker
	CircuitBreaker struct {
		lock                    sync.Mutex
		policy                  *Policy
		state                   State
		transitTime             time.Time
		window                  Window
		numberOfCallsInHalfOpen uint32
		// stateID is the id of current state, it increases every time
		// the state changes. the id is returned by AcquirePermission
		// and must be passed back to RecordResult which will then use
		// it to detect wether state changed or not, and if changed, the
		// result is discarded as it does not belong to current state.
		stateID  uint32
		listener StateListenerFunc
	}
)

// circuit breaker states
const (
	StateDisabled State = iota
	StateClosed
	StateHalfOpen
	StateOpen
	StateForceOpen
)

// NewPolicy create and initialize a policy with default configuration
func NewPolicy() *Policy {
	return &Policy{
		FailureRateThreshold:             50,
		SlowCallRateThreshold:            100,
		SlidingWindowType:                CountBased,
		SlidingWindowSize:                100,
		PermittedNumberOfCallsInHalfOpen: 10,
		MinimumNumberOfCalls:             100,
		SlowCallDurationThreshold:        time.Minute,
		MaxWaitDurationInHalfOpen:        0,
		WaitDurationInOpen:               time.Minute,
	}
}

// New creates a circuit breaker based on `policy`,
func New(policy *Policy) *CircuitBreaker {
	cb := &CircuitBreaker{policy: policy}
	cb.transitTo(StateClosed)
	return cb
}

// SetState sets the state of the circuit breaker to `state`
func (cb *CircuitBreaker) SetState(state State) {
	cb.lock.Lock()
	defer cb.lock.Unlock()
	cb.transitTo(state)
}

// SetStateListener sets a state listener for the CircuitBreaker
func (cb *CircuitBreaker) SetStateListener(listener StateListenerFunc) {
	cb.lock.Lock()
	defer cb.lock.Unlock()
	cb.listener = listener
}

// transitTo sets the state of the CircuitBreaker to `state`
func (cb *CircuitBreaker) transitTo(state State) {
	oldState := cb.state
	if state == oldState {
		return
	}

	cb.state = state
	cb.transitTime = nowFunc()
	cb.stateID++

	if state == StateClosed {
		// recreate the window to remove all existing results to avoid jitter
		if cb.policy.SlidingWindowType == CountBased {
			cb.window = NewCountBasedWindow(cb.policy.SlidingWindowSize)
		} else {
			cb.window = NewTimeBasedWindow(cb.policy.SlidingWindowSize)
		}
	} else if state == StateHalfOpen {
		// always use count based window in half open state to avoid results being evicted
		cb.window = NewCountBasedWindow(cb.policy.PermittedNumberOfCallsInHalfOpen)
		cb.numberOfCallsInHalfOpen = 0
	}

	if cb.listener != nil {
		// create a new goroutine as current function is called inside a lock, and we don't
		// know how much time the listener function will take
		go cb.listener(oldState, state)
	}
}

// State returns the state of the circuit breaker
func (cb *CircuitBreaker) State() State {
	return cb.state
}

// AcquirePermission acquires a permission from the circuit breaker
// returns true & stateID if the request is permitted
// returns false & stateID if the request is rejected
func (cb *CircuitBreaker) AcquirePermission() (bool, uint32) {
	cb.lock.Lock()
	defer cb.lock.Unlock()

	// always return true when disabled
	if cb.state == StateDisabled {
		return true, cb.stateID
	}

	// always return false when force open
	if cb.state == StateForceOpen {
		return false, cb.stateID
	}

	// always return true when closed.
	// for time based window, failure rate or slow rate may change as time elapse,
	// that's even no new result were recorded, state may transit from closed to
	// open if may sucess results are evicted by time. but we just rely on the
	// state here and leave state transition to RecordResult to keep code simple.
	if cb.state == StateClosed {
		return true, cb.stateID
	}

	// when state is open, return false if open duration is less than
	// WaitDurationInOpenState. transit to half open otherwise
	if cb.state == StateOpen {
		if nowFunc().Sub(cb.transitTime) < cb.policy.WaitDurationInOpen {
			return false, cb.stateID
		}
		cb.transitTo(StateHalfOpen)
	}

	// circuit breaker is in half open state
	if cb.numberOfCallsInHalfOpen < cb.policy.PermittedNumberOfCallsInHalfOpen {
		cb.numberOfCallsInHalfOpen++
		return true, cb.stateID
	}

	// if state is still half open after MaxWaitDurationInHalfOpenState, transit
	// back to open. note, to avoid switch to open without permit enough calls,
	// we need to do this after the check of numberOfCallsInHalfOpen.
	if cb.policy.MaxWaitDurationInHalfOpen > 0 &&
		nowFunc().Sub(cb.transitTime) > cb.policy.MaxWaitDurationInHalfOpen {
		cb.transitTo(StateOpen)
	}

	return false, cb.stateID
}

// RecordResult records the result in window
func (cb *CircuitBreaker) RecordResult(stateID uint32, err error, d time.Duration) {
	// calculate call result
	result := CallResultSuccess
	if err != nil {
		result = CallResultFailure
	} else if d >= cb.policy.SlowCallDurationThreshold {
		result = CallResultSlow
	}

	cb.lock.Lock()
	defer cb.lock.Unlock()

	// the result does not belong to current state and should be discarded
	if stateID != cb.stateID {
		return
	}

	// as the CircuitBreaker only permit calls in Closed & HalfOpen state,
	// after the stateID check, state can only be Closed & HalfOpen now.

	cb.window.Push(result)

	// check if enough results were collected
	minNumOfCalls := cb.policy.MinimumNumberOfCalls
	if cb.state == StateHalfOpen {
		if minNumOfCalls > cb.policy.PermittedNumberOfCallsInHalfOpen {
			minNumOfCalls = cb.policy.PermittedNumberOfCallsInHalfOpen
		}
	}
	if cb.window.Total() < minNumOfCalls {
		return
	}

	// for count based window, state doesn't transit if result is success
	// but for time based window, state may transit to open even if result
	// is success as existing success results may be evicted by time.
	// note half open state always use a count based window.
	if cb.window.FailureRate() >= cb.policy.FailureRateThreshold {
		cb.transitTo(StateOpen)
	} else if cb.window.SlowRate() >= cb.policy.SlowCallRateThreshold {
		cb.transitTo(StateOpen)
	} else if cb.state == StateHalfOpen {
		cb.transitTo(StateClosed)
	}
}

// Execute executes the given function if the CircuitBreaker accepts it and
// returns the result of the function, and ErrRejected is returned if the
// CircuitBreaker rejects the request.
// If a panic occurs in the function, CircuitBreaker regards it as an error
// and causes the same panic again.
func (cb *CircuitBreaker) Execute(fn func() (interface{}, error)) (interface{}, error) {
	permitted, stateID := cb.AcquirePermission()
	if !permitted {
		return nil, ErrRejected
	}

	start := nowFunc()

	defer func() {
		if e := recover(); e != nil {
			d := nowFunc().Sub(start)
			err, ok := e.(error)
			if !ok {
				err = fmt.Errorf("unknown error: %v", e)
			}
			cb.RecordResult(stateID, err, d)
			panic(e)
		}
	}()

	res, e := fn()
	cb.RecordResult(stateID, e, nowFunc().Sub(start))

	return res, e
}
