package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"go.uber.org/zap"

	"linkedin-automation-tool/config"
	"linkedin-automation-tool/stealth"
	"linkedin-automation-tool/storage"
)

var (
	ErrCredentialsMissing = errors.New("missing LINKEDIN_EMAIL or LINKEDIN_PASSWORD")
	ErrInvalidCreds       = errors.New("login failed: invalid credentials")
	ErrCheckpoint         = errors.New("checkpoint detected - manual intervention required")
)

// LoginOrRestoreSession restores cookies if present; otherwise performs a fresh login.
func LoginOrRestoreSession(ctx context.Context, browser *rod.Browser, page *rod.Page, store storage.StateStore, log *zap.SugaredLogger, cfg *config.Config) error {
	// 1) Try restoring session from persisted cookies.
	ok, err := restoreSession(ctx, browser, page, store, log)
	if err != nil {
		if errors.Is(err, ErrCheckpoint) {
			log.Warnw("checkpoint detected during restore", "error", err)
			return err
		}
		log.Warnw("session restore attempt failed", "error", err)
	}
	if ok {
		return nil
	}

	// 2) Perform a fresh login.
	email, password, err := loadCredsFromEnv()
	if err != nil {
		return err
	}

	if err := performLogin(ctx, page, email, password, log, cfg); err != nil {
		return err
	}

	if err := persistSession(ctx, browser, store, log); err != nil {
		log.Warnw("persist session failed", "error", err)
	}
	return nil
}

func loadCredsFromEnv() (string, string, error) {
	email, ok1 := os.LookupEnv("LINKEDIN_EMAIL")
	pass, ok2 := os.LookupEnv("LINKEDIN_PASSWORD")
	if !ok1 || !ok2 || strings.TrimSpace(email) == "" || strings.TrimSpace(pass) == "" {
		return "", "", ErrCredentialsMissing
	}
	return email, pass, nil
}

func performLogin(ctx context.Context, page *rod.Page, email, password string, log *zap.SugaredLogger, cfg *config.Config) error {
	if err := page.Navigate("https://www.linkedin.com/login"); err != nil {
		return fmt.Errorf("navigate login: %w", err)
	}

	// Wait for form controls defensively; selectors documented by LinkedIn's login page.
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	waitPage := page.Context(waitCtx)

	// Email may be prefilled on returning login. If not present, continue with password.
	var emailEl *rod.Element
	if el, err := waitPage.Timeout(10 * time.Second).Element("input#username"); err == nil {
		emailEl = el
	} else {
		log.Infow("email field not present; assuming returning user with remembered email")
	}

	passwordEl, err := waitPage.Element("input#password")
	if err != nil {
		return fmt.Errorf("password field not found: %w", err)
	}
	loginBtn, err := waitPage.Element("button[type='submit']")
	if err != nil {
		return fmt.Errorf("login button not found: %w", err)
	}

	// Human-like approach: move mouse via Bézier curve, type with variable rhythm.
	if emailEl != nil {
		val, _ := emailEl.Attribute("value")
		if val == nil || strings.TrimSpace(*val) == "" {
			if err := stealth.MoveToElementHuman(page, emailEl, cfg.Timing); err != nil {
				return fmt.Errorf("move to email: %w", err)
			}
			if err := stealth.TypeHuman(emailEl, email, cfg.Timing); err != nil {
				return fmt.Errorf("type email: %w", err)
			}
		} else {
			log.Infow("email prefilled; skipping email typing")
		}
	}

	if err := stealth.MoveToElementHuman(page, passwordEl, cfg.Timing); err != nil {
		return fmt.Errorf("move to password: %w", err)
	}
	if err := stealth.TypeHuman(passwordEl, password, cfg.Timing); err != nil {
		return fmt.Errorf("type password: %w", err)
	}

	if err := stealth.MoveToElementHuman(page, loginBtn, cfg.Timing); err != nil {
		return fmt.Errorf("move to login button: %w", err)
	}
	stealth.ShortPause(cfg.Timing)
	if err := loginBtn.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("login submit click: %w", err)
	}

	// Wait for redirect or errors using DOM/state heuristics (LinkedIn often
	// keeps you on the same document). Avoid WaitNavigation; explicitly poll
	// for authenticated markers or checkpoint redirects.
	ok, err := awaitLoginResult(page)
	if err != nil {
		if errors.Is(err, ErrCheckpoint) {
			log.Warnw("checkpoint detected after login", "error", err)
		}
		return err
	}
	if !ok {
		return ErrInvalidCreds
	}

	// Ensure we're at feed after login to leave user in a known state. Use the
	// root page (not the short-lived wait context) to avoid deadline expiry.
	if err := ensureOnFeed(page); err != nil {
		log.Warnw("post-login feed nav failed", "error", err)
	}

	log.Infow("login successful")
	return nil
}

// awaitLoginResult waits for either feed elements (success), error indicators
// (invalid creds), or checkpoint/captcha markers. Returns (success, error).
func awaitLoginResult(page *rod.Page) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p := page.Context(ctx)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	
	// Success indicators (multiple selectors for robustness)
	successSelectors := []string{
		"nav.global-nav",
		"img.global-nav__me-photo",
		"main.scaffold-layout__main",
		"div.feed-shared-update-v2",
	}

	for {
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("timeout waiting for login result")
		case <-ticker.C:
			info, _ := p.Info()
			url := ""
			if info != nil {
				url = info.URL
			}

			lurl := strings.ToLower(url)
			
			// Check for checkpoint/challenge
			if strings.Contains(lurl, "checkpoint") || strings.Contains(lurl, "challenge") || strings.Contains(lurl, "captcha") {
				return false, ErrCheckpoint
			}

			// Check for invalid credentials errors first
			if _, err := p.Timeout(1 * time.Second).ElementR("div", "(Invalid|problem signing in|didn't match|Incorrect email or password)"); err == nil {
				return false, ErrInvalidCreds
			}
			if _, err := p.Timeout(1 * time.Second).Element(".alert.error, #error-for-username, #error-for-password"); err == nil {
				return false, ErrInvalidCreds
			}

			// Success heuristics: leaving /login or finding authenticated elements
			if !strings.Contains(url, "/login") {
				// Left login page, check if we have success indicators
				for _, sel := range successSelectors {
					if _, err := p.Timeout(2 * time.Second).Element(sel); err == nil {
						return true, nil
					}
				}
				// Left login but no success indicators yet, keep waiting
				continue
			}
			
			// Still on login page, check for success indicators (sometimes LinkedIn doesn't redirect immediately)
			for _, sel := range successSelectors {
				if _, err := p.Timeout(2 * time.Second).Element(sel); err == nil {
					return true, nil
				}
			}
		}
	}
}

// ensureOnFeed makes a best-effort navigation to the feed page after login so
// the session is in a known good state for subsequent flows.
func ensureOnFeed(page *rod.Page) error {
	// Check if already on feed
	if info, _ := page.Info(); info != nil && strings.Contains(info.URL, "/feed") {
		// Already on feed, but wait for content to load
		return waitForFeedContent(page)
	}
	
	p := page.Timeout(20 * time.Second)
	if err := p.Navigate("https://www.linkedin.com/feed"); err != nil {
		return fmt.Errorf("navigate to feed: %w", err)
	}
	
	// Wait for page load
	if err := p.WaitLoad(); err != nil {
		return fmt.Errorf("wait for feed page load: %w", err)
	}
	
	// Wait for actual feed content to render (LinkedIn is a SPA)
	return waitForFeedContent(page)
}

// waitForFeedContent waits for LinkedIn feed elements to actually appear in the DOM.
// LinkedIn uses a SPA architecture, so we need to wait beyond just page load.
func waitForFeedContent(page *rod.Page) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p := page.Context(ctx)
	
	// Try multiple selectors as LinkedIn's DOM changes frequently.
	// We check for: feed container, navigation bar, or main content area.
	selectors := []string{
		"main.scaffold-layout__main",           // Main content area
		"div.feed-shared-update-v2",            // Feed post container
		"nav.global-nav",                       // Global navigation
		"div[role='main']",                     // Main role container
		"div.scaffold-finite-scroll__content",  // Feed scroll container
		"aside.scaffold-layout__aside",         // Sidebar (appears on feed)
	}
	
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for feed content: tried selectors but none appeared within 20s")
		case <-ticker.C:
			// Try each selector
			for _, sel := range selectors {
				if _, err := p.Timeout(2 * time.Second).Element(sel); err == nil {
					// Found at least one feed element, consider it successful
					return nil
				}
			}
		}
	}
}
