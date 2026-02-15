package utils

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/sirrobot01/decypharr/internal/logger"
)

var (
	hexRegex = regexp.MustCompile("^[0-9a-fA-F]{40}$")
)

type Magnet struct {
	Name     string `json:"name"`
	InfoHash string `json:"infoHash"`
	Size     int64  `json:"size"`
	Link     string `json:"link"`
	File     []byte `json:"-"`
}

func (m *Magnet) IsTorrent() bool {
	return m.File != nil
}

// stripTrackersFromMagnet removes trackers from a magnet and returns a modified copy
func stripTrackersFromMagnet(mi metainfo.Magnet, fileType string) metainfo.Magnet {
	originalTrackerCount := len(mi.Trackers)
	if len(mi.Trackers) > 0 {
		log := logger.Default()
		mi.Trackers = nil
		log.Printf("Removed %d tracker URLs from %s", originalTrackerCount, fileType)
	}
	return mi
}

func stripTrackersFromTorrentFile(data []byte) ([]byte, error) {
	mi, err := metainfo.Load(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	mi.Announce = ""
	mi.AnnounceList = nil
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// GetTorrentInfo parses raw .torrent file bytes and optionally removes tracker URLs.
func GetTorrentInfo(torrentData []byte, rmTrackerUrls bool) (*Magnet, error) {
	mi, err := metainfo.Load(bytes.NewReader(torrentData))
	if err != nil {
		return nil, err
	}
	hash := mi.HashInfoBytes()
	infoHash := hash.HexString()
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return nil, err
	}

	finalTorrentData := torrentData
	if rmTrackerUrls {
		finalTorrentData, err = stripTrackersFromTorrentFile(torrentData)
		if err != nil {
			return nil, err
		}
	}

	finalMI, err := metainfo.Load(bytes.NewReader(finalTorrentData))
	if err != nil {
		return nil, err
	}
	magnetMeta := finalMI.Magnet(&hash, &info)

	return &Magnet{
		InfoHash: infoHash,
		Name:     info.Name,
		Size:     info.Length,
		Link:     magnetMeta.String(),
		File:     finalTorrentData,
	}, nil
}

func GetMagnetFromFile(file io.Reader, filePath string, rmTrackerUrls bool) (*Magnet, error) {
	var (
		m   *Magnet
		err error
	)
	if filepath.Ext(filePath) == ".torrent" {
		torrentData, err := io.ReadAll(file)
		if err != nil {
			return nil, err
		}
		m, err = GetMagnetFromBytes(torrentData, rmTrackerUrls)
		if err != nil {
			return nil, err
		}
	} else {
		// .magnet file
		magnetLink := ReadMagnetFile(file)
		m, err = GetMagnetInfo(magnetLink, rmTrackerUrls)
		if err != nil {
			return nil, err
		}
	}
	m.Name = strings.TrimSuffix(filePath, filepath.Ext(filePath))
	return m, nil
}

func GetMagnetFromUrl(url string, rmTrackerUrls bool) (*Magnet, error) {
	if strings.HasPrefix(url, "magnet:") {
		return GetMagnetInfo(url, rmTrackerUrls)
	} else if strings.HasPrefix(url, "http") {
		return OpenMagnetHttpURL(url, rmTrackerUrls)
	}
	return nil, fmt.Errorf("invalid url")
}

func GetMagnetFromBytes(torrentData []byte, rmTrackerUrls bool) (*Magnet, error) {
	return GetTorrentInfo(torrentData, rmTrackerUrls)
}

func ReadMagnetFile(file io.Reader) string {
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		content := scanner.Text()
		if content != "" {
			return content
		}
	}

	// Check for any errors during scanning
	if err := scanner.Err(); err != nil {
		log := logger.Default()
		log.Println("Error reading file:", err)
	}
	return ""
}

func OpenMagnetHttpURL(magnetLink string, rmTrackerUrls bool) (*Magnet, error) {
	resp, err := http.Get(magnetLink)
	if err != nil {
		return nil, fmt.Errorf("error making GET request: %v", err)
	}
	defer func(resp *http.Response) {
		err := resp.Body.Close()
		if err != nil {
			return
		}
	}(resp) // Ensure the response is closed after the function ends
	torrentData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response body: %v", err)
	}
	return GetMagnetFromBytes(torrentData, rmTrackerUrls)
}

func GetMagnetInfo(magnetLink string, rmTrackerUrls bool) (*Magnet, error) {
	if magnetLink == "" {
		return nil, fmt.Errorf("error getting magnet from file")
	}

	mi, err := metainfo.ParseMagnetUri(magnetLink)
	if err != nil {
		return nil, fmt.Errorf("error parsing magnet link: %w", err)
	}

	// Strip all announce URLs if requested
	if rmTrackerUrls {
		mi = stripTrackersFromMagnet(mi, "magnet link")
	}

	btih := mi.InfoHash.HexString()
	dn := mi.DisplayName

	// Reconstruct the magnet link using the (possibly modified) spec
	finalLink := mi.String()

	magnet := &Magnet{
		InfoHash: btih,
		Name:     dn,
		Size:     0,
		Link:     finalLink,
	}
	return magnet, nil
}

func ExtractInfoHash(magnetDesc string) string {
	const prefix = "xt=urn:btih:"
	start := strings.Index(magnetDesc, prefix)
	if start == -1 {
		return ""
	}
	hash := ""
	start += len(prefix)
	end := strings.IndexAny(magnetDesc[start:], "&#")
	if end == -1 {
		hash = magnetDesc[start:]
	} else {
		hash = magnetDesc[start : start+end]
	}
	hash, _ = processInfoHash(hash) // Convert to hex if needed
	return hash
}

func processInfoHash(input string) (string, error) {
	// Regular expression for a valid 40-character hex infohash

	// If it's already a valid hex infohash, return it as is
	if hexRegex.MatchString(input) {
		return strings.ToLower(input), nil
	}

	// If it's 32 characters long, it might be Base32 encoded
	if len(input) == 32 {
		// Ensure the input is uppercase and remove any padding
		input = strings.ToUpper(strings.TrimRight(input, "="))

		// Try to decode from Base32
		decoded, err := base32.StdEncoding.DecodeString(input)
		if err == nil && len(decoded) == 20 {
			// If successful and the result is 20 bytes, encode to hex
			return hex.EncodeToString(decoded), nil
		}
	}

	// If we get here, it's not a valid infohash and we couldn't convert it
	return "", fmt.Errorf("invalid infohash: %s", input)
}

func ConstructMagnet(infoHash, name string) *Magnet {
	// Create a magnet link from the infohash and name
	name = url.QueryEscape(strings.TrimSpace(name))
	magnetUri := fmt.Sprintf("magnet:?xt=urn:btih:%s&dn=%s", infoHash, name)
	return &Magnet{
		InfoHash: infoHash,
		Name:     name,
		Size:     0,
		Link:     magnetUri,
	}
}

func GenerateInfoHash() string {
	// Generate a random 40-character hexadecimal string (20 bytes = 40 hex chars)
	b := make([]byte, 20)
	_, err := io.ReadFull(rand.Reader, b)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
