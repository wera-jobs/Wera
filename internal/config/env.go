package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"wera/internal/buildinfo"
	"wera/internal/mail"
)

// Env holds environment-derived configuration with the defaults from
// PLAN.md section 5.1. Real environment variables (or a local .env, loaded
// by the main package via godotenv) override every default.
type Env struct {
	DatabaseURL        string
	AI                 AI     // the language-model provider (see ai.go)
	TypstBin           string // typst binary for resume rendering ("typst" on PATH)
	ScoringConcurrency int
	FetchConcurrency   int
	RunInterval        time.Duration
	RunSchedule        *Schedule      // nil: loop every RunInterval
	RunZone            *time.Location // RUN_TIMEZONE: where a digest's day starts
	HTTPAddr           string
	LogFormat          string
	UserAgent          string
	MaxCostPerRunUSD   float64 // per user per run
	UserBudgetUSD      float64 // monthly budget given to new accounts
	MaxMonthlyCostUSD  float64 // all users together, per calendar month

	// Ranking and score limits. Jobs are ranked for each user by embedding
	// similarity (EMBED_BASE_URL, an OpenAI-compatible /embeddings
	// endpoint; empty falls back to the built-in TF-IDF ranking), and only
	// the best-ranked get a per-user AI score.
	EmbedBaseURL string
	EmbedModel   string
	EmbedAPIKey  string
	// EmbedQueryPrefix and EmbedDocPrefix are the instructions the model
	// expects before a search query (the profile) and a document (a
	// posting). Unset (nil), they follow the model: see embed.Prefixes.
	EmbedQueryPrefix     *string
	EmbedDocPrefix       *string
	ScoresPerDay         int // AI scores per user per rolling 24 hours; 0 = no limit
	ScoresPerRun         int // per scheduled run, after the first scores for a profile; 0 = no limit
	InactiveAfterDays    int // users unseen this long get InactiveScoresPerDay
	InactiveScoresPerDay int // enough for their digest; 0 when they get no digest

	// Accounts and browser security.
	SignupEnabled  bool     // POST /api/auth/signup open to anyone
	SignupsPerHour int      // sign-ups allowed per client IP per hour
	CookieSecure   bool     // set Secure on the session cookie (HTTPS only)
	PublicOrigins  []string // extra origins allowed to send state-changing requests
	TrustProxy     bool     // take the client IP from proxy headers

	// Email: off until MAIL_PROVIDER is set.
	Mail mail.Config
}

// LoadEnv builds an Env from the process environment with defaults.
func LoadEnv() (*Env, error) {
	e := &Env{ //nolint:gosec // DatabaseURL is a local development default; deployments set DATABASE_URL
		DatabaseURL:          "postgres://wera:wera@localhost:5433/wera?sslmode=disable",
		ScoringConcurrency:   12,
		FetchConcurrency:     6,
		RunInterval:          30 * time.Minute,
		HTTPAddr:             "127.0.0.1:8080",
		LogFormat:            "text",
		UserAgent:            "Wera/" + strings.TrimPrefix(buildinfo.Version, "v") + " (personal job tracker; contact: jessechumo@gmail.com)",
		MaxCostPerRunUSD:     1.00,
		UserBudgetUSD:        10,
		MaxMonthlyCostUSD:    100,
		EmbedModel:           "Snowflake/snowflake-arctic-embed-m-v1.5",
		ScoresPerDay:         100,
		ScoresPerRun:         30,
		InactiveAfterDays:    7,
		InactiveScoresPerDay: 10,
		SignupEnabled:        true,
		SignupsPerHour:       5,
	}

	var errs []string
	str := func(key string, dst *string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	intVal := func(key string, dst *int) {
		if v := os.Getenv(key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: not an integer: %q", key, v))
				return
			}
			*dst = n
		}
	}
	durVal := func(key string, dst *time.Duration) {
		if v := os.Getenv(key); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: not a duration: %q", key, v))
				return
			}
			*dst = d
		}
	}
	boolVal := func(key string, dst *bool) {
		if v := os.Getenv(key); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: not a boolean: %q", key, v))
				return
			}
			*dst = b
		}
	}
	floatVal := func(key string, dst *float64) {
		if v := os.Getenv(key); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: not a number: %q", key, v))
				return
			}
			*dst = f
		}
	}

	str("DATABASE_URL", &e.DatabaseURL)
	ai, aiErrs := loadAI()
	e.AI = ai
	errs = append(errs, aiErrs...)
	str("TYPST_BIN", &e.TypstBin)
	intVal("SCORING_CONCURRENCY", &e.ScoringConcurrency)
	intVal("FETCH_CONCURRENCY", &e.FetchConcurrency)
	durVal("RUN_INTERVAL", &e.RunInterval)
	tzName := "America/Chicago"
	str("RUN_TIMEZONE", &tzName)
	if loc, err := time.LoadLocation(tzName); err != nil {
		errs = append(errs, fmt.Sprintf("RUN_TIMEZONE: unknown time zone %q", tzName))
	} else if sched, err := ParseSchedule(os.Getenv("RUN_SCHEDULE"), loc); err != nil {
		errs = append(errs, "RUN_SCHEDULE: "+err.Error())
	} else {
		e.RunSchedule, e.RunZone = sched, loc
	}
	str("HTTP_ADDR", &e.HTTPAddr)
	str("LOG_FORMAT", &e.LogFormat)
	str("USER_AGENT", &e.UserAgent)
	floatVal("MAX_COST_PER_RUN_USD", &e.MaxCostPerRunUSD)
	floatVal("USER_MONTHLY_BUDGET_USD", &e.UserBudgetUSD)
	floatVal("MAX_MONTHLY_COST_USD", &e.MaxMonthlyCostUSD)
	str("EMBED_BASE_URL", &e.EmbedBaseURL)
	e.EmbedBaseURL = strings.TrimRight(e.EmbedBaseURL, "/")
	str("EMBED_MODEL", &e.EmbedModel)
	str("EMBED_API_KEY", &e.EmbedAPIKey)
	// Set but empty means "no prefix", so these read presence, not value.
	if v, ok := os.LookupEnv("EMBED_QUERY_PREFIX"); ok {
		e.EmbedQueryPrefix = &v
	}
	if v, ok := os.LookupEnv("EMBED_DOC_PREFIX"); ok {
		e.EmbedDocPrefix = &v
	}
	intVal("SCORES_PER_DAY", &e.ScoresPerDay)
	intVal("SCORES_PER_RUN", &e.ScoresPerRun)
	intVal("INACTIVE_AFTER_DAYS", &e.InactiveAfterDays)
	intVal("INACTIVE_SCORES_PER_DAY", &e.InactiveScoresPerDay)
	boolVal("SIGNUP_ENABLED", &e.SignupEnabled)
	intVal("SIGNUPS_PER_HOUR", &e.SignupsPerHour)
	boolVal("COOKIE_SECURE", &e.CookieSecure)
	boolVal("TRUST_PROXY", &e.TrustProxy)
	e.Mail.SMTPPort = 587
	str("MAIL_PROVIDER", &e.Mail.Provider)
	str("MAIL_FROM", &e.Mail.From)
	str("MAIL_REPLY_TO", &e.Mail.ReplyTo)
	str("MAIL_LOG_DIR", &e.Mail.LogDir)
	str("PUBLIC_URL", &e.Mail.PublicURL)
	e.Mail.PublicURL = strings.TrimRight(e.Mail.PublicURL, "/")
	str("SMTP_HOST", &e.Mail.SMTPHost)
	intVal("SMTP_PORT", &e.Mail.SMTPPort)
	str("SMTP_USERNAME", &e.Mail.SMTPUsername)
	str("SMTP_PASSWORD", &e.Mail.SMTPPassword)
	str("SMTP_SECURITY", &e.Mail.SMTPSecurity)
	if err := e.Mail.Validate(); err != nil {
		errs = append(errs, "email: "+err.Error())
	}
	for _, o := range strings.Split(os.Getenv("PUBLIC_ORIGINS"), ",") {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			e.PublicOrigins = append(e.PublicOrigins, o)
		}
	}

	if e.FetchConcurrency < 1 {
		errs = append(errs, "FETCH_CONCURRENCY must be >= 1")
	}
	if e.ScoringConcurrency < 1 {
		errs = append(errs, "SCORING_CONCURRENCY must be >= 1")
	}
	if e.RunInterval <= 0 {
		errs = append(errs, "RUN_INTERVAL must be positive")
	}
	if e.MaxCostPerRunUSD <= 0 {
		errs = append(errs, "MAX_COST_PER_RUN_USD must be positive")
	}
	if e.UserBudgetUSD < 0 || e.MaxMonthlyCostUSD <= 0 {
		errs = append(errs, "USER_MONTHLY_BUDGET_USD must be >= 0 and MAX_MONTHLY_COST_USD positive")
	}
	if e.ScoresPerDay < 0 || e.ScoresPerRun < 0 || e.InactiveScoresPerDay < 0 || e.InactiveAfterDays < 1 {
		errs = append(errs, "SCORES_PER_DAY, SCORES_PER_RUN and INACTIVE_SCORES_PER_DAY must be >= 0, INACTIVE_AFTER_DAYS >= 1")
	}
	if e.EmbedBaseURL != "" && !internalURL(e.EmbedBaseURL) {
		errs = append(errs, fmt.Sprintf("EMBED_BASE_URL must use https (or http to localhost or a container name), got %q", e.EmbedBaseURL))
	}
	if e.LogFormat != "text" && e.LogFormat != "json" {
		errs = append(errs, fmt.Sprintf("LOG_FORMAT must be text or json, got %q", e.LogFormat))
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid environment:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return e, nil
}

// NewLogger returns a slog logger using the configured format
// (text locally, JSON on the server).
func NewLogger(e *Env) *slog.Logger {
	if e.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, nil))
}

// secureURL accepts https URLs, and plain http only to this machine
// (a local mock). The API key and resumes must never cross a network in
// the clear.
func secureURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	h := u.Hostname()
	return u.Scheme == "http" && (h == "localhost" || h == "127.0.0.1" || h == "::1")
}

// internalURL is secureURL plus plain http to a single-label host: a
// container on the same Docker network (e.g. http://wera-embed), which
// never leaves the machine.
func internalURL(raw string) bool {
	if secureURL(raw) {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "http" && u.Hostname() != "" && !strings.Contains(u.Hostname(), ".")
}
