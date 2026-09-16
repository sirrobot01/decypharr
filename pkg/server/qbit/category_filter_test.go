package qbit

import (
	"os"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "decypharr-category-test-")
	if err != nil {
		panic(err)
	}
	config.SetConfigPath(dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestCategoryHashFilter(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
		want   []bool
		bad    bool
	}{
		{"one", []string{"AAA"}, []bool{true, false, false}, false},
		{"pipe", []string{"aaa|bbb"}, []bool{true, true, false}, false},
		{"repeated", []string{"aaa", "bbb"}, []bool{true, true, false}, false},
		{"unknown", []string{"ddd"}, []bool{false, false, false}, false},
		{"explicit all", []string{"all"}, []bool{true, true, true}, false},
		{"missing", nil, nil, true},
		{"empty", []string{" | "}, nil, true},
		{"mixed all", []string{"all|aaa"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter, err := categoryHashFilter(tc.values)
			if (err != nil) != tc.bad {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.bad {
				return
			}
			for i, hash := range []string{"aaa", "BBB", "ccc"} {
				if filter(&storage.Entry{InfoHash: hash}) != tc.want[i] {
					t.Fatalf("incorrect selection of row %s", hash)
				}
			}
		})
	}
}

func TestCategoryUpdatePreservesUnselectedRows(t *testing.T) {
	s, err := storage.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, hash := range []string{"aaa", "bbb", "ccc"} {
		if err := s.UpdateQueue(&storage.Entry{InfoHash: hash, Category: "wrong", SavePath: "/downloads/original"}); err != nil {
			t.Fatal(err)
		}
	}
	filter, _ := categoryHashFilter([]string{"aaa|bbb"})
	if err := s.UpdateWhereQueued(filter, func(e *storage.Entry) bool {
		e.Category = "restored"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{"aaa", "bbb", "ccc"} {
		e, err := s.GetQueued(hash)
		if err != nil {
			t.Fatal(err)
		}
		want := "restored"
		if hash == "ccc" {
			want = "wrong"
		}
		if e.Category != want || e.SavePath != "/downloads/original" {
			t.Fatalf("unexpected mutation for %s", hash)
		}
	}
}
