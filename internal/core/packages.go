package core

// Package is a curated Composer package offered by the new-project wizard.
type Package struct {
	Name        string     `json:"name"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Category    string     `json:"category"`
	Dev         bool       `json:"dev"`
	Recommended bool       `json:"recommended,omitempty"`
	Needs       string     `json:"needs,omitempty"`   // "redis", "meilisearch"…
	Ext         []string   `json:"ext,omitempty"`     // PHP extensions it can't run without
	Version     string     `json:"version,omitempty"` // constraint, so a missing requirement fails instead of picking an ancient release
	Post        [][]string `json:"-"`                 // artisan commands run after install
}

// Categories in display order.
var PackageCategories = []string{"AI", "Admin & UI", "Auth & API", "Debug & Quality", "Queues & Realtime", "Search & Payments", "Files & Data"}

// Packages is the wizard's curated catalog. Anything else can be found
// with the live Packagist search.
var Packages = []Package{
	// AI
	{Name: "laravel/boost", Title: "Laravel Boost", Category: "AI", Dev: true, Recommended: true,
		Description: "Gives AI coding agents (Claude Code, Cursor, Copilot…) Laravel guidelines, docs search and an MCP server for your app.",
		Post:        [][]string{{"boost:install", "--no-interaction"}}},
	{Name: "laravel/ai", Title: "Laravel AI SDK", Category: "AI",
		Description: "First-party SDK for building AI features: agents, text, images, embeddings across providers."},
	{Name: "laravel/mcp", Title: "Laravel MCP", Category: "AI",
		Description: "Expose your app's tools and data to AI clients through the Model Context Protocol."},
	{Name: "prism-php/prism", Title: "Prism", Category: "AI",
		Description: "One fluent API for OpenAI, Anthropic, Gemini, Ollama and more."},
	{Name: "openai-php/laravel", Title: "OpenAI for Laravel", Category: "AI",
		Description: "Facade and config for the OpenAI API.", Post: [][]string{{"openai:install", "--no-interaction"}}},

	// Admin & UI
	{Name: "filament/filament", Title: "Filament", Category: "Admin & UI", Recommended: true, Ext: []string{"intl"}, Version: "^5.0",
		Description: "Admin panels, forms and tables built on Livewire.",
		Post:        [][]string{{"filament:install", "--panels", "--no-interaction"}}},
	{Name: "livewire/livewire", Title: "Livewire", Category: "Admin & UI",
		Description: "Dynamic interfaces without leaving PHP."},
	{Name: "inertiajs/inertia-laravel", Title: "Inertia", Category: "Admin & UI",
		Description: "Build single-page apps with Vue or React and server-side routing."},

	// Auth & API
	{Name: "laravel/sanctum", Title: "Sanctum (API)", Category: "Auth & API", Recommended: true,
		Description: "API tokens and SPA authentication; also sets up routes/api.php.",
		Post:        [][]string{{"install:api", "--no-interaction"}}},
	{Name: "laravel/socialite", Title: "Socialite", Category: "Auth & API",
		Description: "OAuth login with GitHub, Google, Facebook and more."},
	{Name: "laravel/fortify", Title: "Fortify", Category: "Auth & API",
		Description: "Headless authentication backend (login, 2FA, password reset).", Post: [][]string{{"fortify:install", "--no-interaction"}}},
	{Name: "spatie/laravel-permission", Title: "Permissions", Category: "Auth & API",
		Description: "Roles and permissions stored in the database.",
		Post:        [][]string{{"vendor:publish", "--provider=Spatie\\Permission\\PermissionServiceProvider"}}},

	// Debug & Quality
	{Name: "barryvdh/laravel-debugbar", Title: "Debugbar", Category: "Debug & Quality", Dev: true, Recommended: true,
		Description: "Queries, timing and views in a toolbar on every page."},
	{Name: "laravel/telescope", Title: "Telescope", Category: "Debug & Quality",
		Description: "Inspect requests, jobs, mail, queries and exceptions.", Post: [][]string{{"telescope:install"}}},
	{Name: "pestphp/pest", Title: "Pest", Category: "Debug & Quality", Dev: true,
		Description: "Elegant testing framework (replaces PHPUnit syntax, runs the same tests)."},
	{Name: "larastan/larastan", Title: "Larastan", Category: "Debug & Quality", Dev: true,
		Description: "Static analysis that understands Laravel."},
	{Name: "barryvdh/laravel-ide-helper", Title: "IDE Helper", Category: "Debug & Quality", Dev: true,
		Description: "Accurate autocompletion for facades and models."},
	{Name: "laravel/nightwatch", Title: "Nightwatch", Category: "Debug & Quality",
		Description: "Laravel's hosted monitoring agent."},

	// Queues & Realtime
	{Name: "laravel/horizon", Title: "Horizon", Category: "Queues & Realtime", Needs: "redis",
		Description: "Dashboard and supervisor for Redis queues.", Post: [][]string{{"horizon:install"}}},
	{Name: "laravel/reverb", Title: "Reverb", Category: "Queues & Realtime",
		Description: "First-party WebSocket server for broadcasting."},
	{Name: "laravel/pulse", Title: "Pulse", Category: "Queues & Realtime",
		Description: "Real-time performance dashboard.",
		Post:        [][]string{{"vendor:publish", "--provider=Laravel\\Pulse\\PulseServiceProvider"}}},
	{Name: "laravel/octane", Title: "Octane", Category: "Queues & Realtime",
		Description: "Serve the app with FrankenPHP, Swoole or RoadRunner."},

	// Search & Payments
	{Name: "laravel/scout", Title: "Scout", Category: "Search & Payments", Needs: "meilisearch",
		Description: "Full-text search for Eloquent; wired to Meilisearch when installed."},
	{Name: "laravel/cashier", Title: "Cashier (Stripe)", Category: "Search & Payments",
		Description: "Subscriptions and billing with Stripe."},
	{Name: "laravel/pennant", Title: "Pennant", Category: "Search & Payments",
		Description: "Feature flags."},

	// Files & Data
	{Name: "spatie/laravel-medialibrary", Title: "Media Library", Category: "Files & Data",
		Description: "Attach files and images to models, with conversions."},
	{Name: "spatie/laravel-backup", Title: "Backup", Category: "Files & Data",
		Description: "Back up files and databases."},
	{Name: "spatie/laravel-activitylog", Title: "Activity Log", Category: "Files & Data",
		Description: "Log model changes and user activity."},
	{Name: "spatie/laravel-data", Title: "Laravel Data", Category: "Files & Data",
		Description: "Typed data objects for requests, resources and more."},
	{Name: "maatwebsite/excel", Title: "Laravel Excel", Category: "Files & Data",
		Description: "Import and export Excel and CSV."},
	{Name: "barryvdh/laravel-dompdf", Title: "DomPDF", Category: "Files & Data",
		Description: "Render views to PDF."},
	{Name: "intervention/image-laravel", Title: "Intervention Image", Category: "Files & Data",
		Description: "Resize, crop and convert images."},
}

func findPackage(name string) (Package, bool) {
	for _, p := range Packages {
		if p.Name == name {
			return p, true
		}
	}
	return Package{}, false
}

// Starter kits for new projects: label, composer package (empty = blank).
type StarterKit struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Package     string `json:"package"`
	Frontend    bool   `json:"frontend"` // needs npm install + build
}

var StarterKits = []StarterKit{
	{ID: "laravel", Title: "Laravel", Description: "The standard Laravel application.", Package: "laravel/laravel", Frontend: true},
	{ID: "react", Title: "React starter kit", Description: "Inertia + React + TypeScript with auth screens.", Package: "laravel/react-starter-kit", Frontend: true},
	{ID: "vue", Title: "Vue starter kit", Description: "Inertia + Vue + TypeScript with auth screens.", Package: "laravel/vue-starter-kit", Frontend: true},
	{ID: "livewire", Title: "Livewire starter kit", Description: "Livewire + Flux UI with auth screens.", Package: "laravel/livewire-starter-kit", Frontend: true},
	{ID: "blank", Title: "Blank PHP", Description: "An empty public/index.php, nothing else."},
}

func findKit(id string) (StarterKit, bool) {
	for _, k := range StarterKits {
		if k.ID == id {
			return k, true
		}
	}
	return StarterKit{}, false
}
