// +build freebsd

package freebsd

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/mackerelio/mackerel-agent/logging"
)

// MemoryGenerator collects the host's memory specs.
type MemoryGenerator struct {
}

func (g *MemoryGenerator) Key() string {
	return "memory"
}

var memoryLogger = logging.GetLogger("spec.memory")

const bytesInKibibytes = 1024

// Generate returns memory specs.
// The returned spec must have below:
// - total (in "###kB" format, Kibibytes)
func (g *MemoryGenerator) Generate() (interface{}, error) {
	spec := map[string]string{}

	cmd := exec.Command("sysctl", "-n", "hw.physmem")
	outputBytes, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sysctl -n hw.physmem: %s", err)
	}

	output := string(outputBytes)

	memsizeInBytes, err := strconv.ParseInt(strings.TrimSpace(output), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("while parsing %q: %s", output, err)
	}

	spec["total"] = fmt.Sprintf("%dkB", memsizeInBytes/bytesInKibibytes)

	return spec, nil
}
