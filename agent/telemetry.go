package agent

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/abhishek/hardware-aware-container-orchestrator/model"
)

// collectThermal is a best-effort reading, never a hard failure (SRS §9.5:
// "Thermal data should be treated as a signal, not a universal temperature
// threshold"). Known is false — not zero-and-misleading — whenever the
// host exposes no sensor this agent can read, which includes every
// platform but Linux today.
func collectThermal() model.Thermal {
	if runtime.GOOS != "linux" {
		return model.Thermal{Known: false}
	}

	matches, err := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	if err != nil || len(matches) == 0 {
		return model.Thermal{Known: false}
	}

	var maxC float64
	found := false
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		milliC, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if err != nil {
			continue
		}
		c := milliC / 1000
		if !found || c > maxC {
			maxC = c
			found = true
		}
	}
	if !found {
		return model.Thermal{Known: false}
	}

	// No portable, driver-independent "is this node throttling" signal
	// exists across hardware (SRS §9.5: "prefer explicit thermal-throttling
	// indicators... where available"); lacking one, treat a very high
	// reading itself as the throttling signal rather than inventing a
	// fake one.
	return model.Thermal{Known: true, TemperatureC: maxC, Throttling: maxC >= 95}
}

// collectGPUs is a best-effort NVIDIA-only detection via nvidia-smi. A host
// with no NVIDIA GPU, or no driver installed, reports an empty list — the
// scheduler already treats "no GPU" and "GPU present but unreported" as
// the empty case, and FR-022 only requires GPU scheduling once discovery
// exists, not comprehensive vendor support.
func collectGPUs(ctx context.Context) []model.GPU {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--query-gpu=name,memory.total,memory.free", "--format=csv,noheader,nounits")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var gpus []model.GPU
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ",")
		if len(fields) != 3 {
			continue
		}
		totalMiB, errTotal := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64)
		freeMiB, errFree := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
		if errTotal != nil || errFree != nil {
			continue
		}
		gpus = append(gpus, model.GPU{
			Vendor:         "nvidia",
			Model:          strings.TrimSpace(fields[0]),
			VRAMTotalBytes: totalMiB * 1024 * 1024,
			VRAMFreeBytes:  freeMiB * 1024 * 1024,
		})
	}
	return gpus
}
