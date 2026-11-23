package docktinel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkLoadConfig(b *testing.B) {
	for _, count := range []int{10, 100} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			for i := range 100 {
				b.Setenv(fmt.Sprintf("DOCKTINEL_BENCH_%d", i), strings.Repeat("x", 64))
			}
			var body strings.Builder
			body.WriteString(`{"tasks":[`)
			for i := range count {
				if i != 0 {
					body.WriteByte(',')
				}
				fmt.Fprintf(&body, `{"kind":"job","name":"job%d","command":["true"],"timeout":"5s","interval":"30s"}`, i)
			}
			body.WriteString(`]}`)
			path := filepath.Join(b.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(body.String()), 0600); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := loadConfig(path); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkHeartbeat(b *testing.B) {
	if _, err := uptime(); err != nil {
		b.Skip("requires Linux /proc")
	}
	for _, count := range []int{10, 100} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			d := &Daemon{cfg: Config{Dir: b.TempDir()}, status: Status{Phase: "runtime"}, states: make(map[string]TaskStatus, count)}
			for i := range count {
				name := fmt.Sprintf("job%d", i)
				d.tasks = append(d.tasks, Task{Name: name, Kind: "job"})
				d.states[name] = TaskStatus{Name: name, Kind: "job", State: "healthy"}
			}
			b.ReportAllocs()
			for b.Loop() {
				if err := d.heartbeat(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
