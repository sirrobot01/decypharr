package web

import (
	"cmp"
	"context"
	"embed"
	"html/template"
	"os"

	"github.com/gorilla/sessions"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/store"
)

var restartFunc func()

// SetRestartFunc allows setting a callback to restart services
func SetRestartFunc(fn func()) {
	restartFunc = fn
}

// StoreInterface defines the interface for store operations that Web needs
type StoreInterface interface {
	AddTorrent(ctx context.Context, req *store.ImportRequest) error
	Arr() ArrStorageInterface
}

// ArrStorageInterface defines the interface for arr storage operations
type ArrStorageInterface interface {
	Get(name string) *arr.Arr
}

// ProductionStore wraps the real store to implement StoreInterface
type ProductionStore struct{}

func (p *ProductionStore) AddTorrent(ctx context.Context, req *store.ImportRequest) error {
	return store.Get().AddTorrent(ctx, req)
}

func (p *ProductionStore) Arr() ArrStorageInterface {
	return &ProductionArrStorage{}
}

// ProductionArrStorage wraps the real arr storage to implement ArrStorageInterface
type ProductionArrStorage struct{}

func (p *ProductionArrStorage) Get(name string) *arr.Arr {
	return store.Get().Arr().Get(name)
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

type Web struct {
	logger    zerolog.Logger
	cookie    *sessions.CookieStore
	templates *template.Template
	torrents  *store.TorrentStorage
	store     StoreInterface
}

func New() *Web {
	templates := template.Must(template.ParseFS(
		content,
		"templates/layout.html",
		"templates/index.html",
		"templates/download.html",
		"templates/repair.html",
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
		torrents:  store.Get().Torrents(),
		store:     &ProductionStore{},
	}
}

// NewWithDependencies creates a Web instance with injected dependencies for testing
func NewWithDependencies(storeInterface StoreInterface, logger zerolog.Logger) *Web {
	return &Web{
		logger: logger,
		store:  storeInterface,
		// Note: We don't need cookie, templates, or torrents for API testing
	}
}