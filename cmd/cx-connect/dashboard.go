package main

import (
	"time"

	"github.com/ZacharyJia/cx-connect/config"
	"github.com/ZacharyJia/cx-connect/dashboard"
)

func createDashboardReporter(cfg *config.Config, webURL string) (*dashboard.Reporter, error) {
	if !cfg.Dashboard.Enabled {
		return nil, nil
	}

	heartbeat := 5 * time.Second
	if cfg.Dashboard.Heartbeat != "" {
		d, err := time.ParseDuration(cfg.Dashboard.Heartbeat)
		if err != nil {
			return nil, err
		}
		heartbeat = d
	}

	return dashboard.NewReporter(dashboard.ReporterConfig{
		Enabled:           true,
		Endpoint:          cfg.Dashboard.Endpoint,
		Token:             cfg.Dashboard.Token,
		InstanceID:        cfg.Dashboard.InstanceID,
		InstanceName:      cfg.Dashboard.InstanceName,
		WebURL:            webURL,
		HeartbeatInterval: heartbeat,
	}, "default", cfg.Agent.Type, version)
}
