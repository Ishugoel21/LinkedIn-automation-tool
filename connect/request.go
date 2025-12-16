package connect

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

// ConnectionState tracks connection request attempts and daily limits
type ConnectionState struct {
	Date              string              `json:"date"`                // YYYY-MM-DD format
	RequestsSentToday int                 `json:"requests_sent_today"` // Count for current day
	AttemptedProfiles map[string]bool     `json:"attempted_profiles"`  // Profile URLs already attempted
	SuccessfulSends   map[string]string   `json:"successful_sends"`    // Profile URL -> timestamp
	FailedAttempts    map[string]string   `json:"failed_attempts"`     // Profile URL -> reason
}

// RequestConfig holds configuration for connection requests
type RequestConfig struct {
	MaxPerDay            int    // Maximum connection requests per day
	UsePersonalizedNotes bool   // Whether to add personalized notes
	NoteTemplate         string // Template for connection note
	WaitBetweenRequests  int    // Milliseconds to wait between requests
}

const (
	stateKeyConnectionState = "connection_state"
	maxNoteLength           = 300 // LinkedIn's character limit
)

// SendRequests sends connection requests to a list of profile URLs with rate limiting
func SendRequests(
	ctx context.Context,
	page *rod.Page,
	profiles []string,
	store storage.StateStore,
	reqCfg RequestConfig,
	timingCfg config.TimingConfig,
	log *zap.SugaredLogger,
) error {

	if len(profiles) == 0 {
		log.Info("no profiles provided for connection requests")
		return nil
	}

	log.Infow("starting connection request campaign",
		"totalProfiles", len(profiles),
		"maxPerDay", reqCfg.MaxPerDay,
		"useNotes", reqCfg.UsePersonalizedNotes,
	)

	// Load connection state
	state, err := loadConnectionState(ctx, store, log)
	if err != nil {
		log.Warnw("failed to load connection state, starting fresh", "error", err)
		state = newConnectionState()
	}

	// Check if we need to reset daily counter (new day)
	today := time.Now().Format("2006-01-02")
	if state.Date != today {
		log.Infow("new day detected, resetting daily counter", "previousDate", state.Date, "today", today)
		state.Date = today
		state.RequestsSentToday = 0
	}

	// Check if daily limit already reached
	if state.RequestsSentToday >= reqCfg.MaxPerDay {
		log.Warnw("daily connection limit already reached",
			"sent", state.RequestsSentToday,
			"limit", reqCfg.MaxPerDay,
		)
		return fmt.Errorf("daily connection limit reached: %d/%d", state.RequestsSentToday, reqCfg.MaxPerDay)
	}

	successCount := 0
	skipCount := 0
	errorCount := 0

	for i, profileURL := range profiles {
		// Check daily limit before each attempt
		if state.RequestsSentToday >= reqCfg.MaxPerDay {
			log.Warnw("daily connection limit reached during campaign",
				"sent", state.RequestsSentToday,
				"limit", reqCfg.MaxPerDay,
				"remaining", len(profiles)-i,
			)
			break
		}

		// Check if already attempted
		if state.AttemptedProfiles[profileURL] {
			log.Debugw("skipping already attempted profile", "url", profileURL)
			skipCount++
			continue
		}

		log.Infow("processing profile",
			"index", i+1,
			"total", len(profiles),
			"sentToday", state.RequestsSentToday,
			"limit", reqCfg.MaxPerDay,
			"url", profileURL,
		)

		// Send connection request
		err := sendConnectionRequest(ctx, page, profileURL, reqCfg, timingCfg, log)

		// Mark as attempted regardless of outcome
		state.AttemptedProfiles[profileURL] = true

		if err != nil {
			log.Warnw("failed to send connection request",
				"url", profileURL,
				"error", err,
			)
			state.FailedAttempts[profileURL] = err.Error()
			errorCount++
		} else {
			// Success
			state.RequestsSentToday++
			state.SuccessfulSends[profileURL] = time.Now().Format(time.RFC3339)
			successCount++

			log.Infow("✅ connection request sent successfully",
				"url", profileURL,
				"sentToday", state.RequestsSentToday,
			)
		}

		// Save state after each attempt
		if err := saveConnectionState(ctx, store, state, log); err != nil {
			log.Warnw("failed to save connection state", "error", err)
		}

		// Human-like delay between requests (unless this is the last one)
		if i < len(profiles)-1 {
			waitTime := time.Duration(reqCfg.WaitBetweenRequests) * time.Millisecond
			if waitTime < 5*time.Second {
				waitTime = 5 * time.Second // Minimum 5 seconds between requests
			}

			// Add randomness
			waitTime += stealth.RandomDelay(2000, 5000)

			log.Infow("waiting before next request", "duration", waitTime)
			time.Sleep(waitTime)
		}
	}

	log.Infow("connection request campaign completed",
		"successful", successCount,
		"skipped", skipCount,
		"errors", errorCount,
		"totalSentToday", state.RequestsSentToday,
		"limit", reqCfg.MaxPerDay,
	)

	return nil
}

// sendConnectionRequest sends a single connection request to a profile
func sendConnectionRequest(
	ctx context.Context,
	page *rod.Page,
	profileURL string,
	reqCfg RequestConfig,
	timingCfg config.TimingConfig,
	log *zap.SugaredLogger,
) error {

	// Navigate to profile
	log.Infow("navigating to profile", "url", profileURL)
	if err := page.Timeout(30 * time.Second).Navigate(profileURL); err != nil {
		return fmt.Errorf("navigate to profile: %w", err)
	}

	if err := page.Timeout(30 * time.Second).WaitLoad(); err != nil {
		return fmt.Errorf("wait for profile load: %w", err)
	}

	// Wait for profile to render
	time.Sleep(3 * time.Second)

	// Check if profile is available
	if !isProfileAvailable(page, log) {
		return fmt.Errorf("profile unavailable or private")
	}

	// Scroll naturally to see profile content
	log.Debug("scrolling profile page naturally")
	if err := humanScrollProfile(page, timingCfg); err != nil {
		log.Warnw("profile scroll failed", "error", err)
	}

	// Find and click Connect button
	connectBtn, err := findConnectButton(page, log)
	if err != nil {
		return fmt.Errorf("find connect button: %w", err)
	}

	// Move mouse to button with human-like motion
	log.Debug("moving mouse to Connect button")
	if err := stealth.MoveToElementHuman(page, connectBtn, timingCfg); err != nil {
		log.Warnw("mouse movement to button failed, clicking directly", "error", err)
	}

	// Small hover pause
	time.Sleep(stealth.RandomDelay(500, 1200))

	// Click Connect button
	log.Info("clicking Connect button")
	if err := connectBtn.Click("left", 1); err != nil {
		return fmt.Errorf("click connect button: %w", err)
	}

	// Wait for modal or confirmation
	time.Sleep(2 * time.Second)

	// Check if "Add a note" modal appeared
	if hasNoteModal(page, log) {
		if reqCfg.UsePersonalizedNotes && reqCfg.NoteTemplate != "" {
			log.Info("note modal detected, adding personalized note")
			if err := addPersonalizedNote(page, reqCfg.NoteTemplate, timingCfg, log); err != nil {
				log.Warnw("failed to add note, sending without note", "error", err)
				// Try to send without note
				if err := clickSendWithoutNote(page, log); err != nil {
					return fmt.Errorf("send connection: %w", err)
				}
			}
		} else {
			// Send without note
			log.Info("sending connection request without note")
			if err := clickSendWithoutNote(page, log); err != nil {
				return fmt.Errorf("send connection: %w", err)
			}
		}
	} else {
		// No modal, connection request likely sent immediately
		log.Info("no modal detected, connection request likely sent")
	}

	// Final wait to let request process
	time.Sleep(2 * time.Second)

	return nil
}

// isProfileAvailable checks if the profile page loaded successfully
func isProfileAvailable(page *rod.Page, log *zap.SugaredLogger) bool {
	// Check for profile header elements
	profileSelectors := []string{
		"div.pv-top-card",                    // Main profile card
		"section.artdeco-card",               // Profile sections
		"h1.text-heading-xlarge",             // Name heading
		"div.ph5.pb5",                        // Profile container
		"main.scaffold-layout__main",         // Main content area
	}

	for _, sel := range profileSelectors {
		if _, err := page.Timeout(3 * time.Second).Element(sel); err == nil {
			log.Debugw("profile available", "selector", sel)
			return true
		}
	}

	log.Warn("profile not available - may be private or restricted")
	return false
}

// findConnectButton finds the Connect button on a profile page
func findConnectButton(page *rod.Page, log *zap.SugaredLogger) (*rod.Element, error) {
	// LinkedIn Connect button selectors - multiple strategies for robustness
	// The Connect button can appear in different places and formats
	connectSelectors := []string{
		// Primary action button with "Connect" text
		"button[aria-label*='Invite'][aria-label*='connect']",
		"button[aria-label^='Invite']",
		
		// Button with specific text content
		"button:has-text('Connect')",
		"button.pvs-profile-actions__action:has-text('Connect')",
		
		// Common class patterns
		"button.artdeco-button--secondary:has-text('Connect')",
		"button.pvs-profile-actions__action",
		
		// Fallback: any button with Connect
		"button[aria-label*='Connect']",
	}

	for _, sel := range connectSelectors {
		btn, err := page.Timeout(5 * time.Second).Element(sel)
		if err != nil {
			log.Debugw("connect button selector not found", "selector", sel)
			continue
		}

		// Verify button text contains "Connect"
		text, err := btn.Text()
		if err == nil && strings.Contains(strings.ToLower(text), "connect") {
			log.Infow("found Connect button", "selector", sel, "text", text)
			return btn, nil
		}

		// Check aria-label
		ariaLabel, err := btn.Attribute("aria-label")
		if err == nil && ariaLabel != nil && strings.Contains(strings.ToLower(*ariaLabel), "connect") {
			log.Infow("found Connect button via aria-label", "selector", sel, "label", *ariaLabel)
			return btn, nil
		}
	}

	// Check for cases where Connect is not available
	unavailableReasons := []struct {
		selector string
		reason   string
	}{
		{"button:has-text('Following')", "already following"},
		{"button:has-text('Pending')", "connection request already pending"},
		{"button:has-text('Message')", "already connected"},
		{"span:has-text('Connection')", "already connected"},
	}

	for _, check := range unavailableReasons {
		if _, err := page.Timeout(2 * time.Second).Element(check.selector); err == nil {
			return nil, fmt.Errorf("connect unavailable: %s", check.reason)
		}
	}

	return nil, fmt.Errorf("connect button not found - may already be connected or profile restricted")
}

// hasNoteModal checks if the "Add a note" modal appeared
func hasNoteModal(page *rod.Page, log *zap.SugaredLogger) bool {
	// Modal selectors
	modalSelectors := []string{
		"div[role='dialog'][aria-labelledby*='send-invite']",
		"div.send-invite",
		"button[aria-label*='Add a note']",
		"button:has-text('Add a note')",
		"textarea[name='message']",
	}

	for _, sel := range modalSelectors {
		if _, err := page.Timeout(2 * time.Second).Element(sel); err == nil {
			log.Debugw("note modal detected", "selector", sel)
			return true
		}
	}

	log.Debug("no note modal detected")
	return false
}

// addPersonalizedNote adds a personalized note to the connection request
func addPersonalizedNote(
	page *rod.Page,
	noteTemplate string,
	timingCfg config.TimingConfig,
	log *zap.SugaredLogger,
) error {

	// Click "Add a note" button if it exists
	addNoteSelectors := []string{
		"button[aria-label*='Add a note']",
		"button:has-text('Add a note')",
	}

	clickedAddNote := false
	for _, sel := range addNoteSelectors {
		btn, err := page.Timeout(3 * time.Second).Element(sel)
		if err != nil {
			continue
		}

		log.Debug("clicking 'Add a note' button")
		if err := btn.Click("left", 1); err != nil {
			continue
		}

		clickedAddNote = true
		time.Sleep(time.Second)
		break
	}

	if !clickedAddNote {
		log.Debug("'Add a note' button not found, looking for textarea directly")
	}

	// Find textarea
	textareaSelectors := []string{
		"textarea[name='message']",
		"textarea[id*='custom-message']",
		"textarea[aria-label*='message']",
	}

	var textarea *rod.Element
	var err error

	for _, sel := range textareaSelectors {
		textarea, err = page.Timeout(3 * time.Second).Element(sel)
		if err == nil {
			log.Debugw("found note textarea", "selector", sel)
			break
		}
	}

	if textarea == nil {
		return fmt.Errorf("note textarea not found")
	}

	// Extract first name from profile (optional personalization)
	firstName := extractFirstName(page, log)

	// Personalize note template
	note := personalizeNote(noteTemplate, firstName)

	// Enforce character limit
	if len(note) > maxNoteLength {
		note = note[:maxNoteLength]
		log.Warnw("note truncated to character limit", "limit", maxNoteLength)
	}

	// Click textarea to focus
	if err := textarea.Click("left", 1); err != nil {
		return fmt.Errorf("click textarea: %w", err)
	}

	time.Sleep(stealth.RandomDelay(300, 700))

	// Type note with human-like behavior
	log.Infow("typing personalized note", "length", len(note))
	if err := stealth.TypeHuman(textarea, note, timingCfg); err != nil {
		return fmt.Errorf("type note: %w", err)
	}

	// Wait after typing (human reads what they typed)
	time.Sleep(stealth.RandomDelay(1000, 2000))

	// Click Send button
	sendSelectors := []string{
		"button[aria-label*='Send'][aria-label*='invitation']",
		"button[aria-label*='Send now']",
		"button:has-text('Send')",
		"button.artdeco-button--primary:has-text('Send')",
	}

	for _, sel := range sendSelectors {
		btn, err := page.Timeout(3 * time.Second).Element(sel)
		if err != nil {
			continue
		}

		log.Info("clicking Send button")
		if err := btn.Click("left", 1); err != nil {
			continue
		}

		log.Info("connection request sent with personalized note")
		return nil
	}

	return fmt.Errorf("send button not found")
}

// clickSendWithoutNote sends the connection request without adding a note
func clickSendWithoutNote(page *rod.Page, log *zap.SugaredLogger) error {
	// Look for "Send without a note" or "Send" button
	sendSelectors := []string{
		"button[aria-label*='Send without a note']",
		"button:has-text('Send without a note')",
		"button[aria-label*='Send now']",
		"button:has-text('Send')",
		"button.artdeco-button--primary",
	}

	for _, sel := range sendSelectors {
		btn, err := page.Timeout(3 * time.Second).Element(sel)
		if err != nil {
			log.Debugw("send button selector not found", "selector", sel)
			continue
		}

		text, _ := btn.Text()
		log.Infow("clicking send button", "selector", sel, "text", text)

		if err := btn.Click("left", 1); err != nil {
			log.Warnw("failed to click send button", "selector", sel, "error", err)
			continue
		}

		log.Info("connection request sent without note")
		return nil
	}

	return fmt.Errorf("send button not found")
}

// extractFirstName extracts the first name from the profile page
func extractFirstName(page *rod.Page, log *zap.SugaredLogger) string {
	// Selectors for profile name
	nameSelectors := []string{
		"h1.text-heading-xlarge",
		"h1.inline.t-24",
		"div.pv-top-card h1",
	}

	for _, sel := range nameSelectors {
		nameEl, err := page.Timeout(2 * time.Second).Element(sel)
		if err != nil {
			continue
		}

		fullName, err := nameEl.Text()
		if err != nil || fullName == "" {
			continue
		}

		// Extract first name (before first space)
		parts := strings.Fields(fullName)
		if len(parts) > 0 {
			firstName := parts[0]
			log.Debugw("extracted first name", "name", firstName)
			return firstName
		}
	}

	log.Debug("could not extract first name from profile")
	return ""
}

// personalizeNote replaces placeholders in the note template
func personalizeNote(template string, firstName string) string {
	note := template

	// Replace {{name}} placeholder
	if firstName != "" {
		note = strings.ReplaceAll(note, "{{name}}", firstName)
	} else {
		// Fallback to generic greeting
		note = strings.ReplaceAll(note, "Hi {{name}}", "Hi")
		note = strings.ReplaceAll(note, "{{name}}", "there")
	}

	// Remove other common placeholders
	note = strings.ReplaceAll(note, "{{context}}", "LinkedIn")

	return note
}

// humanScrollProfile scrolls the profile page naturally
func humanScrollProfile(page *rod.Page, timingCfg config.TimingConfig) error {
	// Scroll down 2-3 times to view profile sections
	scrolls := 2 + (int(time.Now().UnixNano()) % 2) // 2-3 scrolls

	for i := 0; i < scrolls; i++ {
		scrollDistance := 300 + (time.Now().UnixNano() % 300) // 300-600px

		if err := page.Mouse.Scroll(0, float64(scrollDistance), 1); err != nil {
			return err
		}

		// Pause between scrolls
		time.Sleep(stealth.RandomDelay(
			timingCfg.MinDelayMs,
			timingCfg.MaxDelayMs,
		))
	}

	// Scroll back up slightly (human behavior)
	backScroll := 100 + (time.Now().UnixNano() % 100)
	_ = page.Mouse.Scroll(0, -float64(backScroll), 1)
	time.Sleep(stealth.RandomDelay(500, 1000))

	return nil
}

// newConnectionState creates a new connection state
func newConnectionState() *ConnectionState {
	return &ConnectionState{
		Date:              time.Now().Format("2006-01-02"),
		RequestsSentToday: 0,
		AttemptedProfiles: make(map[string]bool),
		SuccessfulSends:   make(map[string]string),
		FailedAttempts:    make(map[string]string),
	}
}

// loadConnectionState loads connection state from storage
func loadConnectionState(ctx context.Context, store storage.StateStore, log *zap.SugaredLogger) (*ConnectionState, error) {
	data, err := store.Load(ctx, stateKeyConnectionState)
	if err != nil {
		if err == storage.ErrNotFound {
			return newConnectionState(), nil
		}
		return nil, err
	}

	var state ConnectionState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Warnw("failed to parse connection state, starting fresh", "error", err)
		return newConnectionState(), nil
	}

	// Ensure maps are initialized
	if state.AttemptedProfiles == nil {
		state.AttemptedProfiles = make(map[string]bool)
	}
	if state.SuccessfulSends == nil {
		state.SuccessfulSends = make(map[string]string)
	}
	if state.FailedAttempts == nil {
		state.FailedAttempts = make(map[string]string)
	}

	log.Infow("loaded connection state",
		"date", state.Date,
		"sentToday", state.RequestsSentToday,
		"attemptedTotal", len(state.AttemptedProfiles),
	)

	return &state, nil
}

// saveConnectionState saves connection state to storage
func saveConnectionState(ctx context.Context, store storage.StateStore, state *ConnectionState, log *zap.SugaredLogger) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal connection state: %w", err)
	}

	if err := store.Save(ctx, stateKeyConnectionState, data); err != nil {
		return fmt.Errorf("save connection state: %w", err)
	}

	log.Debugw("saved connection state",
		"date", state.Date,
		"sentToday", state.RequestsSentToday,
	)

	return nil
}
