package engineio

import (
	"sync"
	"time"
)

// yeastAlphabet is the 64-character URL-safe alphabet used to encode timestamps,
// mirroring engine.io's yeast: digits, upper- and lower-case letters, then "-"
// and "_". Every character is safe in a URL query value.
const yeastAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-_"

// yeastGenerator produces short, URL-safe, monotonically-unique strings derived
// from the current time, mirroring engine.io's yeast. It is safe for concurrent
// use: mu guards the shared seed and previous-millisecond state.
//
// engine.io's yeast relies on single-threaded JavaScript to guarantee
// uniqueness; under concurrency the millisecond can appear to bounce between
// callers, so this generator additionally clamps the time to be non-decreasing
// and only resets the sequence when the clock strictly advances. That keeps the
// "<time>" / "<time>.<seq>" output shape while guaranteeing every value is
// globally distinct.
type yeastGenerator struct {
	// mu guards seed and previousMillis, which are read and written on every call from
	// any number of concurrent goroutines.
	mu sync.Mutex
	// seed is the sequence number for the current millisecond, reset only when the
	// clock strictly advances past previousMillis.
	seed int64
	// previousMillis is the last millisecond a value was emitted for, never decreased
	// so interleaved callers cannot reuse a sequence number.
	previousMillis int64
}

// defaultYeast is the package-wide generator shared by every polling transport,
// so values stay unique even across distinct transports in the same process.
var defaultYeast = &yeastGenerator{}

// yeastEncode encodes a non-negative integer in the 64-character yeast alphabet,
// most-significant digit first, mirroring engine.io's encode.
func yeastEncode(num int64) string {
	const base = int64(len(yeastAlphabet))

	// encoded accumulates the digits from least- to most-significant, so each new
	// digit is prepended to keep the most-significant digit first.
	var encoded string
	for {
		encoded = string(yeastAlphabet[num%base]) + encoded
		num /= base
		if num == 0 {
			break
		}
	}

	return encoded
}

// next returns the next unique value. It encodes the current Unix-millisecond
// time and, when called more than once within the same millisecond, appends a
// "." plus an encoded sequence number to disambiguate the colliding values. The
// time read happens under the lock so concurrent callers observe a consistent,
// non-decreasing millisecond and can never reuse a sequence number.
func (g *yeastGenerator) next() string {
	g.mu.Lock()
	defer g.mu.Unlock()

	// millis is the current millisecond, clamped to never go below the previous
	// one so an interleaved caller that observed a later millisecond cannot let
	// this call reset the sequence and collide.
	millis := max(time.Now().UnixMilli(), g.previousMillis)

	// A strictly newer millisecond resets the sequence and is returned without a
	// suffix; the same millisecond reuses the running sequence number.
	if millis != g.previousMillis {
		g.previousMillis = millis
		g.seed = 0

		return yeastEncode(millis)
	}

	// A repeated millisecond appends an encoded, post-incremented sequence so each
	// value within the millisecond stays distinct.
	seq := g.seed
	g.seed++

	return yeastEncode(millis) + "." + yeastEncode(seq)
}
