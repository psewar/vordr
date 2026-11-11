package osv

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/versions"
)

// TestLiveDump builds the index from real dumps, when VORDR_OSV_DUMPS names a folder with PyPI-all.zip and
// npm-all.zip (as osv.dev publishes them): counts, build time, heap and index size for the design, and a few
// lookups that must hold in any current dump.
func TestLiveDump(t *testing.T) {
	dir := os.Getenv("VORDR_OSV_DUMPS")
	if dir == "" {
		t.Skip("VORDR_OSV_DUMPS not set")
	}
	for _, eco := range []versions.Ecosystem{versions.PyPI, versions.Npm} {
		path := filepath.Join(dir, string(eco)+"-all.zip")
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		fi, _ := f.Stat()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		db, skipped, err := Build(f, fi.Size(), eco, time.Now())
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		took := time.Since(start)
		runtime.GC()
		runtime.ReadMemStats(&after)
		idx := filepath.Join(t.TempDir(), "idx")
		if err := db.Save(idx); err != nil {
			t.Fatal(err)
		}
		ii, _ := os.Stat(idx)
		start = time.Now()
		if _, _, err := Load(idx); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s; zip %.1f MB, %d skipped, %d package names, build %s, heap +%.1f MB, index %.1f MB, load %s",
			db, float64(fi.Size())/1e6, skipped, len(db.Packages), took.Round(time.Millisecond),
			float64(after.HeapAlloc-before.HeapAlloc)/1e6, float64(ii.Size())/1e6, time.Since(start).Round(time.Millisecond))
		cases := map[versions.Ecosystem][][3]string{
			versions.Npm:  {{"chalk", "5.6.1", "MAL-2025-46969"}, {"debug", "4.4.2", "MAL-2025-46974"}, {"axios", "1.14.1", "MAL-2026-2307"}},
			versions.PyPI: {{"requests", "2.31.0", "GHSA-9wx4-h78v-vm56"}, {"urllib3", "1.26.18", "GHSA-34jh-p97f-mpxf"}},
		}
		for _, c := range cases[eco] {
			found, _ := db.Lookup(c[0], c[1])
			hit := false
			for _, f := range found {
				hit = hit || f.ID == c[2]
			}
			if !hit {
				t.Errorf("%s %s@%s: %s missing in %v", eco, c[0], c[1], c[2], ids(found))
			}
		}
	}
}
