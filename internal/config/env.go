package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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
	for i := range 100 { // Support up to 100 categories
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
	for i := range 100 {
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

	c.applyNFSEnvVars()

	c.applySMBEnvVars()

	c.applyShareCacheEnvVars()

	c.applyDebridEnvVars()

	c.applyUsenetEnvVars()

	// Arr applications array
	for i := range 20 { // Support up to 20 arr applications
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
		}
	}

}

func (c *Config) applyNFSEnvVars() {
	if val := getEnv("NFS__ENABLED"); val != "" {
		c.NFS.Enabled = parseBool(val)
	}
	if val := getEnv("NFS__BIND_ADDRESS"); val != "" {
		c.NFS.BindAddress = val
	}
	if val := getEnv("NFS__PORT"); val != "" {
		if v, err := strconv.ParseUint(val, 10, 16); err == nil {
			c.NFS.Port = uint16(v)
		}
	}
	if val := getEnv("NFS__ALLOWED_NETWORKS"); val != "" {
		c.NFS.AllowedNetworks = strings.FieldsFunc(val, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\n'
		})
	}
	c.setNFSDefaults()
}

func (c *Config) applySMBEnvVars() {
	if val := getEnv("SMB__ENABLED"); val != "" {
		c.SMB.Enabled = parseBool(val)
	}
	if val := getEnv("SMB__BIND_ADDRESS"); val != "" {
		c.SMB.BindAddress = val
	}
	if val := getEnv("SMB__PORT"); val != "" {
		if v, err := strconv.ParseUint(val, 10, 16); err == nil {
			c.SMB.Port = uint16(v)
		}
	}
	if val := getEnv("SMB__SHARE_NAME"); val != "" {
		c.SMB.ShareName = val
	}
	if val := getEnv("SMB__USERNAME"); val != "" {
		c.SMB.Username = val
	}
	if val := getEnv("SMB__PASSWORD"); val != "" {
		c.SMB.Password = val
	}
	if val := getEnv("SMB__REQUIRE_SIGNING"); val != "" {
		c.SMB.RequireSigning = parseBool(val)
	}
	if val := getEnv("SMB__ALLOWED_NETWORKS"); val != "" {
		c.SMB.AllowedNetworks = strings.FieldsFunc(val, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\n'
		})
	}
	c.setSMBDefaults()
}

func (c *Config) applyShareCacheEnvVars() {
	if val := getEnv("SHARE_CACHE__ENABLED"); val != "" {
		enabled := parseBool(val)
		c.ShareCache.Enabled = &enabled
	}
	if val := getEnv("SHARE_CACHE__DIR"); val != "" {
		c.ShareCache.Dir = val
	}
	if val := getEnv("SHARE_CACHE__MAX_SIZE"); val != "" {
		c.ShareCache.MaxSize = val
	}
	if val := getEnv("SHARE_CACHE__MAX_AGE"); val != "" {
		c.ShareCache.MaxAge = val
	}
	if val := getEnv("SHARE_CACHE__CHUNK_SIZE"); val != "" {
		c.ShareCache.ChunkSize = val
	}
	if val := getEnv("SHARE_CACHE__READ_AHEAD"); val != "" {
		c.ShareCache.ReadAhead = val
	}
	c.setShareCacheDefaults()
}
