package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// Config holds all application configuration loaded from environment variables
// with the SVC_ prefix.
//
// OTel SDK env vars are NOT in this struct — they follow the OTel spec
// (OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_SERVICE_NAME, OTEL_TRACES_SAMPLER_ARG, …)
// and are read by `internal/infra/otelx.Setup` directly from os.Getenv.
type Config struct {
	HTTPPort        int                `mapstructure:"http_port"`
	HTTPTimeout     time.Duration      `mapstructure:"http_timeout"` // global per-request timeout
	HTTPThrottle    ThrottleConfig     `mapstructure:"http_throttle"`
	HTTPLog         HTTPLogConfig      `mapstructure:"http_log"`
	LogLevel        string             `mapstructure:"log_level"`
	LogFormat       string             `mapstructure:"log_format"`
	Env             string             `mapstructure:"env"`        // APP_ENV — surfaced as app_env in /healthcheck + OTel deployment.environment
	AppName         string             `mapstructure:"app_name"`   // APP_NAME, default "go-service-template" — surfaced in /healthcheck
	AppRegion       string             `mapstructure:"app_region"` // APP_REGION, default "ap-southeast-1" — surfaced in /healthcheck
	Mongo           MongoConfig        `mapstructure:"mongo"`
	Valkey          ValkeyConfig       `mapstructure:"valkey"`
	Kafka           KafkaConfig        `mapstructure:"kafka"`
	Auth            AuthConfig         `mapstructure:"auth"`
	Pagination      PaginationConfig   `mapstructure:"pagination"`
	Notification    NotificationConfig `mapstructure:"notification"`
	ShutdownTimeout time.Duration      `mapstructure:"shutdown_timeout"`
}

// PaginationConfig holds shared cursor-pagination knobs applied across every
// list endpoint in the service. MaxLimit is the operational ceiling on
// `?limit=` — each feature's Service rejects oversized requests with 400
// (handler validators only enforce min=1). Shared by design: ops changing
// the page-size policy in one place expect EVERY list endpoint to follow.
// See .claude/rules/http.md §11.
type PaginationConfig struct {
	MaxLimit int `mapstructure:"max_limit"` // SVC_PAGINATION_MAX_LIMIT, default 100
}

// AuthConfig holds the JWT secrets for platform/authtoken. Both secrets are
// required at boot (validate) because every public route is Bearer-authed.
type AuthConfig struct {
	JWTAccessSecret  string        `mapstructure:"jwt_access_secret"`  // SVC_AUTH_JWT_ACCESS_SECRET
	JWTRefreshSecret string        `mapstructure:"jwt_refresh_secret"` // SVC_AUTH_JWT_REFRESH_SECRET
	JWTAccessTTL     time.Duration `mapstructure:"jwt_access_ttl"`     // default 1h
	JWTRefreshTTL    time.Duration `mapstructure:"jwt_refresh_ttl"`    // default 720h (30d)
}

// NotificationConfig holds the notification feature's secrets.
//
// InternalAPISecret guards the account-deletion purge
// DELETE /api/v1/internal/notifications/users/{user_id} (Internal-Secret header,
// middleware.SharedSecret). Empty → the internal route is not mounted (404);
// a set-but-mismatched secret is 401. Feature-namespaced like challenger's
// CHALLENGER_LEADERBOARD_INTERNAL_API_SECRET: one secret per route family.
type NotificationConfig struct {
	InternalAPISecret string `mapstructure:"internal_api_secret"` // SVC_NOTIFICATION_INTERNAL_API_SECRET; empty disables the route
}

// minInternalSecretLen is the shortest accepted internal secret: it authorises a hard delete.
const minInternalSecretLen = 32

// ThrottleConfig bounds concurrent in-flight requests through /api/v1/*.
// Health endpoints (/healthcheck, /liveness, /readiness) are NOT throttled.
//
// The rule: Max ≤ min(downstream pool sizes). Overflow goes to Backlog (FIFO
// wait); requests waiting longer than BacklogTimeout return 503 + Retry-After.
// Set Max=0 to disable the throttle entirely (NOT recommended for prod).
type ThrottleConfig struct {
	Max            int           `mapstructure:"max"`             // concurrent in-flight; 0 disables
	Backlog        int           `mapstructure:"backlog"`         // queued requests above Max
	BacklogTimeout time.Duration `mapstructure:"backlog_timeout"` // max wait in backlog before 503
}

// HTTPLogConfig opts the access log into capturing request/response payloads.
//
// .claude/rules/logging.md forbids logging bodies and headers by default — this
// is the controlled exception, and every field below is a safety rail rather
// than a convenience:
//
//   - Bodies + query capture are ON by default (deliberate deviation from the
//     off-by-default posture logging.md §6 describes — see Load's SetDefault
//     block). Headers/QueryDeny ship with a starter allowlist/denylist; set
//     SVC_HTTP_LOG_BODIES/_QUERY=false to turn capture off entirely.
//   - Bodies are captured only for application/json. Anything else (multipart
//     uploads, binary) is recorded as a size, never as content.
//   - Captured JSON is redacted key-by-key before it reaches a log record, so
//     a payload gaining a `password` field later is covered automatically.
//   - Headers are an ALLOWLIST: Authorization and Cookie are one typo away from
//     a credential in the log, so each header is opted in by name.
//   - Query params are the opposite — one switch logs them all, and QueryDeny
//     removes specific keys. Debugging usually means looking at a param you
//     did not anticipate, so pre-registering each one is friction with little
//     safety gain: query values are redacted by the same key heuristic as
//     bodies, so `?token=` is hidden whether or not anyone denied it.
//
// Bodies are emitted as NESTED OBJECTS so a backend can filter on sub-fields
// (`request_body.type = "email"`). That rules out a byte cap — cutting an
// object at N bytes yields invalid JSON — so size is bounded structurally by
// MaxValueLen and MaxFields instead. Both matter: MaxValueLen stops one long
// string, MaxFields stops a wide or deep payload.
//
// If a deploy needs capture off (log volume, redaction blast radius), set
// SVC_HTTP_LOG_BODIES=false / _QUERY=false explicitly for that env.
type HTTPLogConfig struct {
	Bodies      bool     `mapstructure:"bodies"`        // capture request+response JSON bodies
	Query       bool     `mapstructure:"query"`         // log every query param
	QueryDeny   []string `mapstructure:"query_deny"`    // query-param names to drop entirely (denylist)
	MaxValueLen int      `mapstructure:"max_value_len"` // longest string value kept inside a captured body
	MaxFields   int      `mapstructure:"max_fields"`    // total nodes kept per captured body
	Headers     []string `mapstructure:"headers"`       // request header names to log (allowlist, case-insensitive)
}

// MongoConfig holds MongoDB connection settings. Pool sizing uses the driver
// default (MaxPoolSize=100) — add explicit knobs when production metrics show
// saturation. See .claude/rules/routing.md §5 for the "when to tune" criteria.
type MongoConfig struct {
	URI         string        `mapstructure:"uri"`
	Database    string        `mapstructure:"database"`
	PingTimeout time.Duration `mapstructure:"ping_timeout"`
}

// ValkeyConfig holds Valkey (Redis-compatible) connection settings. Pool
// sizing uses the go-redis default (PoolSize=10×GOMAXPROCS) — add explicit
// knobs when production metrics show saturation. See .claude/rules/routing.md §5.
type ValkeyConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

// KafkaConfig holds Kafka cluster settings. Brokers is optional — when empty,
// the producer/consumer wiring is skipped at boot.
//
// TopicPrefix is the per-environment namespace the everfit MSK bus prepends to
// every topic (`<prefix>-<topic>`, set by the producers' AWS_MSK_PREFIX). It
// MUST match the target cluster's value (dev / staging / prod) or the consumer
// subscribes to a topic no producer writes to and silently receives nothing.
// Empty = no prefix, for single-tenant / local brokers that don't namespace.
type KafkaConfig struct {
	Brokers     []string `mapstructure:"brokers"`
	TopicPrefix string   `mapstructure:"topic_prefix"` // SVC_KAFKA_TOPIC_PREFIX; matches the bus's AWS_MSK_PREFIX
}

// Load reads configuration from the environment (prefix SVC_) and from
// a .env file when present. It fails fast on missing required fields.
func Load() (*Config, error) {
	v := viper.New()

	v.SetEnvPrefix("SVC")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Local .env file — silently ignored when absent, propagate other errors.
	v.SetConfigFile(".env")
	v.SetConfigType("env")
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config: read .env: %w", err)
		}
	}

	// Defaults.
	v.SetDefault("http_port", 7991)
	v.SetDefault("log_level", "info")
	v.SetDefault("log_format", "json")
	v.SetDefault("env", "local")
	v.SetDefault("app_name", "go-service-template") // APP_NAME — identity surfaced in /healthcheck; override per deploy (e.g. <service>-api-stg)
	v.SetDefault("app_region", "ap-southeast-1")    // APP_REGION — deploy region surfaced in /healthcheck
	v.SetDefault("mongo.database", "service")
	v.SetDefault("mongo.ping_timeout", "10s") // Atlas cold connect needs ~5–10s for SRV + TLS + RS discovery
	v.SetDefault("valkey.password", "")
	v.SetDefault("valkey.db", 0)
	v.SetDefault("kafka.brokers", []string{})
	v.SetDefault("kafka.topic_prefix", "dev") // matches the everfit MSK bus AWS_MSK_PREFIX default; override per environment
	v.SetDefault("http_timeout", "30s")
	v.SetDefault("http_throttle.max", 40)     // ≤ min(pool sizes); overflow → backlog → 503
	v.SetDefault("http_throttle.backlog", 80) // queue ~2× Max so transient bursts ride out
	v.SetDefault("http_throttle.backlog_timeout", "2s")

	// Capture is ON by default in every environment (deliberate deviation from
	// the original off-by-default posture — see CLAUDE.md's closing line).
	// Redaction only recognises credential-shaped key names, so an ON default
	// puts ordinary PII (names, addresses) into logs for any payload nobody
	// thought about; QueryDeny/Headers below narrow the blast radius to the
	// known-sensitive keys and the handful of headers actually useful for
	// debugging. Override per environment via SVC_HTTP_LOG_BODIES/_QUERY
	// when a deploy needs it off. Both limits always have values so a caller
	// who flips Bodies alone still gets bounded output.
	v.SetDefault("http_log.bodies", true)
	v.SetDefault("http_log.max_value_len", 512)
	v.SetDefault("http_log.max_fields", 200)
	v.SetDefault("http_log.query", true)
	v.SetDefault("http_log.query_deny", []string{"secret", "sig", "signature"})
	v.SetDefault("http_log.headers", []string{"Content-Type", "Accept", "User-Agent"})
	v.SetDefault("shutdown_timeout", "15s")
	v.SetDefault("auth.jwt_access_ttl", "1h")
	v.SetDefault("auth.jwt_refresh_ttl", "720h")
	v.SetDefault("pagination.max_limit", 100) // shared `?limit=` ceiling for every list endpoint; per-feature Service rejects oversized with 400

	// Explicit env binds for keys without defaults. viper.AutomaticEnv only
	// queries keys it knows about (via SetDefault, ReadInConfig, or BindEnv) at
	// Unmarshal time — so required-but-no-default fields need explicit binding.
	for _, b := range [][2]string{
		// Platform-standard identity vars injected by the deploy template
		// (unprefixed). Bound alongside the SVC_* defaults so /healthcheck
		// reports the real per-deploy name/env/region. Note APP_ENV maps to `env`.
		{"app_name", "APP_NAME"},
		{"app_region", "APP_REGION"},
		{"env", "APP_ENV"},
		{"mongo.uri", "SVC_MONGO_URI"},
		{"valkey.addr", "SVC_VALKEY_ADDR"},
		{"auth.jwt_access_secret", "SVC_AUTH_JWT_ACCESS_SECRET"},
		{"auth.jwt_refresh_secret", "SVC_AUTH_JWT_REFRESH_SECRET"},
		{"notification.internal_api_secret", "SVC_NOTIFICATION_INTERNAL_API_SECRET"},
	} {
		if err := v.BindEnv(b[0], b[1]); err != nil {
			return nil, fmt.Errorf("config: bind env %s: %w", b[1], err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg, viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(
		mapstructure.StringToTimeDurationHookFunc(), // viper default, restated because DecodeHook replaces
		mapstructure.StringToSliceHookFunc(","),     // splits "a,b,c" → []string{"a","b","c"} for SVC_KAFKA_BROKERS
	))); err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func validate(cfg *Config) error {
	if cfg.HTTPPort < 1 || cfg.HTTPPort > 65535 {
		return fmt.Errorf("config: SVC_HTTP_PORT must be between 1 and 65535")
	}
	if cfg.Mongo.URI == "" {
		return fmt.Errorf("config: SVC_MONGO_URI is required")
	}
	if cfg.Valkey.Addr == "" {
		return fmt.Errorf("config: SVC_VALKEY_ADDR is required")
	}
	// Every route is Bearer-authed; missing secrets would otherwise surface as 404s.
	if cfg.Auth.JWTAccessSecret == "" || cfg.Auth.JWTRefreshSecret == "" {
		return fmt.Errorf("config: SVC_AUTH_JWT_ACCESS_SECRET and SVC_AUTH_JWT_REFRESH_SECRET are required")
	}
	if s := cfg.Notification.InternalAPISecret; s != "" && len(s) < minInternalSecretLen {
		return fmt.Errorf("config: SVC_NOTIFICATION_INTERNAL_API_SECRET must be ≥ %d bytes when set (got %d)", minInternalSecretLen, len(s))
	}
	if cfg.HTTPTimeout <= 0 {
		return fmt.Errorf("config: SVC_HTTP_TIMEOUT must be positive (e.g. 30s)")
	}
	// Kafka brokers are optional — empty = Kafka wiring is skipped in app bootstrap.

	if err := validatePayloadCapture(cfg); err != nil {
		return err
	}
	if cfg.Pagination.MaxLimit < 1 {
		return fmt.Errorf("config: SVC_PAGINATION_MAX_LIMIT must be ≥ 1 (got %d)", cfg.Pagination.MaxLimit)
	}
	return nil
}

// validatePayloadCapture guards the two structural bounds on captured bodies.
// Both only matter when capture is ON; an uncapped capture would let a single
// oversized request flood the log pipeline, so refuse to boot rather than
// silently emitting unbounded payloads.
func validatePayloadCapture(cfg *Config) error {
	if !cfg.HTTPLog.Bodies {
		return nil
	}
	if cfg.HTTPLog.MaxValueLen < 1 {
		return fmt.Errorf("config: SVC_HTTP_LOG_MAX_VALUE_LEN must be ≥ 1 when body capture is on (got %d)", cfg.HTTPLog.MaxValueLen)
	}
	if cfg.HTTPLog.MaxFields < 1 {
		return fmt.Errorf("config: SVC_HTTP_LOG_MAX_FIELDS must be ≥ 1 when body capture is on (got %d)", cfg.HTTPLog.MaxFields)
	}
	return nil
}
