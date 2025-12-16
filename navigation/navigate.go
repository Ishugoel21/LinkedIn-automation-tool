package navigation

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"go.uber.org/zap"

	"linkedin-automation-tool/config"
	"linkedin-automation-tool/stealth"
)

// LinkedInTab represents different LinkedIn sections
type LinkedInTab string

const (
	TabFeed          LinkedInTab = "feed"
	TabMyNetwork     LinkedInTab = "mynetwork"
	TabJobs          LinkedInTab = "jobs"
	TabMessaging     LinkedInTab = "messaging"
	TabNotifications LinkedInTab = "notifications"
	TabMe            LinkedInTab = "me"
	TabSearch        LinkedInTab = "search"
)

var tabURLs = map[LinkedInTab]string{
	TabFeed:          "https://www.linkedin.com/feed",
	TabMyNetwork:     "https://www.linkedin.com/mynetwork",
	TabJobs:          "https://www.linkedin.com/jobs",
	TabMessaging:     "https://www.linkedin.com/messaging",
	TabNotifications: "https://www.linkedin.com/notifications",
	TabMe:            "https://www.linkedin.com/in/me",
}

// Tab navigation selectors (multiple for robustness)
var tabSelectors = map[LinkedInTab][]string{
	TabFeed: {
		"a[href*='/feed']",
		"nav a[data-link-to='feed']",
		"a.global-nav__primary-link[href*='feed']",
	},
	TabMyNetwork: {
		"a[href*='/mynetwork']",
		"nav a[data-link-to='mynetwork']",
		"a.global-nav__primary-link[href*='mynetwork']",
	},
	TabJobs: {
		"a[href*='/jobs']",
		"nav a[data-link-to='jobs']",
		"a.global-nav__primary-link[href*='jobs']",
	},
	TabMessaging: {
		"a[href*='/messaging']",
		"nav a[data-link-to='messaging']",
		"a.global-nav__primary-link[href*='messaging']",
	},
	TabNotifications: {
		"a[href*='/notifications']",
		"button[aria-label*='Notifications']",
		"a.global-nav__primary-link[href*='notifications']",
	},
}

// Content verification selectors for each tab
var tabContentSelectors = map[LinkedInTab][]string{
	TabFeed: {
		"main.scaffold-layout__main",
		"div.feed-shared-update-v2",
		"div.scaffold-finite-scroll",
	},
	TabMyNetwork: {
		"main.scaffold-layout__main",
		"div.mn-community-summary",
		"section.artdeco-card",
	},
	TabJobs: {
		"main.scaffold-layout__main",
		"div.jobs-search-results-list",
		"div.scaffold-layout__list",
	},
	TabMessaging: {
		"main.msg-overlay-list-bubble",
		"div.msg-conversations-container",
		"aside.msg-overlay-list-bubble__convo-list",
	},
	TabNotifications: {
		"main.scaffold-layout__main",
		"div.nt-card__stack",
		"section.artdeco-card",
	},
}

// NavigateToTab navigates to a specific LinkedIn tab with human-like behavior
func NavigateToTab(page *rod.Page, tab LinkedInTab, cfg config.TimingConfig, log *zap.SugaredLogger) error {
	log.Infow("navigating to tab", "tab", string(tab))

	// Check if already on the tab
	if isOnTab(page, tab) {
		log.Infow("already on tab", "tab", string(tab))
		// Still verify content is present
		if err := waitForTabContent(page, tab, log); err != nil {
			log.Warnw("content verification failed on current tab", "tab", string(tab), "error", err)
		}
		return nil
	}

	// Try clicking navigation element first (more human-like)
	if err := clickNavigationTab(page, tab, cfg, log); err != nil {
		log.Warnw("failed to click nav tab, falling back to direct navigation", "tab", string(tab), "error", err)
		// Fallback to direct URL navigation
		if err := navigateToTabURL(page, tab, log); err != nil {
			return fmt.Errorf("both click and direct navigation failed for %s: %w", tab, err)
		}
	}

	// Add extra wait time after navigation to let page fully load
	log.Infow("waiting for page to settle after navigation", "tab", string(tab))
	time.Sleep(3 * time.Second)

	// Wait for content to load - don't fail if this times out
	if err := waitForTabContent(page, tab, log); err != nil {
		log.Warnw("content verification timed out, but continuing", "tab", string(tab), "error", err)
		// Don't return error - navigation may have still succeeded
	}

	// Verify we're actually on the right page
	if !isOnTab(page, tab) {
		log.Warnw("navigation may not have completed - URL doesn't match", "tab", string(tab))
	} else {
		log.Infow("confirmed on correct tab", "tab", string(tab))
	}

	return nil
}

// clickNavigationTab finds and clicks the navigation tab element
func clickNavigationTab(page *rod.Page, tab LinkedInTab, cfg config.TimingConfig, log *zap.SugaredLogger) error {
	selectors, ok := tabSelectors[tab]
	if !ok {
		return fmt.Errorf("no selectors defined for tab: %s", tab)
	}

	// Try each selector with reasonable timeout
	for _, sel := range selectors {
		el, err := page.Timeout(10 * time.Second).Element(sel)
		if err != nil {
			log.Debugw("selector not found, trying next", "selector", sel, "error", err)
			continue
		}

		// Wait for element to be visible
		if err := el.WaitVisible(); err != nil {
			log.Debugw("element not visible, trying next", "selector", sel, "error", err)
			continue
		}

		// Click the element
		if err := el.Click("left", 1); err != nil {
			log.Warnw("click failed", "selector", sel, "error", err)
			continue
		}

		log.Infow("clicked navigation tab", "tab", string(tab), "selector", sel)
		time.Sleep(2 * time.Second) // Wait for navigation to start
		return nil
	}

	return fmt.Errorf("could not find or click any navigation element for tab: %s", tab)
}

// navigateToTabURL directly navigates to the tab URL (fallback method)
func navigateToTabURL(page *rod.Page, tab LinkedInTab, log *zap.SugaredLogger) error {
	url, ok := tabURLs[tab]
	if !ok {
		return fmt.Errorf("no URL defined for tab: %s", tab)
	}

	log.Infow("navigating directly to URL", "tab", string(tab), "url", url)

	// Navigate with timeout
	if err := page.Timeout(30 * time.Second).Navigate(url); err != nil {
		return fmt.Errorf("navigate to %s: %w", url, err)
	}

	// Wait for page load
	if err := page.Timeout(30 * time.Second).WaitLoad(); err != nil {
		return fmt.Errorf("wait for page load: %w", err)
	}

	// Add a small pause after navigation to let page settle
	time.Sleep(3 * time.Second)

	return nil
}

// isOnTab checks if currently on the specified tab
func isOnTab(page *rod.Page, tab LinkedInTab) bool {
	info, err := page.Info()
	if err != nil {
		return false
	}

	url := strings.ToLower(info.URL)
	tabStr := string(tab)

	// Special case for "me" tab (profile)
	if tab == TabMe {
		return strings.Contains(url, "/in/")
	}

	return strings.Contains(url, "/"+tabStr)
}

// waitForTabContent waits for tab-specific content to appear
func waitForTabContent(page *rod.Page, tab LinkedInTab, log *zap.SugaredLogger) error {
	selectors, ok := tabContentSelectors[tab]
	if !ok {
		log.Warnw("no content selectors defined for tab, skipping content verification", "tab", string(tab))
		return nil
	}

	// Use a shorter wait time - 10 seconds is enough
	maxWait := 10 * time.Second
	startTime := time.Now()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		if time.Since(startTime) > maxWait {
			log.Warnw("timeout waiting for content", "tab", string(tab), "timeout", maxWait)
			return fmt.Errorf("timeout waiting for %s content to load", tab)
		}

		// Try each selector with a short timeout
		for _, sel := range selectors {
			if _, err := page.Timeout(2 * time.Second).Element(sel); err == nil {
				log.Infow("tab content loaded", "tab", string(tab), "selector", sel)
				return nil
			}
		}

		<-ticker.C
	}
}

// NavigateSequence navigates through multiple tabs in sequence with pauses
func NavigateSequence(page *rod.Page, tabs []LinkedInTab, cfg config.TimingConfig, log *zap.SugaredLogger) error {
	for i, tab := range tabs {
		log.Infow("navigating to tab in sequence", "index", i+1, "total", len(tabs), "tab", string(tab))

		if err := NavigateToTab(page, tab, cfg, log); err != nil {
			return fmt.Errorf("failed to navigate to %s: %w", tab, err)
		}

		// Pause between navigations (human-like)
		if i < len(tabs)-1 {
			pauseDuration := stealth.RandomDelay(
				max(2000, cfg.MinDelayMs*2),
				max(5000, cfg.MaxDelayMs*2),
			)
			log.Infow("pausing before next navigation", "duration", pauseDuration)
			time.Sleep(pauseDuration)
		}
	}

	return nil
}

// ScrollCurrentTab scrolls the current page/tab
func ScrollCurrentTab(page *rod.Page, duration time.Duration, cfg config.TimingConfig, log *zap.SugaredLogger) error {
	log.Infow("scrolling current tab", "duration", duration)
	return stealth.ScrollFeedHuman(page, cfg, duration)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
