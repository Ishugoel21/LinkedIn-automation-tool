package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"go.uber.org/zap"

	"linkedin-automation-tool/config"
	"linkedin-automation-tool/stealth"
	"linkedin-automation-tool/storage"
)

// MessageState tracks messaging history and daily limits
type MessageState struct {
	Date            string              `json:"date"`             // YYYY-MM-DD format
	MessagesSentToday int               `json:"messages_sent_today"` // Count for current day
	MessagedProfiles  map[string]MessageRecord `json:"messaged_profiles"` // Profile URL -> record
}

// MessageRecord tracks individual message details
type MessageRecord struct {
	ProfileURL  string `json:"profile_url"`
	Timestamp   string `json:"timestamp"`    // RFC3339 format
	MessageSent string `json:"message_sent"` // Optional: actual message content
	Success     bool   `json:"success"`
}

// FollowUpConfig holds configuration for follow-up messaging
type FollowUpConfig struct {
	MaxPerDay           int    // Maximum messages per day
	MessageTemplate     string // Template with {{name}} and {{context}} variables
	WaitBetweenMessages int    // Milliseconds to wait between messages
	Context             string // Optional context for {{context}} variable
}

const (
	stateKeyMessageState = "message_state"
	maxMessageLength     = 2000 // LinkedIn's approximate character limit for messages
)

// SendFollowUps sends follow-up messages to newly accepted connections
//
// DETECTION STRATEGY:
// Instead of scraping invitation manager (which can be flaky), we:
// 1. Take a list of profiles that we previously sent connection requests to
// 2. Visit each profile individually
// 3. Check for presence of "Message" button (indicates accepted connection)
// 4. Skip if already messaged (state tracking)
// 5. Send follow-up message if eligible
//
// This approach is:
// - More reliable (direct profile check vs. parsing invitation manager)
// - Naturally rate-limited (one profile at a time)
// - Resilient to LinkedIn UI changes (Message button is stable)
//
// IMPORTANT: This function expects a list of profile URLs to check.
// Typically, these would come from your connection request history.
func SendFollowUps(
	ctx context.Context,
	page *rod.Page,
	profiles []string,
	store storage.StateStore,
	cfg FollowUpConfig,
	timingCfg config.TimingConfig,
	log *zap.SugaredLogger,
) error {

	if len(profiles) == 0 {
		log.Info("no profiles provided for follow-up messaging")
		return nil
	}

	log.Infow("starting follow-up messaging campaign",
		"totalProfiles", len(profiles),
		"maxPerDay", cfg.MaxPerDay,
	)

	// Load message state
	state, err := loadMessageState(ctx, store, log)
	if err != nil {
		log.Warnw("failed to load message state, starting fresh", "error", err)
		state = newMessageState()
	}

	// Check if we need to reset daily counter (new day)
	today := time.Now().Format("2006-01-02")
	if state.Date != today {
		log.Infow("new day detected, resetting daily counter", "previousDate", state.Date, "today", today)
		state.Date = today
		state.MessagesSentToday = 0
	}

	// Check if daily limit already reached
	if state.MessagesSentToday >= cfg.MaxPerDay {
		log.Warnw("daily message limit already reached",
			"sent", state.MessagesSentToday,
			"limit", cfg.MaxPerDay,
		)
		return fmt.Errorf("daily message limit reached: %d/%d", state.MessagesSentToday, cfg.MaxPerDay)
	}

	successCount := 0
	skipCount := 0
	notConnectedCount := 0
	errorCount := 0

	for i, profileURL := range profiles {
		// Check daily limit before each attempt
		if state.MessagesSentToday >= cfg.MaxPerDay {
			log.Warnw("daily message limit reached during campaign",
				"sent", state.MessagesSentToday,
				"limit", cfg.MaxPerDay,
				"remaining", len(profiles)-i,
			)
			break
		}

		// Check if already messaged
		if record, exists := state.MessagedProfiles[profileURL]; exists && record.Success {
			log.Debugw("skipping already messaged profile",
				"url", profileURL,
				"messagedAt", record.Timestamp,
			)
			skipCount++
			continue
		}

		log.Infow("checking profile for messaging",
			"index", i+1,
			"total", len(profiles),
			"sentToday", state.MessagesSentToday,
			"limit", cfg.MaxPerDay,
			"url", profileURL,
		)

		// Check if connection was accepted (has Message button)
		isConnected, err := checkIfConnected(ctx, page, profileURL, log)
		if err != nil {
			log.Warnw("failed to check connection status",
				"url", profileURL,
				"error", err,
			)
			errorCount++
			continue
		}

		if !isConnected {
			log.Debugw("profile not yet connected, skipping", "url", profileURL)
			notConnectedCount++
			continue
		}

		// Profile is connected, send follow-up message
		log.Infow("profile is connected, sending follow-up message", "url", profileURL)

		err = sendFollowUpMessage(ctx, page, profileURL, cfg, timingCfg, log)

		if err != nil {
			log.Warnw("failed to send follow-up message",
				"url", profileURL,
				"error", err,
			)
			// Record failed attempt
			state.MessagedProfiles[profileURL] = MessageRecord{
				ProfileURL: profileURL,
				Timestamp:  time.Now().Format(time.RFC3339),
				Success:    false,
			}
			errorCount++
		} else {
			// Success
			state.MessagesSentToday++
			state.MessagedProfiles[profileURL] = MessageRecord{
				ProfileURL:  profileURL,
				Timestamp:   time.Now().Format(time.RFC3339),
				MessageSent: cfg.MessageTemplate, // Store template, not personalized version
				Success:     true,
			}
			successCount++

			log.Infow("✅ follow-up message sent successfully",
				"url", profileURL,
				"sentToday", state.MessagesSentToday,
			)
		}

		// Save state after each attempt
		if err := saveMessageState(ctx, store, state, log); err != nil {
			log.Warnw("failed to save message state", "error", err)
		}

		// Human-like delay between messages (unless this is the last one)
		if i < len(profiles)-1 {
			waitTime := time.Duration(cfg.WaitBetweenMessages) * time.Millisecond
			if waitTime < 10*time.Second {
				waitTime = 10 * time.Second // Minimum 10 seconds between messages
			}

			// Add randomness (2-5 seconds)
			waitTime += stealth.RandomDelay(2000, 5000)

			log.Infow("waiting before next message check", "duration", waitTime)
			time.Sleep(waitTime)
		}
	}

	log.Infow("follow-up messaging campaign completed",
		"successful", successCount,
		"skipped", skipCount,
		"notConnected", notConnectedCount,
		"errors", errorCount,
		"totalSentToday", state.MessagesSentToday,
		"limit", cfg.MaxPerDay,
	)

	return nil
}

// checkIfConnected verifies if a connection request was accepted
//
// DETECTION LOGIC:
// We navigate to the profile and check for the "Message" button.
// If present, the connection was accepted.
// If "Connect" or "Pending" button is present, connection not yet accepted.
//
// This is more reliable than scraping invitation manager because:
// - Direct profile check is definitive
// - Message button is a stable UI element
// - Handles edge cases (withdrawn invites, blocked users, etc.)
func checkIfConnected(
	ctx context.Context,
	page *rod.Page,
	profileURL string,
	log *zap.SugaredLogger,
) (bool, error) {

	// Navigate to profile
	log.Debugw("navigating to profile to check connection status", "url", profileURL)
	if err := page.Timeout(30 * time.Second).Navigate(profileURL); err != nil {
		return false, fmt.Errorf("navigate to profile: %w", err)
	}

	if err := page.Timeout(30 * time.Second).WaitLoad(); err != nil {
		return false, fmt.Errorf("wait for profile load: %w", err)
	}

	// Wait for profile to render
	time.Sleep(3 * time.Second)

	// Check for Message button (indicates accepted connection)
	messageButtonSelectors := []string{
		"button[aria-label*='Message']",
		"a[aria-label*='Message']",
		"button:has-text('Message')",
		"a:has-text('Message')",
		"button.pvs-profile-actions__action:has-text('Message')",
		"div.pvs-profile-actions button:has-text('Message')",
	}

	for _, sel := range messageButtonSelectors {
		elem, err := page.Timeout(2 * time.Second).Element(sel)
		if err != nil {
			continue
		}

		// Verify it's actually a Message button
		text, _ := elem.Text()
		ariaLabel, _ := elem.Attribute("aria-label")

		if strings.Contains(strings.ToLower(text), "message") ||
			(ariaLabel != nil && strings.Contains(strings.ToLower(*ariaLabel), "message")) {
			log.Debugw("found Message button - connection accepted", "selector", sel)
			return true, nil
		}
	}

	// Check for indicators that connection is NOT accepted
	notConnectedSelectors := []string{
		"button:has-text('Connect')",
		"button:has-text('Pending')",
		"button:has-text('Follow')",
	}

	for _, sel := range notConnectedSelectors {
		if _, err := page.Timeout(2 * time.Second).Element(sel); err == nil {
			log.Debugw("connection not yet accepted", "indicator", sel)
			return false, nil
		}
	}

	// If we can't find Message button or other indicators, assume not connected
	log.Debug("no clear connection indicator found, assuming not connected")
	return false, nil
}

// sendFollowUpMessage sends a single follow-up message to a connected profile
//
// MESSAGING FLOW:
// 1. Navigate to profile (already there from checkIfConnected)
// 2. Find and click Message button
// 3. Wait for messaging interface (modal or full page)
// 4. Find message input textarea
// 5. Extract name for personalization
// 6. Type message with human-like behavior
// 7. Click Send button
//
// SAFETY FEATURES:
// - Defensive DOM queries with multiple selectors
// - Graceful handling of restricted messaging
// - Human-like pauses and typing
// - State persistence to prevent duplicates
func sendFollowUpMessage(
	ctx context.Context,
	page *rod.Page,
	profileURL string,
	cfg FollowUpConfig,
	timingCfg config.TimingConfig,
	log *zap.SugaredLogger,
) error {

	// Profile should already be loaded from checkIfConnected
	// But ensure we're on the right page
	currentURL := page.MustInfo().URL
	if !strings.Contains(currentURL, profileURL) {
		log.Debug("navigating to profile for messaging")
		if err := page.Timeout(30 * time.Second).Navigate(profileURL); err != nil {
			return fmt.Errorf("navigate to profile: %w", err)
		}
		if err := page.Timeout(30 * time.Second).WaitLoad(); err != nil {
			return fmt.Errorf("wait for profile load: %w", err)
		}
		time.Sleep(2 * time.Second)
	}

	// Find Message button
	messageBtn, err := findMessageButton(page, log)
	if err != nil {
		return fmt.Errorf("find message button: %w", err)
	}

	// Human-like mouse movement to button
	log.Debug("moving mouse to Message button")
	if err := stealth.MoveToElementHuman(page, messageBtn, timingCfg); err != nil {
		log.Warnw("mouse movement to button failed, clicking directly", "error", err)
	}

	// Small hover pause (human thinking)
	time.Sleep(stealth.RandomDelay(500, 1200))

	// Click Message button
	log.Info("clicking Message button")
	if err := messageBtn.Click("left", 1); err != nil {
		return fmt.Errorf("click message button: %w", err)
	}

	// Wait for messaging interface to load
	time.Sleep(3 * time.Second)

	// Find message input textarea
	textarea, err := findMessageInput(page, log)
	if err != nil {
		return fmt.Errorf("find message input: %w", err)
	}

	// Extract first name from profile for personalization
	firstName := extractFirstNameFromProfile(page, log)

	// Personalize message template
	message := personalizeMessage(cfg.MessageTemplate, firstName, cfg.Context)

	// Enforce message length limit
	if len(message) > maxMessageLength {
		message = message[:maxMessageLength]
		log.Warnw("message truncated to character limit", "limit", maxMessageLength)
	}

	// Click textarea to focus
	log.Debug("clicking message textarea")
	if err := textarea.Click("left", 1); err != nil {
		return fmt.Errorf("click textarea: %w", err)
	}

	time.Sleep(stealth.RandomDelay(500, 1000))

	// Small "thinking" pause before typing (human behavior)
	thinkPause := stealth.RandomDelay(1000, 3000)
	log.Debugw("pausing before typing (thinking)", "duration", thinkPause)
	time.Sleep(thinkPause)

	// Type message with human-like behavior
	log.Infow("typing follow-up message", "length", len(message))
	if err := stealth.TypeHuman(textarea, message, timingCfg); err != nil {
		return fmt.Errorf("type message: %w", err)
	}

	// Wait after typing (human reads what they typed)
	reviewPause := stealth.RandomDelay(2000, 4000)
	log.Debugw("pausing after typing (reviewing)", "duration", reviewPause)
	time.Sleep(reviewPause)

	// Find and click Send button
	sendBtn, err := findSendButton(page, log)
	if err != nil {
		return fmt.Errorf("find send button: %w", err)
	}

	log.Info("clicking Send button")
	if err := sendBtn.Click("left", 1); err != nil {
		return fmt.Errorf("click send button: %w", err)
	}

	// Wait for message to send
	time.Sleep(2 * time.Second)

	log.Info("follow-up message sent successfully")
	return nil
}

// findMessageButton finds the Message button on a profile page
func findMessageButton(page *rod.Page, log *zap.SugaredLogger) (*rod.Element, error) {
	// Multiple selectors for robustness
	// LinkedIn's Message button can appear in different formats
	messageSelectors := []string{
		// Primary action buttons with aria-label
		"button[aria-label*='Message']",
		"a[aria-label*='Message']",

		// Text-based matching
		"button:has-text('Message')",
		"a:has-text('Message')",

		// Common class patterns
		"button.pvs-profile-actions__action:has-text('Message')",
		"button.artdeco-button:has-text('Message')",
		"div.pvs-profile-actions button:has-text('Message')",

		// Fallback: any button/link with message text
		"button[aria-label^='Message']",
		"a[href*='/messaging/thread']",
	}

	for _, sel := range messageSelectors {
		elem, err := page.Timeout(5 * time.Second).Element(sel)
		if err != nil {
			log.Debugw("message button selector not found", "selector", sel)
			continue
		}

		// Verify it's actually a Message button
		text, _ := elem.Text()
		ariaLabel, _ := elem.Attribute("aria-label")

		if strings.Contains(strings.ToLower(text), "message") ||
			(ariaLabel != nil && strings.Contains(strings.ToLower(*ariaLabel), "message")) {
			log.Infow("found Message button", "selector", sel)
			return elem, nil
		}
	}

	// Check for cases where messaging is restricted
	restrictedReasons := []struct {
		selector string
		reason   string
	}{
		{"button:has-text('Connect')", "not yet connected"},
		{"button:has-text('Pending')", "connection pending"},
		{"div:has-text('messaging not available')", "messaging restricted"},
	}

	for _, check := range restrictedReasons {
		if _, err := page.Timeout(2 * time.Second).Element(check.selector); err == nil {
			return nil, fmt.Errorf("messaging unavailable: %s", check.reason)
		}
	}

	return nil, fmt.Errorf("message button not found")
}

// findMessageInput finds the message textarea in the messaging interface
//
// DETECTION STRATEGY:
// LinkedIn has two messaging interfaces:
// 1. Modal popup (quick message from profile)
// 2. Full messaging page (/messaging/thread/...)
//
// We handle both with multiple selectors
func findMessageInput(page *rod.Page, log *zap.SugaredLogger) (*rod.Element, error) {
	// Wait a bit for messaging interface to fully load
	time.Sleep(2 * time.Second)

	// Multiple selectors for message input
	// Covers both modal and full messaging page
	textareaSelectors := []string{
		// Modal messaging
		"div[role='dialog'] div[role='textbox']",
		"div[aria-label*='Write a message']",
		"div.msg-form__contenteditable",

		// Full messaging page
		"div.msg-form__msg-content-container div[role='textbox']",
		"div.msg-form__contenteditable p",

		// Fallback: any contenteditable in messaging area
		"div[contenteditable='true'][role='textbox']",
		"div.msg-form__contenteditable",

		// Alternative: traditional textarea (less common)
		"textarea[placeholder*='message']",
		"textarea.msg-form__textarea",
	}

	for _, sel := range textareaSelectors {
		elem, err := page.Timeout(5 * time.Second).Element(sel)
		if err != nil {
			log.Debugw("message input selector not found", "selector", sel)
			continue
		}

		log.Infow("found message input", "selector", sel)
		return elem, nil
	}

	return nil, fmt.Errorf("message input not found - messaging may be restricted")
}

// findSendButton finds the Send button in the messaging interface
func findSendButton(page *rod.Page, log *zap.SugaredLogger) (*rod.Element, error) {
	// Multiple selectors for Send button
	sendSelectors := []string{
		// Primary send button
		"button[aria-label*='Send']",
		"button.msg-form__send-button",
		"button:has-text('Send')",

		// Modal messaging
		"div[role='dialog'] button[aria-label*='Send']",
		"div[role='dialog'] button:has-text('Send')",

		// Full messaging page
		"div.msg-form__send-button",
		"button.artdeco-button--primary:has-text('Send')",

		// Fallback: any send button
		"button[type='submit'][aria-label*='Send']",
	}

	for _, sel := range sendSelectors {
		elem, err := page.Timeout(5 * time.Second).Element(sel)
		if err != nil {
			log.Debugw("send button selector not found", "selector", sel)
			continue
		}

		// Verify it's actually a Send button
		text, _ := elem.Text()
		ariaLabel, _ := elem.Attribute("aria-label")

		if strings.Contains(strings.ToLower(text), "send") ||
			(ariaLabel != nil && strings.Contains(strings.ToLower(*ariaLabel), "send")) {
			log.Infow("found Send button", "selector", sel)
			return elem, nil
		}
	}

	return nil, fmt.Errorf("send button not found")
}

// extractFirstNameFromProfile extracts the first name from a LinkedIn profile
//
// EXTRACTION STRATEGY:
// 1. Look for h1 heading (main profile name)
// 2. Extract full name
// 3. Split on whitespace and take first word
// 4. Fallback to "there" if extraction fails
//
// This is safe and defensive - never crashes on missing data
func extractFirstNameFromProfile(page *rod.Page, log *zap.SugaredLogger) string {
	// Selectors for profile name heading
	nameSelectors := []string{
		"h1.text-heading-xlarge",
		"h1.inline.t-24",
		"div.pv-text-details__left-panel h1",
		"h1[class*='profile']",
	}

	for _, sel := range nameSelectors {
		elem, err := page.Timeout(3 * time.Second).Element(sel)
		if err != nil {
			continue
		}

		fullName, err := elem.Text()
		if err != nil || fullName == "" {
			continue
		}

		// Extract first name
		fullName = strings.TrimSpace(fullName)
		parts := strings.Fields(fullName)
		if len(parts) > 0 {
			firstName := parts[0]
			log.Debugw("extracted first name from profile", "name", firstName)
			return firstName
		}
	}

	log.Debug("could not extract first name, using fallback")
	return "there" // Polite fallback
}

// personalizeMessage replaces template variables with actual values
//
// SUPPORTED VARIABLES:
// - {{name}} -> first name
// - {{context}} -> custom context (e.g., "software engineering", "your React work")
//
// DEFENSIVE BEHAVIOR:
// - If variable missing, use fallback text
// - If template malformed, return as-is
// - Never crashes on bad input
func personalizeMessage(template, firstName, context string) string {
	message := template

	// Replace {{name}} with first name
	if firstName != "" {
		message = strings.ReplaceAll(message, "{{name}}", firstName)
	} else {
		// Fallback: remove {{name}} or replace with generic greeting
		message = strings.ReplaceAll(message, "{{name}}", "there")
	}

	// Replace {{context}} with provided context
	if context != "" {
		message = strings.ReplaceAll(message, "{{context}}", context)
	} else {
		// Fallback: remove {{context}} or replace with generic text
		message = strings.ReplaceAll(message, "{{context}}", "your profile")
	}

	return message
}

// newMessageState creates a fresh message state
func newMessageState() *MessageState {
	return &MessageState{
		Date:             time.Now().Format("2006-01-02"),
		MessagesSentToday: 0,
		MessagedProfiles:  make(map[string]MessageRecord),
	}
}

// loadMessageState loads message state from persistent storage
func loadMessageState(ctx context.Context, store storage.StateStore, log *zap.SugaredLogger) (*MessageState, error) {
	data, err := store.Load(ctx, stateKeyMessageState)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}

	if len(data) == 0 {
		return newMessageState(), nil
	}

	var state MessageState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}

	// Ensure maps are initialized
	if state.MessagedProfiles == nil {
		state.MessagedProfiles = make(map[string]MessageRecord)
	}

	log.Infow("loaded message state from storage",
		"date", state.Date,
		"sentToday", state.MessagesSentToday,
		"totalMessaged", len(state.MessagedProfiles),
	)

	return &state, nil
}

// saveMessageState saves message state to persistent storage
func saveMessageState(ctx context.Context, store storage.StateStore, state *MessageState, log *zap.SugaredLogger) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	if err := store.Save(ctx, stateKeyMessageState, data); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	log.Debugw("saved message state to storage",
		"date", state.Date,
		"sentToday", state.MessagesSentToday,
		"totalMessaged", len(state.MessagedProfiles),
	)

	return nil
}
