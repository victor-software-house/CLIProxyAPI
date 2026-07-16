package cliproxy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"gopkg.in/yaml.v3"
)

// ConfigApplyResult describes the current in-memory configuration snapshot.
type ConfigApplyResult struct {
	ConfigHash string
	AppliedAt  time.Time
}

type canonicalConfig struct {
	*config.Config `yaml:",inline"`
	Home           canonicalHomeConfig `yaml:"home"`
}

type canonicalHomeConfig struct {
	Enabled                 bool             `yaml:"enabled"`
	NodeID                  string           `yaml:"node-id"`
	Host                    string           `yaml:"host"`
	Port                    int              `yaml:"port"`
	DisableClusterDiscovery bool             `yaml:"disable-cluster-discovery"`
	TLS                     canonicalHomeTLS `yaml:"tls"`
}

type canonicalHomeTLS struct {
	Enable              bool   `yaml:"enable"`
	ServerName          string `yaml:"server-name"`
	InsecureSkipVerify  bool   `yaml:"insecure-skip-verify"`
	CACert              string `yaml:"ca-cert"`
	ClientCert          string `yaml:"client-cert"`
	ClientKey           string `yaml:"client-key"`
	UseTargetServerName bool   `yaml:"use-target-server-name"`
}

func configHash(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("cliproxy: configuration is required")
	}
	canonical := canonicalConfig{
		Config: cfg,
		Home: canonicalHomeConfig{
			Enabled:                 cfg.Home.Enabled,
			NodeID:                  cfg.Home.NodeID,
			Host:                    cfg.Home.Host,
			Port:                    cfg.Home.Port,
			DisableClusterDiscovery: cfg.Home.DisableClusterDiscovery,
			TLS: canonicalHomeTLS{
				Enable:              cfg.Home.TLS.Enable,
				ServerName:          cfg.Home.TLS.ServerName,
				InsecureSkipVerify:  cfg.Home.TLS.InsecureSkipVerify,
				CACert:              cfg.Home.TLS.CACert,
				ClientCert:          cfg.Home.TLS.ClientCert,
				ClientKey:           cfg.Home.TLS.ClientKey,
				UseTargetServerName: cfg.Home.TLS.UseTargetServerName,
			},
		},
	}
	data, errMarshal := yaml.Marshal(canonical)
	if errMarshal != nil {
		return "", fmt.Errorf("cliproxy: marshal canonical configuration: %w", errMarshal)
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum), nil
}

// ApplyConfig synchronously applies an independent in-memory config snapshot.
func (s *Service) ApplyConfig(ctx context.Context, cfg *config.Config) (ConfigApplyResult, error) {
	if s == nil {
		return ConfigApplyResult{}, fmt.Errorf("cliproxy: service is nil")
	}
	if cfg == nil {
		return ConfigApplyResult{}, fmt.Errorf("cliproxy: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return ConfigApplyResult{}, fmt.Errorf("cliproxy: %w", errContext)
	}

	applied := cfg.CloneForRuntime()
	hash, errHash := configHash(applied)
	if errHash != nil {
		return ConfigApplyResult{}, errHash
	}

	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()
	if errContext := ctx.Err(); errContext != nil {
		return ConfigApplyResult{}, fmt.Errorf("cliproxy: %w", errContext)
	}

	s.applyConfigUpdateWithAuthSynthesisLocked(applied, true)
	if s.watcher != nil {
		s.watcher.SetConfig(applied.CloneForRuntime())
	}
	result := ConfigApplyResult{ConfigHash: hash, AppliedAt: time.Now().UTC()}
	s.configSnapshot = result
	return result, nil
}

// ConfigSnapshot returns the hash and time of the most recently applied config.
func (s *Service) ConfigSnapshot(ctx context.Context) (ConfigApplyResult, error) {
	if s == nil {
		return ConfigApplyResult{}, fmt.Errorf("cliproxy: service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return ConfigApplyResult{}, fmt.Errorf("cliproxy: %w", errContext)
	}

	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()
	if errContext := ctx.Err(); errContext != nil {
		return ConfigApplyResult{}, fmt.Errorf("cliproxy: %w", errContext)
	}
	return s.configSnapshot, nil
}
