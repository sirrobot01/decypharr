package config

import (
	"strconv"
	"strings"
)

type Hearsay struct {
	Disabled             bool     `json:"disabled,omitzero"`
	Participate          *bool    `json:"participate,omitempty"`
	Publish              *bool    `json:"publish,omitempty"`
	AdviceMode           string   `json:"advice_mode,omitempty"`
	MinSupport           float64  `json:"min_support,omitzero"`
	MinEvidence          float64  `json:"min_evidence,omitzero"`
	MinSources           int      `json:"min_sources,omitzero"`
	Port                 int      `json:"port,omitzero"`
	GossipPort           int      `json:"gossip_port,omitzero"`
	Interval             string   `json:"interval,omitempty"`
	MaxStorageBytes      int64    `json:"max_storage_bytes,omitzero"`
	MaxFeedsPerNamespace int      `json:"max_feeds_per_namespace,omitzero"`
	Follow               []string `json:"follow,omitempty"`
}

func (h Hearsay) Participates() bool {
	return h.Participate == nil || *h.Participate
}

func (h Hearsay) Publishes() bool {
	return h.Participates() && (h.Publish == nil || *h.Publish)
}

func (h Hearsay) IsZero() bool {
	return !h.Disabled && h.Participate == nil && h.Publish == nil && h.AdviceMode == "" &&
		h.MinSupport == 0 && h.MinEvidence == 0 && h.MinSources == 0 &&
		h.Port == 0 && h.GossipPort == 0 && h.Interval == "" &&
		h.MaxStorageBytes == 0 && h.MaxFeedsPerNamespace == 0 && len(h.Follow) == 0
}

func (c *Config) applyHearsayEnvVars() {
	if v := getEnv("HEARSAY__DISABLED"); v != "" {
		c.Hearsay.Disabled = parseBool(v)
	}
	if v := getEnv("HEARSAY__PARTICIPATE"); v != "" {
		c.Hearsay.Participate = new(parseBool(v))
	}
	if v := getEnv("HEARSAY__PUBLISH"); v != "" {
		c.Hearsay.Publish = new(parseBool(v))
	}
	if v := getEnv("HEARSAY__ADVICE_MODE"); v != "" {
		c.Hearsay.AdviceMode = strings.ToLower(strings.TrimSpace(v))
	}
	if v := getEnv("HEARSAY__MIN_SUPPORT"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			c.Hearsay.MinSupport = n
		}
	}
	if v := getEnv("HEARSAY__MIN_EVIDENCE"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			c.Hearsay.MinEvidence = n
		}
	}
	if v := getEnv("HEARSAY__MIN_SOURCES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Hearsay.MinSources = n
		}
	}
	if v := getEnv("HEARSAY__PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.Hearsay.Port = p
		}
	}
	if v := getEnv("HEARSAY__GOSSIP_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.Hearsay.GossipPort = p
		}
	}
	if v := getEnv("HEARSAY__INTERVAL"); v != "" {
		c.Hearsay.Interval = v
	}
	if v := getEnv("HEARSAY__MAX_STORAGE_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.Hearsay.MaxStorageBytes = n
		}
	}
	if v := getEnv("HEARSAY__MAX_FEEDS_PER_NAMESPACE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Hearsay.MaxFeedsPerNamespace = n
		}
	}
	if v := getEnv("HEARSAY__FOLLOW"); v != "" {
		c.Hearsay.Follow = nil
		for key := range strings.SplitSeq(v, ",") {
			if key = strings.TrimSpace(key); key != "" {
				c.Hearsay.Follow = append(c.Hearsay.Follow, key)
			}
		}
	}
}
