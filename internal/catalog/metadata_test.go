package catalog

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeMetadata(t *testing.T) {
	got, err := NormalizeMetadata(Metadata{Title: "  Trò chơi  ", Description: " dòng 1\ndòng 2 ",
		Genres: []string{" drama ", "", "drama", "hài"}, Year: 2024})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Trò chơi" || got.Description != "dòng 1\ndòng 2" || got.Year != 2024 ||
		!slices.Equal(got.Genres, []string{"drama", "hài"}) {
		t.Errorf("got %+v", got)
	}
	if got, _ := NormalizeMetadata(Metadata{Title: "A"}); got.Genres == nil {
		t.Error("genres must be non-nil")
	}

	tooManyGenres := make([]string, MaxGenres+1)
	for i := range tooManyGenres {
		tooManyGenres[i] = strings.Repeat("g", i+1)
	}
	bad := map[string]Metadata{
		"empty title":      {Title: "   "},
		"long title":       {Title: strings.Repeat("ữ", MaxTitleLen+1)},
		"control in title": {Title: "a\x00b"},
		"newline in title": {Title: "a\nb"},
		"long description": {Title: "A", Description: strings.Repeat("a", MaxDescriptionLen+1)},
		"long genre":       {Title: "A", Genres: []string{strings.Repeat("g", MaxGenreLen+1)}},
		"too many genres":  {Title: "A", Genres: tooManyGenres},
		"year too small":   {Title: "A", Year: 1500},
		"year too big":     {Title: "A", Year: 3000},
		"invalid utf8":     {Title: "\xff"},
	}
	for name, m := range bad {
		if _, err := NormalizeMetadata(m); !errors.Is(err, ErrInvalidMetadata) {
			t.Errorf("%s: err = %v, want ErrInvalidMetadata", name, err)
		}
	}
	if _, err := NormalizeMetadata(Metadata{Title: strings.Repeat("ữ", MaxTitleLen), Year: MinYear}); err != nil {
		t.Errorf("limits must be inclusive: %v", err)
	}
}
