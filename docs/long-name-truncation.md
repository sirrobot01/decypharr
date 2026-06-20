# Smart Torrent Name Truncation

## Problem

Linux imposes a hard limit of **255 bytes** per directory component (`NAME_MAX`). Multi-byte
UTF-8 scripts (Cyrillic = 2 bytes/char, CJK = 3 bytes/char, Arabic = 2 bytes/char) cause long
release names to exceed this limit. Symlink creation then fails with:

```
failed to create symlink … file name too long
```

Example offending name (296 bytes in UTF-8):

```
Перси Джексон и Олимпийцы - Сезон 2 / Percy Jackson and the Olympians - Season 2 (2025) [WEB-DL 1080p] MVO
```

---

## Solution

Two complementary mechanisms, no external dependencies:

| Layer | What it does |
|-------|-------------|
| **Safety net** in `DownloadPath()` | Byte-safe UTF-8 truncation on every path built — always active, protects pre-existing entries |
| **Smart resolver** in `processNewTorrent` | Extracts the ASCII/English title, year, and SxxExx from the raw name; builds a short clean name |

The smart resolver works for any non-Western script (Cyrillic, CJK, Arabic, Korean, Japanese,
Thai, …) as long as the release includes an embedded English title — which nearly all
internationally distributed releases do.

---

## Configuration

Add to `config.json` only if you need to change the default:

```json
{
  "prefer_ascii_name": false
}
```

| Value | Behaviour |
|-------|-----------|
| `true` (default, key omitted) | Extract ASCII/English title and build a compact name |
| `false` | Preserve the raw name as-is, byte-safe truncated to 255 bytes |

Set to `false` only if your library is intentionally non-Western and you want native-script
directory names preserved.

---

## Changes

### 1. `internal/utils/file.go` — `TruncateName`

```diff
+import "unicode/utf8"
+
+func TruncateName(name string, maxBytes int) string {
+    if len(name) <= maxBytes {
+        return name
+    }
+    b := []byte(name)[:maxBytes]
+    for len(b) > 0 && !utf8.Valid(b) {
+        b = b[:len(b)-1]
+    }
+    return strings.TrimRight(string(b), " ")
+}
```

Walks back from byte 255 until the slice is valid UTF-8, avoiding split multibyte runes.

### 2. `pkg/storage/types.go` — `DownloadPath()` safety net

```diff
 func (e *Entry) DownloadPath() string {
-    return filepath.Join(e.SavePath, utils.RemoveExtension(e.Name))
+    const nameMax = 255
+    name := utils.TruncateName(utils.RemoveExtension(e.Name), nameMax)
+    return filepath.Join(e.SavePath, name)
 }
```

Always active regardless of `prefer_ascii_name`.

### 3. `internal/config/config.go` — `PreferASCIIName` flag

```diff
+    // PreferASCIIName controls whether decypharr extracts the ASCII/Western title
+    // from a raw torrent name and builds a compact canonical directory name.
+    // Disable only if your library is intentionally non-Western.
+    // Default: true
+    PreferASCIIName *bool `json:"prefer_ascii_name,omitempty"`
```

Using `*bool` so omitting the key from `config.json` defaults to `true` with no breaking
change for existing installs.

### 4. `internal/utils/nameparser.go` — new file

Pure-regex parser, no network or external dependencies. Handles:

- Standard `SxxExx` notation: `S02E01`, `S02E01E04`, `S02E01-04`
- Word-based: `Season 2`, `Сезон 2`, `Episodes 1-4`, `Серии 1-4`
- Dotted names: `Breaking.Bad.S05E10.720p` → spaces before parsing
- Technical tags (`HEVC`, `1080p`, `WEB-DL`, …) mark end of title region
- Longest capitalised ASCII run used as English title candidate

```diff
+func ParseTorrentName(raw string) ParsedName
+func BuildCleanName(p ParsedName, canonicalTitle string) string
+
+type ParsedName struct {
+    Title   string  // ASCII title candidate
+    Year    string  // "2025" or ""
+    Season  int     // 0 if unknown
+    EpStart int     // 0 if unknown
+    EpEnd   int     // 0 if no range
+    IsTV    bool
+}
```

**Output examples:**

| Raw torrent name | Clean name |
|-----------------|-----------|
| `Перси Джексон…Сезон 2…Percy Jackson and the Olympians…S02E01E04…` | `Percy Jackson and the Olympians (2025) S02E01-E04` |
| `鬼滅の刃 Demon Slayer S03E11 1080p` | `Demon Slayer S03E11` |
| `괴물 Monster (2021) 한국영화 1080p` | `Monster (2021)` |
| `Breaking.Bad.S05E10.720p.BluRay` | `Breaking Bad S05E10` |
| `NCIS.New.Orleans.S04E23E24.HDTV` | `NCIS New Orleans S04E23-E24` |

### 5. `pkg/manager/nameresolver.go` — new file

```diff
+func (m *Manager) resolveEntryName(raw string) string {
+    cfg := config.Get()
+    if cfg.PreferASCIIName != nil && !*cfg.PreferASCIIName {
+        return utils.TruncateName(utils.RemoveExtension(raw), 255)
+    }
+    parsed := utils.ParseTorrentName(raw)
+    clean := utils.BuildCleanName(parsed, "")
+    if clean == "" {
+        return utils.TruncateName(utils.RemoveExtension(raw), 255)
+    }
+    return clean
+}
```

### 6. `pkg/manager/processor.go` — `processNewTorrent`

```diff
-torrent.Name = debridTorrent.Name
+torrent.Name = m.resolveEntryName(debridTorrent.Name)
 torrent.OriginalFilename = debridTorrent.OriginalFilename
+torrent.ContentPath = torrent.DownloadPath() // re-sync after name resolution
 torrent.UpdatedAt = time.Now()
```

---

## Edge Cases

| Scenario | Behaviour |
|---------|-----------|
| Cyrillic/CJK/Arabic with embedded English title | English title extracted; native script discarded |
| Non-Western name with no ASCII title at all | `BuildCleanName` returns `""`; falls back to `TruncateName(raw, 255)` |
| `prefer_ascii_name: false` | Always `TruncateName(raw, 255)` — raw name preserved |
| Pre-existing entries in storage | `DownloadPath()` safety net truncates regardless of setting |
| Multi-episode range | `S02E01-E04` preserved in output |
| Dotted name | Dots replaced with spaces before parsing |
