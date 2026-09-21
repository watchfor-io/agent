//go:build linux

package hostinfo

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/watchfor-io/agent/internal/procfs"
)

// cpuModel names the processor. x86 puts it in cpuinfo's "model name";
// ARM only gives implementer and part numbers, so those are looked up
// (Neoverse-N1 and friends), and on EC2 the Graviton generation is added
// because that is the name people know.
func cpuModel() string {
	b, err := os.ReadFile(filepath.Join(procfs.Root, "cpuinfo"))
	if err != nil {
		return ""
	}
	var implementer, part, uarch string
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "model name", "Model", "cpu model":
			if v != "" {
				return v
			}
		case "CPU implementer":
			implementer = v
		case "CPU part":
			part = v
		case "uarch": // RISC-V
			uarch = v
		}
	}
	if name := armName(implementer, part); name != "" {
		return name
	}
	return uarch
}

// cleanDMI drops the placeholders motherboard vendors leave in the tables.
func cleanDMI(s string) string {
	s = procfs.Clean(s)
	s = strings.TrimSpace(s)
	for _, junk := range []string{"To be filled by O.E.M.", "To Be Filled By O.E.M.", "System Product Name", "System manufacturer", "Default string", "Not Specified", "Not Applicable", "None", "Unknown", "N/A", "OEM"} {
		if strings.EqualFold(s, junk) {
			return ""
		}
	}
	return s
}

var armImplementers = map[int]string{
	0x41: "ARM", 0x42: "Broadcom", 0x43: "Cavium", 0x44: "DEC", 0x46: "Fujitsu", 0x48: "HiSilicon",
	0x49: "Infineon", 0x4d: "Motorola", 0x4e: "NVIDIA", 0x50: "APM", 0x51: "Qualcomm", 0x53: "Samsung",
	0x56: "Marvell", 0x61: "Apple", 0x66: "Faraday", 0x69: "Intel", 0x70: "Phytium", 0xc0: "Ampere",
}

var armParts = map[int]map[int]string{
	0x41: {
		0xd03: "Cortex-A53", 0xd04: "Cortex-A35", 0xd05: "Cortex-A55", 0xd06: "Cortex-A65", 0xd07: "Cortex-A57",
		0xd08: "Cortex-A72", 0xd09: "Cortex-A73", 0xd0a: "Cortex-A75", 0xd0b: "Cortex-A76", 0xd0c: "Neoverse-N1",
		0xd0d: "Cortex-A77", 0xd0e: "Cortex-A76AE", 0xd40: "Neoverse-V1", 0xd41: "Cortex-A78", 0xd42: "Cortex-A78AE",
		0xd44: "Cortex-X1", 0xd46: "Cortex-A510", 0xd47: "Cortex-A710", 0xd48: "Cortex-X2", 0xd49: "Neoverse-N2",
		0xd4a: "Neoverse-E1", 0xd4b: "Cortex-A78C", 0xd4d: "Cortex-A715", 0xd4e: "Cortex-X3", 0xd4f: "Neoverse-V2",
		0xd80: "Cortex-A520", 0xd81: "Cortex-A720", 0xd82: "Cortex-X4", 0xd84: "Neoverse-V3", 0xd85: "Cortex-X925",
		0xd87: "Cortex-A725", 0xd8e: "Neoverse-N3",
	},
	0x43: {0x0a1: "ThunderX", 0x0af: "ThunderX2", 0x0b8: "ThunderX3"},
	0x46: {0x001: "A64FX"},
	0x48: {0xd01: "Kunpeng-920"},
	0x4e: {0x004: "Carmel"},
	0x50: {0x000: "X-Gene"},
	0x51: {0xc00: "Falkor", 0x800: "Kryo-2xx-Gold", 0x801: "Kryo-2xx-Silver", 0x803: "Kryo-3xx-Silver", 0x804: "Kryo-4xx-Gold", 0x805: "Kryo-4xx-Silver"},
	0xc0: {0xac3: "Ampere-1", 0xac4: "Ampere-1a"},
}

// AWS sells these Neoverse cores under its own names.
var graviton = map[string]string{"Neoverse-N1": "Graviton2", "Neoverse-V1": "Graviton3", "Neoverse-V2": "Graviton4"}

func armName(implementer, part string) string {
	if implementer == "" || part == "" {
		return ""
	}
	impl, err1 := strconv.ParseInt(strings.TrimPrefix(implementer, "0x"), 16, 32)
	prt, err2 := strconv.ParseInt(strings.TrimPrefix(part, "0x"), 16, 32)
	if err1 != nil || err2 != nil {
		return ""
	}
	vendor := armImplementers[int(impl)]
	if vendor == "" {
		vendor = "ARM implementer " + implementer
	}
	name := armParts[int(impl)][int(prt)]
	if name == "" {
		return vendor + " part " + part
	}
	if g := graviton[name]; g != "" && onEC2() {
		return "AWS " + g + " (" + name + ")"
	}
	return vendor + " " + name
}

func onEC2() bool {
	c := detectCloud(readDMI())
	return c != nil && c.virt == "ec2"
}
