package stealth

import (
	"context"
	"math/rand"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"

	"linkedin-automation-tool/config"
)

// TypeHuman simulates natural typing: variable delays, slight rhythm changes,
// and occasional single-character typos that get corrected.
func TypeHuman(el *rod.Element, text string, cfg config.TimingConfig) error {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	typed := el.Context(ctx)

	if err := typed.ScrollIntoView(); err != nil {
		return err
	}
	if err := typed.Click("left", 1); err != nil {
		return err
	}

	kb := typed.Page().Keyboard

	for _, ch := range text {
		// Small chance to introduce a typo then fix it, to avoid robotic cadence.
		if r.Float64() < 0.05 {
			wrong := randomNearbyRune(r, ch)
			if err := kb.Type(input.Key(wrong)); err != nil {
				return err
			}
			time.Sleep(RandomDelay(max(25, cfg.MinDelayMs/5), max(60, cfg.MaxDelayMs/5)))
			if err := kb.Press(input.Backspace); err != nil {
				return err
			}
			time.Sleep(RandomDelay(max(25, cfg.MinDelayMs/5), max(60, cfg.MaxDelayMs/5)))
		}

		if err := kb.Type(input.Key(ch)); err != nil {
			return err
		}

		// Variable delay per key to avoid constant speed.
		time.Sleep(RandomDelay(max(35, cfg.MinDelayMs/4), max(95, cfg.MaxDelayMs/4)))

		// Subtle rhythm change after some characters.
		if r.Float64() < 0.12 {
			ShortPause(cfg)
		}
	}
	return nil
}

// Example integration (auth/login.go):
//   MoveToElementHuman(page, emailEl, cfg.Timing)
//   TypeHuman(emailEl, email, cfg.Timing)
//   MoveToElementHuman(page, passwordEl, cfg.Timing)
//   TypeHuman(passwordEl, password, cfg.Timing)

func randomNearbyRune(r *rand.Rand, ch rune) rune {
	neighbors := []rune{'a', 's', 'd', 'f', 'j', 'k', 'l', 'e', 'i', 'o'}
	if ch >= 'a' && ch <= 'z' && len(neighbors) > 0 {
		return neighbors[r.Intn(len(neighbors))]
	}
	return 'x'
}
