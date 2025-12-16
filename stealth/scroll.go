package stealth

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"

	"linkedin-automation-tool/config"
)

// ScrollFeedHuman scrolls through the LinkedIn feed in a human-like manner.
// It performs multiple scroll actions with variable distances and pauses.
func ScrollFeedHuman(page *rod.Page, cfg config.TimingConfig, duration time.Duration) error {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	
	startTime := time.Now()
	scrollCount := 0
	successfulScrolls := 0
	errorCount := 0
	
	for time.Since(startTime) < duration {
		// Variable scroll distance (200-800 pixels)
		scrollDistance := 200 + r.Intn(600)
		
		// Scroll down - use a timeout to avoid hanging
		err := page.Timeout(5 * time.Second).Mouse.Scroll(0, float64(scrollDistance), 1)
		scrollCount++
		
		if err != nil {
			errorCount++
			// Log first few errors for debugging
			if errorCount <= 3 {
				fmt.Printf("Scroll attempt %d failed: %v\n", scrollCount, err)
			}
			time.Sleep(500 * time.Millisecond)
			continue
		}
		
		successfulScrolls++
		
		// Log successful scroll every 5 scrolls
		if successfulScrolls%5 == 0 {
			fmt.Printf("Scrolled successfully %d times (total attempts: %d)\n", successfulScrolls, scrollCount)
		}
		
		// Human-like pause between scrolls (1-4 seconds)
		pauseMin := max(1000, cfg.MinDelayMs*2)
		pauseMax := max(4000, cfg.MaxDelayMs*2)
		time.Sleep(RandomDelay(pauseMin, pauseMax))
		
		// Occasionally scroll back up slightly (mimics reading)
		if r.Float64() < 0.25 && scrollCount > 2 {
			smallScrollBack := 50 + r.Intn(150)
			_ = page.Timeout(5 * time.Second).Mouse.Scroll(0, float64(-smallScrollBack), 1)
			time.Sleep(RandomDelay(300, 800))
		}
		
		// Occasionally pause longer (mimics reading a post)
		if r.Float64() < 0.3 {
			time.Sleep(RandomDelay(2000, 5000))
		}
	}
	
	fmt.Printf("Scroll complete: %d successful out of %d attempts\n", successfulScrolls, scrollCount)
	
	// If we got at least some successful scrolls, consider it a success
	if successfulScrolls > 0 {
		return nil
	}
	
	return fmt.Errorf("no successful scrolls out of %d attempts (errors: %d)", scrollCount, errorCount)
}

// ScrollToElement scrolls an element into view in a human-like way.
func ScrollToElement(page *rod.Page, el *rod.Element, cfg config.TimingConfig) error {
	// Get current scroll position
	currentScroll, err := page.Eval(`() => window.scrollY`)
	if err != nil {
		return fmt.Errorf("get scroll position: %w", err)
	}
	
	// Scroll element into view
	if err := el.ScrollIntoView(); err != nil {
		return fmt.Errorf("scroll into view: %w", err)
	}
	
	// Add small random pause
	ShortPause(cfg)
	
	// Get new scroll position
	newScroll, err := page.Eval(`() => window.scrollY`)
	if err == nil && newScroll.Value.Num() != currentScroll.Value.Num() {
		// Add small delay to mimic human reading after scroll
		time.Sleep(RandomDelay(max(400, cfg.MinDelayMs), max(1200, cfg.MaxDelayMs)))
	}
	
	return nil
}

// SmoothScrollDown performs a smooth scroll down animation (more human-like than instant scroll).
func SmoothScrollDown(page *rod.Page, distance int, cfg config.TimingConfig) error {
	// Break scroll into smaller chunks for smoothness
	steps := 8 + rand.Intn(5) // 8-12 steps
	stepDistance := distance / steps
	
	for i := 0; i < steps; i++ {
		if err := page.Mouse.Scroll(0, float64(stepDistance), 1); err != nil {
			return err
		}
		// Very short delay between micro-scrolls (20-50ms)
		time.Sleep(time.Duration(20+rand.Intn(30)) * time.Millisecond)
	}
	
	return nil
}

// ScrollWithKeyboard uses Page Down or arrow keys to scroll (alternative method).
func ScrollWithKeyboard(page *rod.Page, scrolls int, cfg config.TimingConfig) error {
	kb := page.Keyboard
	
	for i := 0; i < scrolls; i++ {
		// Random choice between Space, PageDown, or Arrow Down
		choice := rand.Intn(3)
		
		switch choice {
		case 0:
			if err := kb.Press(input.Space); err != nil {
				return err
			}
		case 1:
			if err := kb.Press(input.PageDown); err != nil {
				return err
			}
		case 2:
			// Press Arrow Down multiple times
			for j := 0; j < 3+rand.Intn(3); j++ {
				if err := kb.Press(input.ArrowDown); err != nil {
					return err
				}
				time.Sleep(time.Duration(50+rand.Intn(100)) * time.Millisecond)
			}
		}
		
		// Pause between scroll actions
		time.Sleep(RandomDelay(max(800, cfg.MinDelayMs), max(3000, cfg.MaxDelayMs)))
	}
	
	return nil
}
