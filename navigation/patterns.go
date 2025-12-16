package navigation

import (
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"go.uber.org/zap"

	"linkedin-automation-tool/config"
)

// NavigationPattern represents a predefined navigation workflow
type NavigationPattern struct {
	Name        string
	Tabs        []TabWithAction
	Description string
}

// TabWithAction combines a tab with an action to perform on it
type TabWithAction struct {
	Tab          LinkedInTab
	ScrollTime   time.Duration // How long to scroll (0 = no scroll)
	PauseAfter   time.Duration // Pause after this tab (0 = use default)
	CustomAction func(*rod.Page, config.TimingConfig, *zap.SugaredLogger) error
}

// Predefined navigation patterns
var (
	// QuickTourPattern - Quick tour through main tabs
	QuickTourPattern = NavigationPattern{
		Name:        "Quick Tour",
		Description: "Quickly visit all main LinkedIn tabs",
		Tabs: []TabWithAction{
			{Tab: TabFeed, ScrollTime: 10 * time.Second},
			{Tab: TabMyNetwork, ScrollTime: 5 * time.Second},
			{Tab: TabJobs, ScrollTime: 5 * time.Second},
			{Tab: TabMessaging, ScrollTime: 3 * time.Second},
			{Tab: TabNotifications, ScrollTime: 3 * time.Second},
			{Tab: TabFeed, ScrollTime: 5 * time.Second},
		},
	}

	// NetworkingPattern - Focus on networking activities
	NetworkingPattern = NavigationPattern{
		Name:        "Networking Focus",
		Description: "Focus on networking and connections",
		Tabs: []TabWithAction{
			{Tab: TabFeed, ScrollTime: 15 * time.Second},
			{Tab: TabMyNetwork, ScrollTime: 20 * time.Second},
			{Tab: TabMessaging, ScrollTime: 10 * time.Second},
			{Tab: TabFeed, ScrollTime: 10 * time.Second},
		},
	}

	// JobSearchPattern - Focus on job searching
	JobSearchPattern = NavigationPattern{
		Name:        "Job Search Focus",
		Description: "Focus on job search and applications",
		Tabs: []TabWithAction{
			{Tab: TabFeed, ScrollTime: 10 * time.Second},
			{Tab: TabJobs, ScrollTime: 30 * time.Second},
			{Tab: TabMyNetwork, ScrollTime: 10 * time.Second},
			{Tab: TabJobs, ScrollTime: 20 * time.Second},
			{Tab: TabFeed, ScrollTime: 5 * time.Second},
		},
	}

	// CasualBrowsingPattern - Mimic casual browsing
	CasualBrowsingPattern = NavigationPattern{
		Name:        "Casual Browsing",
		Description: "Casual browsing with more time on feed",
		Tabs: []TabWithAction{
			{Tab: TabFeed, ScrollTime: 30 * time.Second},
			{Tab: TabNotifications, ScrollTime: 5 * time.Second},
			{Tab: TabFeed, ScrollTime: 20 * time.Second},
			{Tab: TabMyNetwork, ScrollTime: 10 * time.Second},
			{Tab: TabFeed, ScrollTime: 15 * time.Second},
		},
	}
)

// ExecutePattern executes a predefined navigation pattern
func ExecutePattern(page *rod.Page, pattern NavigationPattern, cfg config.TimingConfig, log *zap.SugaredLogger) error {
	log.Infow("executing navigation pattern", "pattern", pattern.Name, "description", pattern.Description)

	successCount := 0
	failCount := 0

	for i, tabAction := range pattern.Tabs {
		log.Infow("executing tab action", "step", i+1, "total", len(pattern.Tabs), "tab", string(tabAction.Tab))

		// Navigate to tab
		log.Infow("attempting navigation", "tab", string(tabAction.Tab))
		if err := NavigateToTab(page, tabAction.Tab, cfg, log); err != nil {
			log.Warnw("navigation failed, continuing with next tab", "tab", string(tabAction.Tab), "error", err)
			failCount++
			// Continue to next tab instead of failing entirely
			continue
		}
		successCount++
		log.Infow("navigation successful", "tab", string(tabAction.Tab))

		// Execute custom action if provided
		if tabAction.CustomAction != nil {
			log.Infow("executing custom action", "tab", string(tabAction.Tab))
			if err := tabAction.CustomAction(page, cfg, log); err != nil {
				log.Warnw("custom action failed", "tab", string(tabAction.Tab), "error", err)
			}
		}

		// Scroll if specified
		if tabAction.ScrollTime > 0 {
			log.Infow("scrolling on tab", "tab", string(tabAction.Tab), "duration", tabAction.ScrollTime)
			if err := ScrollCurrentTab(page, tabAction.ScrollTime, cfg, log); err != nil {
				log.Warnw("scroll failed", "tab", string(tabAction.Tab), "error", err)
			} else {
				log.Infow("scroll completed", "tab", string(tabAction.Tab))
			}
		}

		// Pause after (if specified, otherwise use default)
		if i < len(pattern.Tabs)-1 {
			pauseDuration := tabAction.PauseAfter
			if pauseDuration == 0 {
				// Default pause
				pauseDuration = RandomDelay(
					max(2000, cfg.MinDelayMs*2),
					max(5000, cfg.MaxDelayMs*2),
				)
			}
			log.Infow("pausing before next action", "duration", pauseDuration, "nextTab", string(pattern.Tabs[i+1].Tab))
			time.Sleep(pauseDuration)
			log.Infow("pause completed, moving to next tab")
		}
	}

	log.Infow("navigation pattern completed", 
		"pattern", pattern.Name, 
		"successful", successCount, 
		"failed", failCount,
		"total", len(pattern.Tabs))

	// Only return error if all navigations failed
	if successCount == 0 && failCount > 0 {
		return fmt.Errorf("all navigation attempts failed")
	}

	return nil
}

// RandomDelay is a helper function
func RandomDelay(minMs, maxMs int) time.Duration {
	if minMs < 0 {
		minMs = 0
	}
	if maxMs < minMs {
		maxMs = minMs
	}
	// Use simple time-based randomness
	return time.Duration(minMs+int(time.Now().UnixNano()%(int64(maxMs-minMs+1)))) * time.Millisecond
}

// NavigateAndInteract is a helper for simple tab navigation with interaction
func NavigateAndInteract(page *rod.Page, tab LinkedInTab, scrollDuration time.Duration, cfg config.TimingConfig, log *zap.SugaredLogger) error {
	if err := NavigateToTab(page, tab, cfg, log); err != nil {
		return err
	}

	if scrollDuration > 0 {
		return ScrollCurrentTab(page, scrollDuration, cfg, log)
	}

	return nil
}
