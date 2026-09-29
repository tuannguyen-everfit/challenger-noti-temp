package config

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// setEnv clears every SVC_* var and the unprefixed APP_NAME/APP_ENV/APP_REGION
// identity vars inherited from the shell (mise leaks them in dev), then applies
// the test's overrides. t.Setenv auto-restores the
// originals after the test.
var identityEnvVars = []string{"APP_NAME", "APP_ENV", "APP_REGION"}

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, e := range os.Environ() {
		k, _, ok := strings.Cut(e, "=")
		if !ok || (!strings.HasPrefix(k, "SVC_") && !slices.Contains(identityEnvVars, k)) {
			continue
		}
		if _, override := kv[k]; override {
			continue
		}
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

// validBaseEnv is the minimum a deploy must set for Load to succeed.
func validBaseEnv() map[string]string {
	return map[string]string{
		"SVC_MONGO_URI":               "mongodb://localhost:27017/?replicaSet=rs0",
		"SVC_VALKEY_ADDR":             "localhost:6379",
		"SVC_AUTH_JWT_ACCESS_SECRET":  "access",
		"SVC_AUTH_JWT_REFRESH_SECRET": "refresh",
		"SVC_CHALLENGER_GRPC_ADDR":    "dns:///challenger-internal-grpc:7992",
		"SVC_CHALLENGER_GRPC_SECRET":  "grpc-secret",
	}
}

func TestLoad_Success_AllDefaultsApplied(t *testing.T) {
	setEnv(t, validBaseEnv())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if cfg.HTTPPort != 7991 {
		t.Errorf("HTTPPort = %d, want 7991", cfg.HTTPPort)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.Env != "local" {
		t.Errorf("Env = %q, want local", cfg.Env)
	}
	if cfg.Mongo.Database != "service" {
		t.Errorf("Mongo.Database = %q, want service", cfg.Mongo.Database)
	}
	if cfg.ShutdownTimeout.Seconds() != 15 {
		t.Errorf("ShutdownTimeout = %v, want 15s", cfg.ShutdownTimeout)
	}
	if len(cfg.Kafka.Brokers) != 0 {
		t.Errorf("Kafka.Brokers default = %v, want empty", cfg.Kafka.Brokers)
	}

	// Edge-throttle defaults — see .claude/rules/routing.md for the math.
	if cfg.HTTPThrottle.Max != 40 {
		t.Errorf("HTTPThrottle.Max = %d, want 40", cfg.HTTPThrottle.Max)
	}
	if cfg.HTTPThrottle.Backlog != 80 {
		t.Errorf("HTTPThrottle.Backlog = %d, want 80", cfg.HTTPThrottle.Backlog)
	}
	if cfg.HTTPThrottle.BacklogTimeout != 2*time.Second {
		t.Errorf("HTTPThrottle.BacklogTimeout = %v, want 2s", cfg.HTTPThrottle.BacklogTimeout)
	}
}

func TestLoad_Missing_MongoURI(t *testing.T) {
	// Explicitly clear MONGO_URI in addition to setting VALKEY_ADDR — `make
	// coverage-integration-gate` exports SVC_MONGO_URI for the
	// integration tests, and t.Setenv only OVERRIDES, doesn't unset; an
	// inherited value would leak into this test otherwise.
	setEnv(t, map[string]string{
		"SVC_MONGO_URI":   "",
		"SVC_VALKEY_ADDR": "localhost:6379",
	})
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "MONGO_URI") {
		t.Errorf("err = %v, want one mentioning MONGO_URI", err)
	}
}

func TestLoad_Missing_ValkeyAddr(t *testing.T) {
	setEnv(t, map[string]string{
		"SVC_MONGO_URI":   "mongodb://localhost:27017/?replicaSet=rs0",
		"SVC_VALKEY_ADDR": "", // see TestLoad_Missing_MongoURI for the why
	})
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "VALKEY_ADDR") {
		t.Errorf("err = %v, want one mentioning VALKEY_ADDR", err)
	}
}

func TestLoad_InvalidHTTPPort(t *testing.T) {
	env := validBaseEnv()
	env["SVC_HTTP_PORT"] = "0"
	setEnv(t, env)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "HTTP_PORT") {
		t.Errorf("err = %v, want one mentioning HTTP_PORT", err)
	}
}

func TestLoad_PortOverride(t *testing.T) {
	env := validBaseEnv()
	env["SVC_HTTP_PORT"] = "9090"
	setEnv(t, env)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if cfg.HTTPPort != 9090 {
		t.Errorf("HTTPPort = %d, want 9090", cfg.HTTPPort)
	}
}

func TestLoad_LogLevelOverride(t *testing.T) {
	env := validBaseEnv()
	env["SVC_LOG_LEVEL"] = "debug"
	setEnv(t, env)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}

func TestLoad_Auth_JWTTTLDefaults(t *testing.T) {
	setEnv(t, validBaseEnv())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if cfg.Auth.JWTAccessTTL != time.Hour {
		t.Errorf("JWTAccessTTL = %v, want 1h", cfg.Auth.JWTAccessTTL)
	}
	if cfg.Auth.JWTRefreshTTL != 720*time.Hour {
		t.Errorf("JWTRefreshTTL = %v, want 720h", cfg.Auth.JWTRefreshTTL)
	}
}

func TestLoad_Auth_JWTSecretsFromEnv(t *testing.T) {
	env := validBaseEnv()
	env["SVC_AUTH_JWT_ACCESS_SECRET"] = "access-from-env"
	env["SVC_AUTH_JWT_REFRESH_SECRET"] = "refresh-from-env"
	setEnv(t, env)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if cfg.Auth.JWTAccessSecret != "access-from-env" || cfg.Auth.JWTRefreshSecret != "refresh-from-env" {
		t.Errorf("JWT secrets = %q / %q", cfg.Auth.JWTAccessSecret, cfg.Auth.JWTRefreshSecret)
	}
}

func TestLoad_Kafka_CommaSplitsBrokers(t *testing.T) {
	env := validBaseEnv()
	env["SVC_KAFKA_BROKERS"] = "localhost:9095,localhost:9096,localhost:9097"
	setEnv(t, env)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if len(cfg.Kafka.Brokers) != 3 {
		t.Fatalf("brokers = %v, want 3", cfg.Kafka.Brokers)
	}
	if cfg.Kafka.Brokers[0] != "localhost:9095" {
		t.Errorf("brokers[0] = %q", cfg.Kafka.Brokers[0])
	}
}

// TestValidate_Guards covers the remaining validate() branches as a table. Each
// row is a value that would otherwise fail late and confusingly at runtime; the
// point of validate() is that they fail loudly at boot instead.
func TestValidate_Guards(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"http timeout must be positive", map[string]string{"SVC_HTTP_TIMEOUT": "0s"}, "SVC_HTTP_TIMEOUT"},
		{"pagination limit must be >= 1", map[string]string{"SVC_PAGINATION_MAX_LIMIT": "0"}, "SVC_PAGINATION_MAX_LIMIT"},
		{"jwt access secret required", map[string]string{"SVC_AUTH_JWT_ACCESS_SECRET": ""}, "SVC_AUTH_JWT_ACCESS_SECRET"},
		{"jwt refresh secret required", map[string]string{"SVC_AUTH_JWT_REFRESH_SECRET": ""}, "SVC_AUTH_JWT_REFRESH_SECRET"},
		{"internal secret too short", map[string]string{"SVC_NOTIFICATION_INTERNAL_API_SECRET": "short"}, "SVC_NOTIFICATION_INTERNAL_API_SECRET"},
		{"challenger addr required", map[string]string{"SVC_CHALLENGER_GRPC_ADDR": ""}, "SVC_CHALLENGER_GRPC_ADDR"},
		{"challenger secret required", map[string]string{"SVC_CHALLENGER_GRPC_SECRET": ""}, "SVC_CHALLENGER_GRPC_SECRET"},
		{"challenger call timeout positive", map[string]string{"SVC_CHALLENGER_GRPC_CALL_TIMEOUT": "0s"}, "SVC_CHALLENGER_GRPC_CALL_TIMEOUT"},
		{"challenger attempts >= 1", map[string]string{"SVC_CHALLENGER_GRPC_MAX_ATTEMPTS": "0"}, "SVC_CHALLENGER_GRPC_MAX_ATTEMPTS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validBaseEnv()
			for k, v := range tt.env {
				env[k] = v
			}
			setEnv(t, env)
			_, err := Load()
			if err == nil {
				t.Fatal("Load succeeded; want a boot failure")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestLoad_HTTPLogCapture_DefaultsOn(t *testing.T) {
	setEnv(t, validBaseEnv())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.HTTPLog.Bodies {
		t.Error("body capture must default to ON")
	}
	if !cfg.HTTPLog.Query {
		t.Error("query logging must default to ON")
	}
	if cfg.HTTPLog.MaxValueLen != 512 || cfg.HTTPLog.MaxFields != 200 {
		t.Errorf("limits = %d/%d, want 512/200", cfg.HTTPLog.MaxValueLen, cfg.HTTPLog.MaxFields)
	}
	wantHeaders := []string{"Content-Type", "Accept", "User-Agent"}
	if !slices.Equal(cfg.HTTPLog.Headers, wantHeaders) {
		t.Errorf("headers = %#v, want %#v", cfg.HTTPLog.Headers, wantHeaders)
	}
	wantQueryDeny := []string{"secret", "sig", "signature"}
	if !slices.Equal(cfg.HTTPLog.QueryDeny, wantQueryDeny) {
		t.Errorf("query_deny = %#v, want %#v", cfg.HTTPLog.QueryDeny, wantQueryDeny)
	}
}

func TestLoad_HTTPLogCapture_EnvOverride(t *testing.T) {
	env := validBaseEnv()
	env["SVC_HTTP_LOG_BODIES"] = "false"
	env["SVC_HTTP_LOG_MAX_VALUE_LEN"] = "2048"
	env["SVC_HTTP_LOG_HEADERS"] = "Content-Type,Accept"
	env["SVC_HTTP_LOG_QUERY"] = "false"
	env["SVC_HTTP_LOG_QUERY_DENY"] = "secret,sig"
	setEnv(t, env)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPLog.Bodies || cfg.HTTPLog.MaxValueLen != 2048 {
		t.Errorf("bodies=%v max_value_len=%d", cfg.HTTPLog.Bodies, cfg.HTTPLog.MaxValueLen)
	}
	if len(cfg.HTTPLog.Headers) != 2 || cfg.HTTPLog.Headers[0] != "Content-Type" {
		t.Errorf("headers = %#v, want [Content-Type Accept]", cfg.HTTPLog.Headers)
	}
	if cfg.HTTPLog.Query {
		t.Error("SVC_HTTP_LOG_QUERY=false not picked up")
	}
	if len(cfg.HTTPLog.QueryDeny) != 2 || cfg.HTTPLog.QueryDeny[0] != "secret" {
		t.Errorf("query_deny = %#v, want [secret sig]", cfg.HTTPLog.QueryDeny)
	}
}

func TestLoad_HTTPLogCapture_RejectsUncappedBodies(t *testing.T) {
	env := validBaseEnv()
	env["SVC_HTTP_LOG_BODIES"] = "true"
	env["SVC_HTTP_LOG_MAX_VALUE_LEN"] = "0"
	setEnv(t, env)

	if _, err := Load(); err == nil {
		t.Fatal("want a boot failure when body capture has no value cap")
	}

	t.Setenv("SVC_HTTP_LOG_MAX_VALUE_LEN", "512")
	t.Setenv("SVC_HTTP_LOG_MAX_FIELDS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("want a boot failure when body capture has no field cap")
	}
}

func TestLoad_Notification_InternalSecret(t *testing.T) {
	setEnv(t, validBaseEnv())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if cfg.Notification.InternalAPISecret != "" {
		t.Errorf("default secret = %q, want empty (internal route off)", cfg.Notification.InternalAPISecret)
	}

	env := validBaseEnv()
	env["SVC_NOTIFICATION_INTERNAL_API_SECRET"] = strings.Repeat("s", minInternalSecretLen)
	setEnv(t, env)
	if cfg, err = Load(); err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if cfg.Notification.InternalAPISecret != env["SVC_NOTIFICATION_INTERNAL_API_SECRET"] {
		t.Errorf("secret = %q, want the env value", cfg.Notification.InternalAPISecret)
	}
}

func TestLoad_Challenger(t *testing.T) {
	env := validBaseEnv()
	setEnv(t, env)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load err: %v", err)
	}
	want := ChallengerConfig{GRPCAddr: env["SVC_CHALLENGER_GRPC_ADDR"], GRPCSecret: env["SVC_CHALLENGER_GRPC_SECRET"], GRPCCallTimeout: 2 * time.Second, GRPCMaxAttempts: 3}
	if cfg.Challenger != want {
		t.Errorf("challenger = %+v, want %+v", cfg.Challenger, want)
	}

	env["SVC_CHALLENGER_GRPC_CALL_TIMEOUT"] = "500ms"
	env["SVC_CHALLENGER_GRPC_MAX_ATTEMPTS"] = "5"
	setEnv(t, env)
	if cfg, err = Load(); err != nil {
		t.Fatalf("Load err: %v", err)
	}
	if cfg.Challenger.GRPCCallTimeout != 500*time.Millisecond || cfg.Challenger.GRPCMaxAttempts != 5 {
		t.Errorf("overrides = %+v", cfg.Challenger)
	}
}
