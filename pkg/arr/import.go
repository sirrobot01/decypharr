package arr

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	gourl "net/url"
	"strconv"
	"time"
)

type ImportResponseSchema struct {
	Path         string `json:"path"`
	RelativePath string `json:"relativePath"`
	FolderName   string `json:"folderName"`
	Name         string `json:"name"`
	Size         int    `json:"size"`
	Series       struct {
		Title     string `json:"title"`
		SortTitle string `json:"sortTitle"`
		Status    string `json:"status"`
		Ended     bool   `json:"ended"`
		Overview  string `json:"overview"`
		Network   string `json:"network"`
		AirTime   string `json:"airTime"`
		Images    []struct {
			CoverType string `json:"coverType"`
			RemoteUrl string `json:"remoteUrl"`
		} `json:"images"`
		OriginalLanguage struct {
			Id   int    `json:"id"`
			Name string `json:"name"`
		} `json:"originalLanguage"`
		Seasons []struct {
			SeasonNumber int  `json:"seasonNumber"`
			Monitored    bool `json:"monitored"`
		} `json:"seasons"`
		Year              int           `json:"year"`
		Path              string        `json:"path"`
		QualityProfileId  int           `json:"qualityProfileId"`
		SeasonFolder      bool          `json:"seasonFolder"`
		Monitored         bool          `json:"monitored"`
		MonitorNewItems   string        `json:"monitorNewItems"`
		UseSceneNumbering bool          `json:"useSceneNumbering"`
		Runtime           int           `json:"runtime"`
		TvdbId            int           `json:"tvdbId"`
		TvRageId          int           `json:"tvRageId"`
		TvMazeId          int           `json:"tvMazeId"`
		TmdbId            int           `json:"tmdbId"`
		FirstAired        time.Time     `json:"firstAired"`
		LastAired         time.Time     `json:"lastAired"`
		SeriesType        string        `json:"seriesType"`
		CleanTitle        string        `json:"cleanTitle"`
		ImdbId            string        `json:"imdbId"`
		TitleSlug         string        `json:"titleSlug"`
		Certification     string        `json:"certification"`
		Genres            []string      `json:"genres"`
		Tags              []interface{} `json:"tags"`
		Added             time.Time     `json:"added"`
		Ratings           struct {
			Votes int     `json:"votes"`
			Value float64 `json:"value"`
		} `json:"ratings"`
		LanguageProfileId int `json:"languageProfileId"`
		Id                int `json:"id"`
	} `json:"series"`
	SeasonNumber int `json:"seasonNumber"`
	Episodes     []struct {
		SeriesId                 int       `json:"seriesId"`
		TvdbId                   int       `json:"tvdbId"`
		EpisodeFileId            int       `json:"episodeFileId"`
		SeasonNumber             int       `json:"seasonNumber"`
		EpisodeNumber            int       `json:"episodeNumber"`
		Title                    string    `json:"title"`
		AirDate                  string    `json:"airDate"`
		AirDateUtc               time.Time `json:"airDateUtc"`
		Runtime                  int       `json:"runtime"`
		Overview                 string    `json:"overview"`
		HasFile                  bool      `json:"hasFile"`
		Monitored                bool      `json:"monitored"`
		AbsoluteEpisodeNumber    int       `json:"absoluteEpisodeNumber"`
		UnverifiedSceneNumbering bool      `json:"unverifiedSceneNumbering"`
		Id                       int       `json:"id"`
		FinaleType               string    `json:"finaleType,omitempty"`
	} `json:"episodes"`
	ReleaseGroup string `json:"releaseGroup"`
	Quality      struct {
		Quality struct {
			Id         int    `json:"id"`
			Name       string `json:"name"`
			Source     string `json:"source"`
			Resolution int    `json:"resolution"`
		} `json:"quality"`
		Revision struct {
			Version  int  `json:"version"`
			Real     int  `json:"real"`
			IsRepack bool `json:"isRepack"`
		} `json:"revision"`
	} `json:"quality"`
	Languages []struct {
		Id   int    `json:"id"`
		Name string `json:"name"`
	} `json:"languages"`
	QualityWeight     int           `json:"qualityWeight"`
	CustomFormats     []interface{} `json:"customFormats"`
	CustomFormatScore int           `json:"customFormatScore"`
	IndexerFlags      int           `json:"indexerFlags"`
	ReleaseType       string        `json:"releaseType"`
	Rejections        []struct {
		Reason string `json:"reason"`
		Type   string `json:"type"`
	} `json:"rejections"`
	Id int `json:"id"`
}

type ManualImportRequestFile struct {
	Path         string `json:"path"`
	SeriesId     int    `json:"seriesId"`
	SeasonNumber int    `json:"seasonNumber"`
	EpisodeIds   []int  `json:"episodeIds"`
	Quality      struct {
		Quality struct {
			Id         int    `json:"id"`
			Name       string `json:"name"`
			Source     string `json:"source"`
			Resolution int    `json:"resolution"`
		} `json:"quality"`
		Revision struct {
			Version  int  `json:"version"`
			Real     int  `json:"real"`
			IsRepack bool `json:"isRepack"`
		} `json:"revision"`
	} `json:"quality"`
	Languages []struct {
		Id   int    `json:"id"`
		Name string `json:"name"`
	} `json:"languages"`
	ReleaseGroup      string        `json:"releaseGroup"`
	CustomFormats     []interface{} `json:"customFormats"`
	CustomFormatScore int           `json:"customFormatScore"`
	IndexerFlags      int           `json:"indexerFlags"`
	ReleaseType       string        `json:"releaseType"`
	Rejections        []struct {
		Reason string `json:"reason"`
		Type   string `json:"type"`
	} `json:"rejections"`
}

type ManualImportRequestSchema struct {
	Name       string                    `json:"name"`
	Files      []ManualImportRequestFile `json:"files"`
	ImportMode string                    `json:"importMode"`
}

func (a *Arr) Import(path string, seriesId int, seasons []int) (io.ReadCloser, error) {
	query := gourl.Values{}
	query.Add("folder", path)
	if seriesId != 0 {
		query.Add("seriesId", strconv.Itoa(seriesId))
	}
	url := "api/v3/manualimport" + "?" + query.Encode()
	resp, err := a.Request(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to import, invalid file: %w", err)
	}
	defer resp.Body.Close()
	var data []ImportResponseSchema
	if err = json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var files []ManualImportRequestFile
	for _, d := range data {
		episodesIds := []int{}
		for _, e := range d.Episodes {
			episodesIds = append(episodesIds, e.Id)
		}
		file := ManualImportRequestFile{
			Path:              d.Path,
			SeriesId:          d.Series.Id,
			SeasonNumber:      d.SeasonNumber,
			EpisodeIds:        episodesIds,
			Quality:           d.Quality,
			Languages:         d.Languages,
			ReleaseGroup:      d.ReleaseGroup,
			CustomFormats:     d.CustomFormats,
			CustomFormatScore: d.CustomFormatScore,
			IndexerFlags:      d.IndexerFlags,
			ReleaseType:       d.ReleaseType,
			Rejections:        d.Rejections,
		}
		files = append(files, file)
	}
	request := ManualImportRequestSchema{
		Name:       "ManualImport",
		Files:      files,
		ImportMode: "copy",
	}

	url = "api/v3/command"
	resp, err = a.Request(http.MethodPost, url, request)
	if err != nil {
		return nil, fmt.Errorf("failed to import: %w", err)
	}
	defer resp.Body.Close()
	return resp.Body, nil

}
