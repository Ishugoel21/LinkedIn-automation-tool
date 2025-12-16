package stealth

import (
	"math/rand"
	"time"

	"linkedin-automation-tool/config"
)

// RandomDelay returns a duration between the given bounds (ms).
// A seeded rand.Rand keeps randomness deterministic per run.
func RandomDelay(minMs, maxMs int) time.Duration {
	if minMs < 0 {
		minMs = 0
	}
	if maxMs < minMs {
		maxMs = minMs
	}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	n := r.Intn(maxMs-minMs+1) + minMs
	return time.Duration(n) * time.Millisecond
}

// ShortPause simulates a brief micro delay between subtle actions.
func ShortPause(cfg config.TimingConfig) {
	time.Sleep(RandomDelay(max(40, cfg.MinDelayMs/6), max(80, cfg.MinDelayMs/4)))
}

// ThinkPause simulates a longer human hesitation before a decisive action.
func ThinkPause(cfg config.TimingConfig) {
	base := max(cfg.MinDelayMs, 400)
	upper := max(cfg.MaxDelayMs, base+400)
	time.Sleep(RandomDelay(base, upper))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
