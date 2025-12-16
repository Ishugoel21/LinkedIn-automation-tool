package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"math/big"
	mathrand "math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/joho/godotenv"
	"go.uber.org/zap"

	"linkedin-automation-tool/auth"
	"linkedin-automation-tool/config"
	"linkedin-automation-tool/connect"
	"linkedin-automation-tool/logger"
	"linkedin-automation-tool/messaging"
	"linkedin-automation-tool/navigation"
	"linkedin-automation-tool/search"
	"linkedin-automation-tool/stealth"
	"linkedin-automation-tool/storage"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Trap interrupts to exit cleanly and close the browser.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	cfg, err := config.Load("./config.yaml")
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Load optional .env to ease local development (non-production, educational).
	_ = godotenv.Load()

	zapLogger, err := logger.New(cfg.Logging.Level)
	if err != nil {
		log.Fatalf("logger: %v", err)
	}
	defer zapLogger.Sync()
	logr := zapLogger.Sugar()

	width, height := randomViewport(cfg.Browser.MinViewport, cfg.Browser.MaxViewport)

	ua := cfg.Browser.UserAgents[randomInt(len(cfg.Browser.UserAgents))]

	launchURL, err := launcher.New().
		// If a specific browser binary is provided, use it (helps in pinned Chrome revisions).
		Bin(cfg.Browser.Bin).
		// Disable leakless wrapper on Windows AV-sensitive environments.
		Leakless(false).
		// Reduce easily detectable automation switches.
		// These flags avoid exposing Chrome's automation bits often probed by bot defenses.
		Headless(cfg.Browser.Headless).
		Set("disable-blink-features", "AutomationControlled").
		Set("disable-features", "IsolateOrigins,site-per-process").
		Set("disable-extensions").
		Set("disable-component-update").
		Set("disable-client-side-phishing-detection").
		Set("window-size", fmt.Sprintf("%d,%d", width, height)).
		Set("user-agent", ua).
		Launch()
	if err != nil {
		logr.Fatalf("launch browser: %v", err)
	}

	browser := rod.New().
		ControlURL(launchURL)
		// Don't set a global timeout - let individual operations handle their own timeouts

	if err := browser.Connect(); err != nil {
		logr.Fatalf("connect browser: %v", err)
	}
	defer func() {
		_ = browser.Close()
	}()

	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		logr.Fatalf("open page: %v", err)
	}
	defer func() {
		_ = page.Close()
	}()

	if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width:             width,
		Height:            height,
		DeviceScaleFactor: 1,
		Mobile:            false,
	}); err != nil {
		logr.Warnf("set viewport: %v", err)
	}

	if err := stealth.Apply(page); err != nil {
		logr.Fatalf("apply stealth: %v", err)
	}

	store := &storage.FileStore{BaseDir: "data"}
	if err := auth.LoginOrRestoreSession(ctx, browser, page, store, logr, cfg); err != nil {
		logr.Fatalw("auth failed", "error", err)
	}

	logr.Infow("browser ready",
		"userAgent", ua,
		"viewportWidth", width,
		"viewportHeight", height,
		"headless", cfg.Browser.Headless,
	)

	// Wait 5 seconds after successful login
	logr.Info("waiting 5 seconds before starting automation...")
	time.Sleep(5 * time.Second)

	// Create a custom navigation sequence with 5-second pauses
	logr.Info("starting tab navigation with 5-second intervals...")
	customTabs := []navigation.TabWithAction{
		{
			Tab:        navigation.TabFeed,
			ScrollTime: 10 * time.Second,
			PauseAfter: 5 * time.Second,
		},
		{
			Tab:        navigation.TabMyNetwork,
			ScrollTime: 0, // No scroll on My Network
			PauseAfter: 5 * time.Second,
		},
		{
			Tab:        navigation.TabJobs,
			ScrollTime: 0, // No scroll on Jobs
			PauseAfter: 5 * time.Second,
		},
		{
			Tab:        navigation.TabMessaging,
			ScrollTime: 0, // No scroll on Messaging
			PauseAfter: 5 * time.Second,
		},
		{
			Tab:        navigation.TabNotifications,
			ScrollTime: 5 * time.Second,
			PauseAfter: 5 * time.Second,
		},
		{
			Tab:        navigation.TabFeed,
			ScrollTime: 10 * time.Second,
			PauseAfter: 0, // Last one, no pause needed
		},
	}

	customPattern := navigation.NavigationPattern{
		Name:        "Custom 5-Second Interval",
		Description: "Navigate through tabs with 5-second pauses",
		Tabs:        customTabs,
	}

	if err := navigation.ExecutePattern(page, customPattern, cfg.Timing, logr); err != nil {
		logr.Warnw("navigation pattern failed", "error", err)
	} else {
		logr.Info("navigation pattern completed successfully")
	}

	// Execute people search after navigation
	logr.Info("🔍 starting LinkedIn people search...")
	profiles := runPeopleSearch(page, store, *cfg, logr)

	// Send connection requests if profiles were found
	if len(profiles) > 0 {
		logr.Info("🤝 starting connection request automation...")
		runConnectionRequests(page, store, profiles, *cfg, logr)
	} else {
		logr.Info("⏭️  skipping connection requests (no profiles found)")
	}

	// Send follow-up messages to accepted connections
	logr.Info("💬 starting follow-up messaging...")
	runFollowUpMessaging(page, store, profiles, *cfg, logr)

	<-ctx.Done()
	logr.Info("shutdown requested, exiting")
}

// runPeopleSearch executes a LinkedIn people search and saves results
func runPeopleSearch(page *rod.Page, store storage.StateStore, cfg config.Config, log *zap.SugaredLogger) []string {
	// Configure search parameters
	// Modify these parameters based on your search needs
	params := search.SearchParams{
		Keywords: "software engineer",
		Location: "India",
		MaxPages: 3, // Search first 3 pages (~30 profiles)
	}

	log.Infow("executing people search",
		"keywords", params.Keywords,
		"location", params.Location,
		"maxPages", params.MaxPages,
	)

	// Execute search
	profiles, err := search.FindPeople(
		context.Background(),
		page,
		store,
		params,
		cfg.Timing,
		log,
	)

	if err != nil {
		log.Errorf("❌ Search failed: %v", err)
		return nil
	}

	log.Infof("✅ Found %d profiles", len(profiles))

	// Save results to file
	filename := "data/search_results.txt"
	if err := saveSearchResults(profiles, filename); err != nil {
		log.Errorf("❌ Failed to save results: %v", err)
		return profiles
	}

	log.Infof("💾 Results saved to %s", filename)

	// Display first 10 results
	displayCount := 10
	if len(profiles) < displayCount {
		displayCount = len(profiles)
	}

	log.Infof("📋 First %d profiles:", displayCount)
	for i := 0; i < displayCount; i++ {
		log.Infof("  %d. %s", i+1, profiles[i])
	}

	if len(profiles) > displayCount {
		log.Infof("  ... and %d more (see %s)", len(profiles)-displayCount, filename)
	}

	return profiles
}

// runConnectionRequests sends connection requests to found profiles
func runConnectionRequests(page *rod.Page, store storage.StateStore, profiles []string, cfg config.Config, log *zap.SugaredLogger) {
	// Configure connection request settings
	// Start conservative to avoid LinkedIn restrictions
	connectCfg := connect.RequestConfig{
		MaxPerDay:            10,   // Conservative daily limit (10-20 is safe for most accounts)
		UsePersonalizedNotes: true, // Send requests with personalized notes using human-like typing
		NoteTemplate:         "Hi {{name}}, I came across your profile and would love to connect with you. Looking forward to staying in touch!",
		WaitBetweenRequests:  8000, // 8 seconds minimum wait between requests (longer for note typing)
	}

	log.Infow("connection request configuration",
		"maxPerDay", connectCfg.MaxPerDay,
		"usePersonalizedNotes", connectCfg.UsePersonalizedNotes,
		"waitBetweenRequests", connectCfg.WaitBetweenRequests,
	)

	// Execute connection requests
	err := connect.SendRequests(
		context.Background(),
		page,
		profiles,
		store,
		connectCfg,
		cfg.Timing,
		log,
	)

	if err != nil {
		log.Errorf("❌ Connection requests failed: %v", err)
		return
	}

	log.Info("✅ Connection request automation completed")
}

// saveSearchResults saves profile URLs to a text file
func saveSearchResults(profiles []string, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer file.Close()

	// Write header
	header := fmt.Sprintf("# LinkedIn People Search Results\n# Generated: %s\n# Total Profiles: %d\n\n",
		time.Now().Format("2006-01-02 15:04:05"),
		len(profiles),
	)
	if _, err := file.WriteString(header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	// Write profile URLs
	for i, url := range profiles {
		line := fmt.Sprintf("%d. %s\n", i+1, url)
		if _, err := file.WriteString(line); err != nil {
			return fmt.Errorf("write url: %w", err)
		}
	}

	return nil
}

func randomViewport(min, max int) (int, int) {
	if min <= 0 {
		min = 1024
	}
	if max <= min {
		max = min + 200
	}

	w := randomIntRange(min, max)
	// Keep a common desktop aspect ratio to avoid anomalous sizes.
	h := int(math.Round(float64(w) * 0.5625)) // ~16:9

	return w, h
}

func randomIntRange(min, max int) int {
	seed, _ := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	mathrand.Seed(seed.Int64())
	return mathrand.Intn(max-min+1) + min
}

func randomInt(limit int) int {
	if limit <= 1 {
		return 0
	}

	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		n := binary.BigEndian.Uint64(b[:])
		return int(n % uint64(limit))
	}

	mathrand.Seed(time.Now().UnixNano())
	return mathrand.Intn(limit)
}

// runFollowUpMessaging sends follow-up messages to accepted connections
func runFollowUpMessaging(page *rod.Page, store storage.StateStore, profiles []string, cfg config.Config, log *zap.SugaredLogger) {
	if len(profiles) == 0 {
		log.Info("⏭️  no profiles to message")
		return
	}

	// Configure messaging settings
	msgCfg := messaging.FollowUpConfig{
		MaxPerDay:           5,     // Conservative: 5 messages per day
		MessageTemplate:     "Hi {{name}}, thanks for connecting! I came across your profile and thought we might have some interesting synergies. Looking forward to staying in touch!",
		WaitBetweenMessages: 15000, // 15 seconds between messages
		Context:             "software engineering", // Context for {{context}} variable
	}

	log.Infow("follow-up messaging configuration",
		"maxPerDay", msgCfg.MaxPerDay,
		"waitBetweenMessages", msgCfg.WaitBetweenMessages,
	)

	// Execute follow-up messaging
	err := messaging.SendFollowUps(
		context.Background(),
		page,
		profiles,
		store,
		msgCfg,
		cfg.Timing,
		log,
	)

	if err != nil {
		log.Errorf("❌ Follow-up messaging failed: %v", err)
		return
	}

	log.Info("✅ Follow-up messaging completed")
}
