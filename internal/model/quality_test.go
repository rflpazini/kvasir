package model_test

import (
	"testing"

	"github.com/rflpazini/kvasir/internal/model"
)

func TestParseQuality(t *testing.T) {
	cases := []struct {
		title string
		want  model.Quality
	}{
		// 4K family
		{"Movie.Name.2024.2160p.WEB-DL.x265.HDR-GROUP", model.Quality4K},
		{"Some Movie 4K UHD HDR BluRay", model.Quality4K},
		{"Show.S01E01.UHD.HDR10+.WEB-DL", model.Quality4K},
		{"Movie 2024 4k WEBRip", model.Quality4K},
		{"FILME.2024.2160P.BLURAY.X265-GROUP", model.Quality4K}, // upper case 2160P

		// 1080p family
		{"Movie Name 2024 1080p BluRay x264-GROUP", model.Quality1080p},
		{"Show.S02E10.1080p.WEB.h264-GROUP.mkv", model.Quality1080p},
		{"Some.Film.FullHD.WEB-DL", model.Quality1080p},
		{"Filme 2024 FHD BluRay Dual", model.Quality1080p},
		{"Movie.1080P.WEB-DL.DUAL", model.Quality1080p}, // upper case
		{"Filme - Dublado [1080p]", model.Quality1080p}, // brackets

		// Other (neither 4K nor 1080p)
		{"Movie 720p HDTV", model.QualityOther},
		{"Old.Show.480p.DVDRip", model.QualityOther},
		{"Some Title BRRip x264", model.QualityOther},
		{"Filme sem qualidade no titulo", model.QualityOther},
		{"", model.QualityOther},

		// Mixed/precedence: 4K wins over 1080p when both present
		// (rare in practice but possible: "4K.1080p.PROPER")
		{"Movie 2160p 1080p PROPER", model.Quality4K},
		{"Movie 4K 1080p", model.Quality4K},

		// False positives we must NOT trigger:
		// "1080" (without "p") is not 1080p; numbers in titles must not match.
		{"Year 1080 Documentary", model.QualityOther},
		{"Year 2160 BC", model.QualityOther},
		// Words containing the substrings should not match (word-boundary check).
		{"x1080pluskick", model.QualityOther},
	}

	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			got := model.ParseQuality(tc.title)
			if got != tc.want {
				t.Errorf("ParseQuality(%q) = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

func TestQuality_Valid(t *testing.T) {
	valid := []model.Quality{model.Quality4K, model.Quality1080p, model.QualityOther}
	for _, q := range valid {
		if !q.Valid() {
			t.Errorf("Valid() = false for %q, want true", q)
		}
	}

	if model.Quality("720p").Valid() {
		t.Error("Valid() = true for unknown 720p, want false")
	}
}

func TestQualityFromString(t *testing.T) {
	cases := []struct {
		in   string
		want model.Quality
		ok   bool
	}{
		{"4k", model.Quality4K, true},
		{"4K", model.Quality4K, true},
		{"  4K  ", model.Quality4K, true},
		{"1080p", model.Quality1080p, true},
		{"1080P", model.Quality1080p, true},
		{"other", model.QualityOther, true},
		{"720p", "", false},
		{"", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := model.QualityFromString(tc.in)
			if ok != tc.ok {
				t.Fatalf("QualityFromString(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("QualityFromString(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFilterByQuality(t *testing.T) {
	results := []model.Result{
		{Title: "A 2160p", Quality: model.Quality4K},
		{Title: "B 1080p", Quality: model.Quality1080p},
		{Title: "C 720p", Quality: model.QualityOther},
		{Title: "D 4K", Quality: model.Quality4K},
	}

	t.Run("nil filter returns all", func(t *testing.T) {
		got := model.FilterByQuality(results, nil)
		if len(got) != 4 {
			t.Errorf("expected 4, got %d", len(got))
		}
	})

	t.Run("empty filter returns all", func(t *testing.T) {
		got := model.FilterByQuality(results, []model.Quality{})
		if len(got) != 4 {
			t.Errorf("expected 4, got %d", len(got))
		}
	})

	t.Run("4K only", func(t *testing.T) {
		got := model.FilterByQuality(results, []model.Quality{model.Quality4K})
		if len(got) != 2 {
			t.Errorf("expected 2 (4K), got %d", len(got))
		}
		for _, r := range got {
			if r.Quality != model.Quality4K {
				t.Errorf("got non-4K result: %+v", r)
			}
		}
	})

	t.Run("4K + 1080p", func(t *testing.T) {
		got := model.FilterByQuality(results, []model.Quality{model.Quality4K, model.Quality1080p})
		if len(got) != 3 {
			t.Errorf("expected 3, got %d", len(got))
		}
		for _, r := range got {
			if r.Quality == model.QualityOther {
				t.Errorf("Other should be filtered out: %+v", r)
			}
		}
	})

	t.Run("preserves order", func(t *testing.T) {
		got := model.FilterByQuality(results, []model.Quality{model.Quality4K})
		if len(got) != 2 {
			t.Fatalf("expected 2, got %d", len(got))
		}
		if got[0].Title != "A 2160p" || got[1].Title != "D 4K" {
			t.Errorf("order not preserved: got %+v", got)
		}
	})
}
