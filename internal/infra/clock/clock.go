package clock

import (
	"sync"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/config"
)

var (
	location     *time.Location
	locationOnce sync.Once
)

// Configure runs once in the CLI, before starting any background goroutines.
func Configure() { time.Local = Location() }

// Resolve lazily, after main's time/tzdata import has registered the embedded
// database. Package initialization must not depend on host zoneinfo files.
func Location() *time.Location {
	locationOnce.Do(func() {
		var err error
		location, err = time.LoadLocation(config.Timezone)
		if err != nil {
			panic("Asia/Shanghai timezone data is required")
		}
	})
	return location
}

func Now() time.Time           { return time.Now().In(Location()) }
func In(t time.Time) time.Time { return t.In(Location()) }
