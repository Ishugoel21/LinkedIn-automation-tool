package stealth

import (
	"github.com/go-rod/rod"
)

// Apply injects evasive JS to approximate a real browser profile:
// - navigator.webdriver: hide automation flag used by many bot checks.
// - navigator.languages: populate with common language list to avoid empty arrays.
// - navigator.platform: set to a mainstream desktop platform.
// - navigator.plugins: fake plugin count because headless browsers report zero.
func Apply(page *rod.Page) error {
	script := `
	() => {
		Object.defineProperty(navigator, 'webdriver', { get: () => undefined });
		Object.defineProperty(navigator, 'languages', { get: () => ['en-US', 'en'] });
		Object.defineProperty(navigator, 'platform', { get: () => 'Win32' });
		Object.defineProperty(navigator, 'plugins', { 
			get: () => [
				{name: 'Chrome PDF Plugin'},
				{name: 'Chrome PDF Viewer'},
				{name: 'Native Client'}
			] 
		});
	}
	`

	_, err := page.Evaluate(rod.Eval(script))
	return err
}



