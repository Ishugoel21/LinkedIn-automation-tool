package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"go.uber.org/zap"

	"linkedin-automation-tool/storage"
)

const sessionKey = "linkedin_session"

// restoreSession attempts to load cookies from the StateStore and validate
// whether the session is still usable by navigating to the feed.
func restoreSession(ctx context.Context, browser *rod.Browser, page *rod.Page, store storage.StateStore, log *zap.SugaredLogger) (bool, error) {
	raw, err := store.Load(ctx, sessionKey)
	if err != nil {
		// Absence is fine; surface other failures.
		if !errors.Is(err, storage.ErrNotFound) {
			log.Warnw("session load failed", "error", err)
		}
		return false, nil
	}

	var cookies []*proto.NetworkCookie
	if err := json.Unmarshal(raw, &cookies); err != nil {
		log.Warnw("session parse failed", "error", err)
		return false, nil
	}

	params := toCookieParams(cookies)
	if err := browser.SetCookies(params); err != nil {
		log.Warnw("session cookie inject failed", "error", err)
		return false, nil
	}

	// Validate by visiting feed; LinkedIn redirects to login if cookies are stale.
	if err := page.Navigate("https://www.linkedin.com/feed"); err != nil {
		return false, fmt.Errorf("navigate feed during restore: %w", err)
	}

	ok, err := waitForFeedOrCheckpoint(page)
	if err != nil {
		return false, err
	}

	if ok {
		log.Infow("session restored")
		return true, nil
	}

	return false, nil
}

// persistSession captures current cookies and saves them for reuse.
func persistSession(ctx context.Context, browser *rod.Browser, store storage.StateStore, log *zap.SugaredLogger) error {
	cookies, err := browser.GetCookies()
	if err != nil {
		return fmt.Errorf("get cookies: %w", err)
	}

	payload, err := json.MarshalIndent(cookies, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cookies: %w", err)
	}

	if err := store.Save(ctx, sessionKey, payload); err != nil {
		return fmt.Errorf("save session: %w", err)
	}

	log.Infow("session persisted")
	return nil
}

func toCookieParams(cs []*proto.NetworkCookie) []*proto.NetworkCookieParam {
	out := make([]*proto.NetworkCookieParam, 0, len(cs))
	for _, c := range cs {
		if c == nil {
			continue
		}
		out = append(out, &proto.NetworkCookieParam{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Expires:  c.Expires,
			HTTPOnly: c.HTTPOnly,
			Secure:   c.Secure,
			SameSite: c.SameSite,
			Priority: c.Priority,
		})
	}
	return out
}

// waitForFeedOrCheckpoint waits briefly to determine whether we landed on feed,
// got bounced to login, or hit a checkpoint page. We avoid aggressive retries
// to respect LinkedIn's security layers.
func waitForFeedOrCheckpoint(page *rod.Page) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p := page.Context(ctx)

	// Wait for page load first
	if err := rod.Try(func() {
		_ = p.WaitLoad()
	}); err != nil {
		return false, fmt.Errorf("wait load failed: %w", err)
	}

	// Poll for authentication indicators
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	
	// Multiple selectors for authenticated state (LinkedIn DOM changes frequently)
	authSelectors := []string{
		"main.scaffold-layout__main",           // Main content area
		"nav.global-nav",                       // Global navigation
		"img.global-nav__me-photo",             // Profile photo in nav
		"button[aria-label*='Me']",             // Me button
		"div.feed-shared-update-v2",            // Feed post
		"aside.scaffold-layout__aside",         // Sidebar
	}

	for {
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("timeout waiting for feed or checkpoint indicators")
		case <-ticker.C:
			info, _ := p.Info()
			url := ""
			if info != nil {
				url = info.URL
			}
			
			// Check for checkpoint/challenge
			if strings.Contains(url, "/checkpoint/") || strings.Contains(url, "captcha") || strings.Contains(url, "/challenge/") {
				return false, ErrCheckpoint
			}

			// Check if on feed page
			if strings.Contains(url, "/feed") {
				// URL says feed, but verify content is actually there
				for _, sel := range authSelectors {
					if _, err := p.Timeout(2 * time.Second).Element(sel); err == nil {
						return true, nil
					}
				}
				// URL is /feed but no content yet, keep waiting
				continue
			}

			// Not on feed URL, check for authenticated elements
			for _, sel := range authSelectors {
				if _, err := p.Timeout(2 * time.Second).Element(sel); err == nil {
					return true, nil
				}
			}
		}
	}
}

