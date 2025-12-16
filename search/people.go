package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"go.uber.org/zap"

	"linkedin-automation-tool/config"
	"linkedin-automation-tool/stealth"
	"linkedin-automation-tool/storage"
)

// SearchParams defines parameters for LinkedIn people search
type SearchParams struct {
	Keywords string // General search keywords
	JobTitle string // Current job title filter
	Company  string // Company name filter
	Location string // Geographic location filter
	MaxPages int    // Maximum number of pages to crawl (0 = no limit)
}

// SearchResult holds a profile URL and metadata
type SearchResult struct {
	ProfileURL string
	Name       string // Optional: profile name if extracted
	Headline   string // Optional: headline if extracted
}

const (
	stateKeySeenProfiles = "seen_profiles"
	searchBaseURL        = "https://www.linkedin.com/search/results/people/"
	linkedInHomeURL      = "https://www.linkedin.com/feed/"
)

// FindPeople performs a LinkedIn people search and returns profile URLs.
// It handles pagination, de-duplication, and human-like interaction.
func FindPeople(
	ctx context.Context,
	page *rod.Page,
	store storage.StateStore,
	params SearchParams,
	cfg config.TimingConfig,
	log *zap.SugaredLogger,
) ([]string, error) {

	log.Infow("starting people search",
		"keywords", params.Keywords,
		"jobTitle", params.JobTitle,
		"company", params.Company,
		"location", params.Location,
		"maxPages", params.MaxPages,
	)

	// Load previously seen profiles from state
	seenProfiles, err := loadSeenProfiles(ctx, store, log)
	if err != nil {
		log.Warnw("failed to load seen profiles, starting fresh", "error", err)
		seenProfiles = make(map[string]bool)
	}

	// Use human-like search typing instead of URL navigation
	log.Info("performing human-like search via search box...")
	if err := performHumanSearch(page, params, cfg, log); err != nil {
		log.Warnw("human search failed, falling back to URL navigation", "error", err)
		// Fallback to URL navigation
		searchURL, err := buildSearchURL(params)
		if err != nil {
			return nil, fmt.Errorf("build search URL: %w", err)
		}
		log.Infow("navigating to search", "url", searchURL)
		if err := page.Timeout(30 * time.Second).Navigate(searchURL); err != nil {
			return nil, fmt.Errorf("navigate to search: %w", err)
		}
		if err := page.Timeout(30 * time.Second).WaitLoad(); err != nil {
			return nil, fmt.Errorf("wait for search page load: %w", err)
		}
	}

	// Wait for search results to appear
	time.Sleep(3 * time.Second)

	// Human-like: scroll to trigger lazy-loaded content
	log.Info("scrolling to load results")
	if err := humanScrollOnce(page, cfg); err != nil {
		log.Warnw("initial scroll failed", "error", err)
	}

	// Collect profiles with pagination
	var allProfiles []string
	currentPage := 1
	maxPages := params.MaxPages
	if maxPages == 0 {
		maxPages = 10 // Default safety limit
	}

	for currentPage <= maxPages {
		log.Infow("processing search results page", "page", currentPage, "maxPages", maxPages)

		// Extract profile URLs from current page
		profiles, err := extractProfileURLs(page, log)
		if err != nil {
			return nil, fmt.Errorf("extract profiles on page %d: %w", currentPage, err)
		}

		log.Infow("extracted profiles from page", "page", currentPage, "count", len(profiles))

		// De-duplicate and collect new profiles
		newCount := 0
		for _, profileURL := range profiles {
			normalized := normalizeProfileURL(profileURL)
			if normalized == "" {
				continue
			}

			if seenProfiles[normalized] {
				log.Debugw("skipping duplicate profile", "url", normalized)
				continue
			}

			seenProfiles[normalized] = true
			allProfiles = append(allProfiles, normalized)
			newCount++
			log.Debugw("collected new profile", "url", normalized)
		}

		log.Infow("new profiles on page", "page", currentPage, "new", newCount, "duplicates", len(profiles)-newCount)

		// Human-like: scroll through results
		if err := humanScrollResults(page, cfg, log); err != nil {
			log.Warnw("scroll results failed", "error", err)
		}

		// Check for next page
		hasNext, err := hasNextPage(page)
		if err != nil {
			log.Warnw("error checking for next page", "error", err)
			break
		}

		if !hasNext {
			log.Infow("no more pages available", "finalPage", currentPage)
			break
		}

		// Click next page with human-like behavior
		log.Infow("navigating to next page", "nextPage", currentPage+1)
		if err := clickNextPage(page, cfg, log); err != nil {
			log.Warnw("failed to navigate to next page", "page", currentPage, "error", err)
			break
		}

		// Wait for new results to load
		time.Sleep(3 * time.Second)

		// Human-like: scroll to trigger new content
		if err := humanScrollOnce(page, cfg); err != nil {
			log.Warnw("scroll after pagination failed", "error", err)
		}

		currentPage++

		// Human pause between pages
		pauseDuration := stealth.RandomDelay(
			max(2000, cfg.MinDelayMs*3),
			max(5000, cfg.MaxDelayMs*3),
		)
		log.Debugw("pausing before next page", "duration", pauseDuration)
		time.Sleep(pauseDuration)
	}

	// Save updated seen profiles to state
	if err := saveSeenProfiles(ctx, store, seenProfiles, log); err != nil {
		log.Warnw("failed to save seen profiles", "error", err)
	}

	log.Infow("search completed",
		"totalProfiles", len(allProfiles),
		"pagesProcessed", currentPage,
	)

	return allProfiles, nil
}

// buildSearchURL constructs the LinkedIn people search URL with filters
func buildSearchURL(params SearchParams) (string, error) {
	base, err := url.Parse(searchBaseURL)
	if err != nil {
		return "", err
	}

	query := url.Values{}

	// Build keywords query combining all filters
	var keywordParts []string

	if params.Keywords != "" {
		keywordParts = append(keywordParts, params.Keywords)
	}

	if params.JobTitle != "" {
		keywordParts = append(keywordParts, params.JobTitle)
	}

	if params.Company != "" {
		keywordParts = append(keywordParts, params.Company)
	}

	if len(keywordParts) > 0 {
		query.Set("keywords", strings.Join(keywordParts, " "))
	}

	// Location is typically a separate filter in LinkedIn
	if params.Location != "" {
		// LinkedIn uses geoUrn for location filtering
		// For simplicity, include in keywords
		query.Set("keywords", query.Get("keywords")+" "+params.Location)
	}

	base.RawQuery = query.Encode()
	return base.String(), nil
}

// extractProfileURLs extracts all profile URLs from the current search results page
func extractProfileURLs(page *rod.Page, log *zap.SugaredLogger) ([]string, error) {
	// Wait longer for dynamic content to load
	log.Info("waiting for search result cards to load...")
	time.Sleep(3 * time.Second)

	// Scroll down to trigger lazy-loaded content
	for i := 0; i < 3; i++ {
		_ = page.Mouse.Scroll(0, 300, 1)
		time.Sleep(800 * time.Millisecond)
	}

	// Additional wait after scrolling
	time.Sleep(2 * time.Second)

	// LinkedIn search results use various selectors over time
	// We try multiple strategies for robustness
	selectors := []string{
		"a[href*='/in/']",                                       // Broadest - all profile links
		"a.app-aware-link[href*='/in/']",                        // Primary profile links
		"a[href*='/in/'][data-control-name*='search']",          // Search result links
		"span.entity-result__title-text a[href*='/in/']",        // Result title links
		"div.entity-result a.app-aware-link[href*='/in/']",      // Entity result links
		"li.reusable-search__result-container a[href*='/in/']",  // Reusable search results
		"a[href^='/in/']",                                       // Relative URLs starting with /in/
		"a.ember-view[href*='/in/']",                            // Ember framework links
	}

	var allLinks []string
	foundAny := false

	for _, sel := range selectors {
		elements, err := page.Timeout(10 * time.Second).Elements(sel)
		if err != nil {
			log.Debugw("selector not found", "selector", sel, "error", err)
			continue
		}

		log.Debugw("found elements with selector", "selector", sel, "count", len(elements))

		for _, el := range elements {
			href, err := el.Attribute("href")
			if err != nil || href == nil {
				continue
			}

			profileURL := *href
			if isValidProfileURL(profileURL) {
				allLinks = append(allLinks, profileURL)
				foundAny = true
			}
		}

		if len(allLinks) > 0 {
			log.Infow("extracted links with selector", "selector", sel, "count", len(allLinks))
			break // Found results with this selector, no need to try others
		}
	}

	if !foundAny {
		// Log page HTML for debugging
		html, _ := page.HTML()
		log.Debugw("page HTML snapshot", "length", len(html), "preview", html[:min(500, len(html))])
		return nil, fmt.Errorf("no profile links found with any selector")
	}

	// De-duplicate within this page
	unique := make(map[string]bool)
	var result []string
	for _, link := range allLinks {
		normalized := normalizeProfileURL(link)
		if normalized != "" && !unique[normalized] {
			unique[normalized] = true
			result = append(result, normalized)
		}
	}

	return result, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// isValidProfileURL checks if a URL is a valid LinkedIn profile URL
func isValidProfileURL(rawURL string) bool {
	// Must contain /in/ path
	if !strings.Contains(rawURL, "/in/") {
		return false
	}

	// Exclude certain patterns that aren't real profiles
	excludePatterns := []string{
		"/company/",
		"/school/",
		"/groups/",
		"/events/",
		"/jobs/",
		"/posts/",
		"urn:li:fs_miniProfile:",
	}

	for _, pattern := range excludePatterns {
		if strings.Contains(rawURL, pattern) {
			return false
		}
	}

	return true
}

// normalizeProfileURL cleans and normalizes a profile URL
func normalizeProfileURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	// Parse URL
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	// Ensure it's a LinkedIn URL
	if !strings.Contains(parsed.Host, "linkedin.com") {
		// Relative URL - make it absolute
		if strings.HasPrefix(rawURL, "/in/") {
			return "https://www.linkedin.com" + strings.Split(rawURL, "?")[0]
		}
		return ""
	}

	// Remove query parameters (tracking, etc.)
	parsed.RawQuery = ""
	parsed.Fragment = ""

	// Normalize path
	path := parsed.Path
	// Remove trailing slash
	path = strings.TrimSuffix(path, "/")

	// Extract just the profile identifier
	if strings.Contains(path, "/in/") {
		parts := strings.Split(path, "/in/")
		if len(parts) > 1 {
			profileID := strings.Split(parts[1], "/")[0]
			return "https://www.linkedin.com/in/" + profileID
		}
	}

	return parsed.String()
}

// hasNextPage checks if there's a next page button available
func hasNextPage(page *rod.Page) (bool, error) {
	// LinkedIn pagination selectors
	nextButtonSelectors := []string{
		"button[aria-label='Next']",
		"button.artdeco-pagination__button--next",
		"li.artdeco-pagination__indicator--number:not(.selected) + li button",
		"button[data-test-pagination-page-btn='next']",
	}

	for _, sel := range nextButtonSelectors {
		el, err := page.Timeout(3 * time.Second).Element(sel)
		if err != nil {
			continue
		}

		// Check if button is disabled
		disabled, _ := el.Attribute("disabled")
		ariaDisabled, _ := el.Attribute("aria-disabled")

		if disabled != nil || (ariaDisabled != nil && *ariaDisabled == "true") {
			return false, nil
		}

		// Button exists and is not disabled
		return true, nil
	}

	return false, nil
}

// clickNextPage clicks the next page button with human-like behavior
func clickNextPage(page *rod.Page, cfg config.TimingConfig, log *zap.SugaredLogger) error {
	nextButtonSelectors := []string{
		"button[aria-label='Next']",
		"button.artdeco-pagination__button--next",
		"button[data-test-pagination-page-btn='next']",
	}

	for _, sel := range nextButtonSelectors {
		el, err := page.Timeout(5 * time.Second).Element(sel)
		if err != nil {
			continue
		}

		// Scroll button into view
		if err := el.ScrollIntoView(); err != nil {
			log.Warnw("scroll to next button failed", "error", err)
		}

		time.Sleep(stealth.RandomDelay(500, 1000))

		// Click the button
		if err := el.Click("left", 1); err != nil {
			log.Warnw("click next button failed", "selector", sel, "error", err)
			continue
		}

		log.Infow("clicked next page button", "selector", sel)

		// Wait for navigation to start
		time.Sleep(2 * time.Second)
		return nil
	}

	return fmt.Errorf("could not find or click next page button")
}

// humanScrollOnce performs a single human-like scroll
func humanScrollOnce(page *rod.Page, cfg config.TimingConfig) error {
	scrollDistance := 300 + (time.Now().UnixNano() % 400) // 300-700px
	if err := page.Timeout(5 * time.Second).Mouse.Scroll(0, float64(scrollDistance), 1); err != nil {
		return err
	}
	time.Sleep(stealth.RandomDelay(800, 1500))
	return nil
}

// humanScrollResults scrolls through the search results page in a human-like way
func humanScrollResults(page *rod.Page, cfg config.TimingConfig, log *zap.SugaredLogger) error {
	// Scroll down a few times to view results
	scrolls := 2 + (int(time.Now().UnixNano()) % 3) // 2-4 scrolls

	for i := 0; i < scrolls; i++ {
		scrollDistance := 400 + (time.Now().UnixNano() % 400) // 400-800px

		if err := page.Timeout(5 * time.Second).Mouse.Scroll(0, float64(scrollDistance), 1); err != nil {
			log.Warnw("scroll failed", "attempt", i+1, "error", err)
			continue
		}

		// Pause between scrolls
		time.Sleep(stealth.RandomDelay(
			max(1000, cfg.MinDelayMs*2),
			max(3000, cfg.MaxDelayMs*2),
		))

		// Occasionally scroll back up slightly (human behavior)
		if i > 0 && time.Now().UnixNano()%4 == 0 {
			backScroll := 100 + (time.Now().UnixNano() % 200)
			_ = page.Timeout(5 * time.Second).Mouse.Scroll(0, -float64(backScroll), 1)
			time.Sleep(stealth.RandomDelay(500, 1000))
		}
	}

	return nil
}

// loadSeenProfiles loads the set of previously seen profile URLs from state
func loadSeenProfiles(ctx context.Context, store storage.StateStore, log *zap.SugaredLogger) (map[string]bool, error) {
	data, err := store.Load(ctx, stateKeySeenProfiles)
	if err != nil {
		if err == storage.ErrNotFound {
			return make(map[string]bool), nil
		}
		return nil, err
	}

	// Parse JSON array of profile URLs
	var profiles []string
	if err := parseJSON(data, &profiles); err != nil {
		log.Warnw("failed to parse seen profiles, starting fresh", "error", err)
		return make(map[string]bool), nil
	}

	// Convert to map
	seenMap := make(map[string]bool, len(profiles))
	for _, url := range profiles {
		seenMap[url] = true
	}

	log.Infow("loaded seen profiles from state", "count", len(seenMap))
	return seenMap, nil
}

// saveSeenProfiles saves the set of seen profile URLs to state
func saveSeenProfiles(ctx context.Context, store storage.StateStore, seenProfiles map[string]bool, log *zap.SugaredLogger) error {
	// Convert map to slice
	profiles := make([]string, 0, len(seenProfiles))
	for url := range seenProfiles {
		profiles = append(profiles, url)
	}

	// Serialize to JSON
	data, err := toJSON(profiles)
	if err != nil {
		return fmt.Errorf("serialize profiles: %w", err)
	}

	if err := store.Save(ctx, stateKeySeenProfiles, data); err != nil {
		return fmt.Errorf("save to state: %w", err)
	}

	log.Infow("saved seen profiles to state", "count", len(profiles))
	return nil
}

// Helper functions for JSON operations
func parseJSON(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

func toJSON(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// performHumanSearch performs a search using the search box with human-like typing
func performHumanSearch(page *rod.Page, params SearchParams, cfg config.TimingConfig, log *zap.SugaredLogger) error {
	// Build search query
	var queryParts []string
	if params.Keywords != "" {
		queryParts = append(queryParts, params.Keywords)
	}
	if params.JobTitle != "" {
		queryParts = append(queryParts, params.JobTitle)
	}
	if params.Company != "" {
		queryParts = append(queryParts, params.Company)
	}
	if params.Location != "" {
		queryParts = append(queryParts, params.Location)
	}

	if len(queryParts) == 0 {
		return fmt.Errorf("no search terms provided")
	}

	searchQuery := strings.Join(queryParts, " ")
	log.Infow("searching with query", "query", searchQuery)

	// Navigate to feed first if not already there
	currentURL := page.MustInfo().URL
	if !strings.Contains(currentURL, "linkedin.com/feed") && !strings.Contains(currentURL, "linkedin.com/search") {
		log.Info("navigating to LinkedIn feed first...")
		if err := page.Timeout(30 * time.Second).Navigate(linkedInHomeURL); err != nil {
			return fmt.Errorf("navigate to feed: %w", err)
		}
		if err := page.Timeout(30 * time.Second).WaitLoad(); err != nil {
			return fmt.Errorf("wait for feed: %w", err)
		}
		time.Sleep(2 * time.Second)
	}

	// Find and click the search box
	searchBoxSelectors := []string{
		"input[placeholder*='Search']",
		"input.search-global-typeahead__input",
		"input[aria-label*='Search']",
		"input.search-global-typeahead__collapsed-search-button-input",
		"button.search-global-typeahead__button",
		"div.search-global-typeahead input",
	}

	var searchBox *rod.Element
	var err error

	for _, sel := range searchBoxSelectors {
		searchBox, err = page.Timeout(5 * time.Second).Element(sel)
		if err == nil {
			log.Infow("found search box", "selector", sel)
			break
		}
		log.Debugw("search box selector not found", "selector", sel)
	}

	if searchBox == nil {
		return fmt.Errorf("could not find search box with any selector")
	}

	// Scroll to search box
	if err := searchBox.ScrollIntoView(); err != nil {
		log.Warnw("scroll to search box failed", "error", err)
	}
	time.Sleep(stealth.RandomDelay(300, 700))

	// Click search box
	log.Info("clicking search box...")
	if err := searchBox.Click("left", 1); err != nil {
		return fmt.Errorf("click search box: %w", err)
	}

	time.Sleep(stealth.RandomDelay(500, 1000))

	// Type search query with human-like behavior
	log.Infow("typing search query", "query", searchQuery)
	if err := stealth.TypeHuman(searchBox, searchQuery, cfg); err != nil {
		return fmt.Errorf("type search query: %w", err)
	}

	// Wait a bit before submitting (human reads what they typed)
	time.Sleep(stealth.RandomDelay(800, 1500))

	// Submit search - try Enter key first
	log.Info("submitting search with Enter key...")
	if err := page.Keyboard.Press(input.Enter); err != nil {
		log.Warnw("enter key failed, trying click submit", "error", err)
		
		// Fallback: try clicking search button
		searchButtonSelectors := []string{
			"button[type='submit']",
			"button.search-global-typeahead__search-button",
			"button[aria-label*='Search']",
		}

		var clicked bool
		for _, sel := range searchButtonSelectors {
			btn, err := page.Timeout(3 * time.Second).Element(sel)
			if err == nil {
				if err := btn.Click("left", 1); err == nil {
					clicked = true
					break
				}
			}
		}

		if !clicked {
			return fmt.Errorf("could not submit search")
		}
	}

	// Wait for navigation to search results
	log.Info("waiting for search results to load...")
	time.Sleep(3 * time.Second)

	// Wait for URL to change to search results
	startTime := time.Now()
	for time.Since(startTime) < 15*time.Second {
		currentURL := page.MustInfo().URL
		if strings.Contains(currentURL, "/search/results/") {
			log.Infow("search results loaded", "url", currentURL)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Check if we need to click "People" filter
	currentURL = page.MustInfo().URL
	if !strings.Contains(currentURL, "/people/") {
		log.Info("clicking People filter...")
		peopleFilterSelectors := []string{
			"button[aria-label='People']",
			"button:has-text('People')",
			"li button:has-text('People')",
			"a[href*='/search/results/people/']",
			"div[role='tablist'] button:has-text('People')",
		}

		clickedFilter := false
		for _, sel := range peopleFilterSelectors {
			filterBtn, err := page.Timeout(3 * time.Second).Element(sel)
			if err == nil {
				if err := filterBtn.Click("left", 1); err == nil {
					log.Info("clicked People filter")
					time.Sleep(3 * time.Second) // Wait longer for filter to apply
					clickedFilter = true
					break
				}
			}
		}

		if !clickedFilter {
			log.Warn("could not click People filter, continuing anyway")
		}
	}

	// Final wait for results to load with additional scrolling
	time.Sleep(2 * time.Second)
	
	// Scroll to trigger lazy-loaded profile cards
	log.Info("scrolling to load more profile cards...")
	for i := 0; i < 2; i++ {
		_ = page.Mouse.Scroll(0, 400, 1)
		time.Sleep(time.Second)
	}

	log.Info("search submitted successfully via human-like typing")
	return nil
}
