package web

import (
	"cmp"
	"embed"
	"github.com/gorilla/sessions"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/wire"
	"html/template"
	"os"
)

var restartFunc func()

// SetRestartFunc allows setting a callback to restart services
func SetRestartFunc(fn func()) {
	restartFunc = fn
}

type AddRequest struct {
	Url        string   `json:"url"`
	Arr        string   `json:"arr"`
	File       string   `json:"file"`
	NotSymlink bool     `json:"notSymlink"`
	Content    string   `json:"content"`
	Seasons    []string `json:"seasons"`
	Episodes   []string `json:"episodes"`
}

type ArrResponse struct {
	Name string `json:"name"`
	Url  string `json:"url"`
}

type ContentResponse struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
	ArrID string `json:"arr"`
}

type RepairRequest struct {
	ArrName     string   `json:"arr"`
	MediaIds    []string `json:"mediaIds"`
	Async       bool     `json:"async"`
	AutoProcess bool     `json:"autoProcess"`
}

//go:embed templates/*
var content embed.FS

//go:embed assets/build/*
var assetsEmbed embed.FS

//go:embed assets/images/*
var imagesEmbed embed.FS

type Web struct {
	logger    zerolog.Logger
	cookie    *sessions.CookieStore
	templates *template.Template
	torrents  *wire.TorrentStorage
}

func New() *Web {
	templates := template.Must(template.ParseFS(
		content,
		"templates/layout.html",
		"templates/index.html",
		"templates/download.html",
		"templates/repair.html",
		"templates/stats.html",
		"templates/config.html",
		"templates/login.html",
		"templates/register.html",
	))
	secretKey := cmp.Or(os.Getenv("DECYPHARR_SECRET_KEY"), "\"wqj(v%lj*!-+kf@4&i95rhh_!5_px5qnuwqbr%cjrvrozz_r*(\"")
	cookieStore := sessions.NewCookieStore([]byte(secretKey))
	cookieStore.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7,
		HttpOnly: false,
	}
	return &Web{
		logger:    logger.New("ui"),
		templates: templates,
		cookie:    cookieStore,
		torrents:  wire.Get().Torrents(),
	}
}
