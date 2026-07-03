package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewDoltServerManagerNormalizesManagedEndpointFromTownConfig(t *testing.T) {
	townRoot := t.TempDir()
	writeManagedDoltConfig(t, townRoot, "listener:\n  host: 127.0.0.2\n  port: 5507\n")
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "")
	t.Setenv("GT_DOLT_HOST", "stale-env-host")
	t.Setenv("GT_DOLT_PORT", "9999")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "stale-beads-host")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "9999")
	t.Setenv("BEADS_DOLT_PORT", "9999")

	cfg := &DoltServerConfig{
		Enabled:              true,
		External:             true,
		Host:                 "stale-daemon-host",
		Port:                 9999,
		User:                 "root",
		Password:             "secret",
		DataDir:              filepath.Join(townRoot, "custom-data"),
		LogFile:              filepath.Join(townRoot, "custom.log"),
		AutoRestart:          true,
		RestartDelay:         2 * time.Second,
		MaxRestartDelay:      3 * time.Second,
		MaxRestartsInWindow:  4,
		RestartWindow:        5 * time.Second,
		HealthyResetInterval: 6 * time.Second,
		HealthCheckInterval:  7 * time.Second,
	}

	m := NewDoltServerManager(townRoot, cfg, func(string, ...interface{}) {})
	if got := m.config.Host; got != "127.0.0.2" {
		t.Fatalf("manager host = %q, want managed host", got)
	}
	if got := m.config.Port; got != 5507 {
		t.Fatalf("manager port = %d, want managed port", got)
	}
	if cfg.Host != "stale-daemon-host" || cfg.Port != 9999 {
		t.Fatalf("input config was mutated: host=%q port=%d", cfg.Host, cfg.Port)
	}
	if !m.config.Enabled || !m.config.External || m.config.User != "root" || m.config.Password != "secret" {
		t.Fatalf("non-endpoint fields were not preserved: %#v", m.config)
	}
	if m.config.DataDir != cfg.DataDir || m.config.LogFile != cfg.LogFile {
		t.Fatalf("paths were not preserved: %#v", m.config)
	}
	if m.config.RestartDelay != cfg.RestartDelay || m.config.MaxRestartDelay != cfg.MaxRestartDelay || m.config.MaxRestartsInWindow != cfg.MaxRestartsInWindow || m.config.RestartWindow != cfg.RestartWindow || m.config.HealthyResetInterval != cfg.HealthyResetInterval || m.config.HealthCheckInterval != cfg.HealthCheckInterval {
		t.Fatalf("restart/health settings were not preserved: %#v", m.config)
	}
}

func TestNewDoltServerManagerPortOnlyManagedConfigClearsStaleHost(t *testing.T) {
	townRoot := t.TempDir()
	writeManagedDoltConfig(t, townRoot, "listener:\n  port: 5507\n")
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "")

	m := NewDoltServerManager(townRoot, &DoltServerConfig{Enabled: true, Host: "stale-daemon-host", Port: 9999}, func(string, ...interface{}) {})
	if got := m.config.Host; got != "" {
		t.Fatalf("manager host = %q, want cleared", got)
	}
	if got := m.config.Port; got != 5507 {
		t.Fatalf("manager port = %d, want managed port", got)
	}
}

func TestNewDoltServerManagerHonorsIgnoreConfig(t *testing.T) {
	townRoot := t.TempDir()
	writeManagedDoltConfig(t, townRoot, "listener:\n  host: 127.0.0.2\n  port: 5507\n")
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "1")

	m := NewDoltServerManager(townRoot, &DoltServerConfig{Enabled: true, Host: "daemon-host", Port: 9999}, func(string, ...interface{}) {})
	if got := m.config.Host; got != "daemon-host" {
		t.Fatalf("manager host = %q, want daemon config host", got)
	}
	if got := m.config.Port; got != 9999 {
		t.Fatalf("manager port = %d, want daemon config port", got)
	}
}

func TestApplyDoltServerConfigEnvUsesNormalizedManagerConfig(t *testing.T) {
	townRoot := t.TempDir()
	writeManagedDoltConfig(t, townRoot, "listener:\n  host: 127.0.0.2\n  port: 5507\n")
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "")
	t.Setenv("GT_DOLT_HOST", "stale-env-host")
	t.Setenv("GT_DOLT_PORT", "9999")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "stale-beads-host")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "9999")
	t.Setenv("BEADS_DOLT_PORT", "9999")

	m := NewDoltServerManager(townRoot, &DoltServerConfig{Enabled: true, Host: "stale-daemon-host", Port: 9999}, func(string, ...interface{}) {})
	applyDoltServerConfigEnv(m.config)

	assertProcessEnv(t, "GT_DOLT_HOST", "127.0.0.2")
	assertProcessEnv(t, "BEADS_DOLT_SERVER_HOST", "127.0.0.2")
	assertProcessEnv(t, "GT_DOLT_PORT", "5507")
	assertProcessEnv(t, "BEADS_DOLT_SERVER_PORT", "5507")
	assertProcessEnv(t, "BEADS_DOLT_PORT", "5507")
}

func TestApplyConfiguredDoltHostEnvClearsManagedConfigWithoutHost(t *testing.T) {
	townRoot := t.TempDir()
	writeManagedDoltConfig(t, townRoot, "listener:\n  port: 5507\n")
	t.Setenv("GT_DOLT_IGNORE_CONFIG", "")
	t.Setenv("GT_DOLT_HOST", "stale-env-host")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "stale-beads-host")

	applyConfiguredDoltHostEnv(townRoot, nil)

	if got := os.Getenv("GT_DOLT_HOST"); got != "" {
		t.Fatalf("GT_DOLT_HOST = %q, want cleared", got)
	}
	if got := os.Getenv("BEADS_DOLT_SERVER_HOST"); got != "" {
		t.Fatalf("BEADS_DOLT_SERVER_HOST = %q, want cleared", got)
	}
}

func writeManagedDoltConfig(t *testing.T, townRoot, content string) {
	t.Helper()
	doltDataDir := filepath.Join(townRoot, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func assertProcessEnv(t *testing.T, key, want string) {
	t.Helper()
	if got := os.Getenv(key); got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}
