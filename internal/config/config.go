// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all runtime configuration for the proxy.
type Config struct {
	AWSRegion    string
	ECRRegistry  string
	Port         string
	CatalogCache time.Duration
}

// Load reads configuration from environment variables and validates it.
func Load() (*Config, error) {
	cfg := &Config{
		AWSRegion:   os.Getenv("AWS_REGION"),
		ECRRegistry: os.Getenv("ECR_REGISTRY"),
		Port:        os.Getenv("PORT"),
	}

	if cfg.AWSRegion == "" {
		return nil, fmt.Errorf("AWS_REGION must be set")
	}
	if cfg.ECRRegistry == "" {
		return nil, fmt.Errorf("ECR_REGISTRY must be set")
	}
	if cfg.Port == "" {
		cfg.Port = "5000"
	}

	cacheSeconds := 60
	if raw := os.Getenv("CATALOG_CACHE_SECONDS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return nil, fmt.Errorf("CATALOG_CACHE_SECONDS must be a non-negative integer: %w", err)
		}
		cacheSeconds = parsed
	}
	cfg.CatalogCache = time.Duration(cacheSeconds) * time.Second

	return cfg, nil
}
