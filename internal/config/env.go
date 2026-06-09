package config

import (
	"fmt"
	"os"
	"strconv"
)

func getEnv(key string) string {
	return os.Getenv("DECYPHARR_" + key)
}

func parseBool(val string) bool {
	return val == "true" || val == "1" || val == "yes"
}

// applyEnvOverrides applies environment variable overrides with DECYPHARR_ prefix
// Environment variables use __ (double underscore) for nested fields and array indices
// Examples:
//
//	DECYPHARR_PORT=9090
//	DECYPHARR_DOWNLOAD_FOLDER=/downloads
//	DECYPHARR_DEBRIDS__0__NAME=realdebrid
//	DECYPHARR_DEBRIDS__0__API_KEY=abc123
func (c *Config) applyEnvOverrides() {
	// Root level fields
	if val := getEnv("PORT"); val != "" {
		c.Port = val
	}
	if val := getEnv("BIND_ADDRESS"); val != "" {
		c.BindAddress = val
	}
	if val := getEnv("URL_BASE"); val != "" {
		c.URLBase = val
	}
	if val := getEnv("LOG_LEVEL"); val != "" {
		c.LogLevel = val
	}
	if val := getEnv("USE_AUTH"); val != "" {
		c.UseAuth = parseBool(val)
	}

	// Manager settings
	if val := getEnv("DOWNLOAD_FOLDER"); val != "" {
		c.DownloadFolder = val
	}
	if val := getEnv("REFRESH_INTERVAL"); val != "" {
		c.RefreshInterval = val
	}
	if val := getEnv("MAX_ACTIVE_DOWNLOADS"); val != "" {
		if v, err := strconv.Atoi(val); err == nil {
			c.MaxActiveDownloads = v
		}
	}
	if val := getEnv("SKIP_PRE_CACHE"); val != "" {
		c.SkipPreCache = parseBool(val)
	}
	if val := getEnv("ALWAYS_RM_TRACKER_URLS"); val != "" {
		c.AlwaysRmTrackerUrls = parseBool(val)
	}
	if val := getEnv("MIN_FILE_SIZE"); val != "" {
		c.MinFileSize = val
	}
	if val := getEnv("MAX_FILE_SIZE"); val != "" {
		c.MaxFileSize = val
	}
	if val := getEnv("REMOVE_STALLED_AFTER"); val != "" {
		c.RemoveStalledAfter = val
	}
	if val := getEnv("ENABLE_WEBDAV_AUTH"); val != "" {
		c.EnableWebdavAuth = parseBool(val)
	}
	if val := getEnv("RETRIES"); val != "" {
		if v, err := strconv.Atoi(val); err == nil {
			c.Retries = v
		}
	}

	if val := getEnv("SKIP_AUTO_MOVE"); val != "" {
		c.SkipAutoMove = parseBool(val)
	}
	// Manager categories array
	for i := 0; i < 100; i++ { // Support up to 100 categories
		key := fmt.Sprintf("CATEGORIES__%d", i)
		if val := getEnv(key); val != "" {
			if i >= len(c.Categories) {
				c.Categories = append(c.Categories, make([]string, i-len(c.Categories)+1)...)
			}
			c.Categories[i] = val
		} else {
			break
		}
	}
	// Manager allowed extensions array
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("ALLOWED_FILE_TYPES__%d", i)
		if val := getEnv(key); val != "" {
			if i >= len(c.AllowedExt) {
				c.AllowedExt = append(c.AllowedExt, make([]string, i-len(c.AllowedExt)+1)...)
			}
			c.AllowedExt[i] = val
		} else {
			break
		}
	}

	if nzbUserAgent := getEnv("NZB_USER_AGENT"); nzbUserAgent != "" {
		c.NZBUserAgent = nzbUserAgent
	}

	c.applyMountEnvVars()

	c.applyDebridEnvVars()

	c.applyUsenetEnvVars()

	// Arr applications array
	for i := 0; i < 20; i++ { // Support up to 20 arr applications
		prefix := fmt.Sprintf("ARRS__%d__", i)
		if val := getEnv(prefix + "NAME"); val != "" {
			// Ensure array is large enough
			if i >= len(c.Arrs) {
				c.Arrs = append(c.Arrs, make([]Arr, i-len(c.Arrs)+1)...)
			}
			c.Arrs[i].Name = val

			// Set other arr fields
			if host := getEnv(prefix + "HOST"); host != "" {
				c.Arrs[i].Host = host
			}
			if token := getEnv(prefix + "TOKEN"); token != "" {
				c.Arrs[i].Token = token
			}
			if cleanup := getEnv(prefix + "CLEANUP"); cleanup != "" {
				c.Arrs[i].Cleanup = parseBool(cleanup)
			}
		}
	}

}
