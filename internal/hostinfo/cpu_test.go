//go:build linux

package hostinfo

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/watchfor-io/agent/internal/procfs"
)

func fakeMachine(t *testing.T, cpuinfo, vendor, product string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sys", "class", "dmi", "id"), 0o755); err != nil {
		t.Fatal(err)
	}
	must := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(filepath.Join(root, "cpuinfo"), cpuinfo)
	if vendor != "" {
		must(filepath.Join(root, "sys", "class", "dmi", "id", "sys_vendor"), vendor+"\n")
	}
	if product != "" {
		must(filepath.Join(root, "sys", "class", "dmi", "id", "product_name"), product+"\n")
	}
	oldRoot, oldSys := procfs.Root, procfs.SysRoot
	procfs.Root, procfs.SysRoot = root, filepath.Join(root, "sys")
	t.Cleanup(func() { procfs.Root, procfs.SysRoot = oldRoot, oldSys })
}

const gravitonCPUInfo = `processor	: 0
BogoMIPS	: 243.75
Features	: fp asimd evtstrm aes pmull sha1 sha2 crc32 atomics
CPU implementer	: 0x41
CPU architecture: 8
CPU variant	: 0x3
CPU part	: 0xd0c
CPU revision	: 1

processor	: 1
CPU implementer	: 0x41
CPU part	: 0xd0c
`

func TestCPUModelAndHardware(t *testing.T) {
	cases := []struct {
		name, cpuinfo, vendor, product, wantModel, wantHW string
	}{
		{"x86 with model name", "processor\t: 0\nmodel name\t: Intel(R) Xeon(R) Silver 4114 CPU @ 2.20GHz\n", "Dell Inc.", "PowerEdge R640",
			"Intel(R) Xeon(R) Silver 4114 CPU @ 2.20GHz", "Dell Inc. PowerEdge R640"},
		{"Graviton2 on EC2", gravitonCPUInfo, "Amazon EC2", "t4g.small",
			"AWS Graviton2 (Neoverse-N1)", "Amazon EC2 t4g.small"},
		{"Neoverse-N1 elsewhere", gravitonCPUInfo, "Hetzner", "vServer",
			"ARM Neoverse-N1", "Hetzner vServer"},
		{"Ampere Altra", "CPU implementer\t: 0xc0\nCPU part\t: 0xac3\n", "", "",
			"Ampere Ampere-1", ""},
		{"unknown ARM part", "CPU implementer\t: 0x41\nCPU part\t: 0xd99\n", "", "",
			"ARM part 0xd99", ""},
		{"DMI placeholders ignored", "model name\t: AMD Ryzen 5 5500U with Radeon Graphics\n", "To be filled by O.E.M.", "System Product Name",
			"AMD Ryzen 5 5500U with Radeon Graphics", ""},
		{"QEMU product repeats nothing", "model name\t: QEMU Virtual CPU version 2.5+\n", "QEMU", "Standard PC (i440FX + PIIX, 1996)",
			"QEMU Virtual CPU version 2.5+", "QEMU Standard PC (i440FX + PIIX, 1996)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeMachine(t, c.cpuinfo, c.vendor, c.product)
			if got := cpuModel(); got != c.wantModel {
				t.Errorf("cpuModel = %q, want %q", got, c.wantModel)
			}
			if got := hardware(Options{CloudMetadata: true}); got != c.wantHW {
				t.Errorf("hardware = %q, want %q", got, c.wantHW)
			}
		})
	}
}

func writeSys(t *testing.T, name, value string) {
	t.Helper()
	p := filepath.Join(procfs.SysRoot, "class", "dmi", "id", name)
	if err := os.WriteFile(p, []byte(value+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCloudHardwareFromMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
			io.WriteString(w, "TOKEN123")
		case r.URL.Path == "/latest/meta-data/instance-type":
			if r.Header.Get("X-aws-ec2-metadata-token") != "TOKEN123" {
				w.WriteHeader(401)
				return
			}
			io.WriteString(w, "m5.large")
		case r.URL.Path == "/computeMetadata/v1/instance/machine-type":
			if r.Header.Get("Metadata-Flavor") != "Google" {
				w.WriteHeader(403)
				return
			}
			io.WriteString(w, "projects/1234/machineTypes/e2-medium")
		case r.URL.Path == "/metadata/instance/compute/vmSize":
			if r.Header.Get("Metadata") != "true" {
				w.WriteHeader(400)
				return
			}
			io.WriteString(w, "Standard_B2s")
		case r.URL.Path == "/opc/v2/instance/shape":
			if r.Header.Get("Authorization") != "Bearer Oracle" {
				w.WriteHeader(401)
				return
			}
			io.WriteString(w, "VM.Standard.E4.Flex")
		case r.URL.Path == "/latest/meta-data/instance/instance-type":
			io.WriteString(w, "ecs.g6.large")
		case r.URL.Path == "/conf":
			io.WriteString(w, `{"commercial_type":"DEV1-S","id":"x"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	old := map[string]string{}
	for k, v := range metadataHosts {
		old[k] = v
		metadataHosts[k] = srv.URL
	}
	t.Cleanup(func() {
		for k, v := range old {
			metadataHosts[k] = v
		}
	})

	cases := []struct {
		name, vendor, product, assetTag, biosVendor string
		wantHW, wantVirt                            string
	}{
		{"EC2 Nitro (type in DMI)", "Amazon EC2", "t4g.small", "", "", "Amazon EC2 t4g.small", "ec2"},
		{"EC2 Xen (type via IMDSv2)", "Xen", "HVM domU", "", "Amazon EC2", "Amazon EC2 m5.large", "ec2"},
		{"GCE", "Google", "Google Compute Engine", "", "", "Google Compute Engine e2-medium", "gce"},
		{"Azure (asset tag, not plain Hyper-V)", "Microsoft Corporation", "Virtual Machine", azureAssetTag, "", "Microsoft Azure Standard_B2s", "azure"},
		{"Hyper-V without the tag", "Microsoft Corporation", "Virtual Machine", "", "", "Microsoft Corporation Virtual Machine", "hyperv"},
		{"OCI (asset tag over QEMU vendor)", "QEMU", "Standard PC (i440FX + PIIX, 1996)", "OracleCloud.com", "", "Oracle Cloud VM.Standard.E4.Flex", "oci"},
		{"Alibaba", "Alibaba Cloud", "Alibaba Cloud ECS", "", "", "Alibaba Cloud ECS ecs.g6.large", "alibaba"},
		{"Scaleway", "Scaleway", "SCW-DEV1-S", "", "", "Scaleway DEV1-S", "scaleway"},
		{"DigitalOcean (no size in metadata)", "DigitalOcean", "Droplet", "", "", "DigitalOcean Droplet", "digitalocean"},
		{"Hetzner", "Hetzner", "vServer", "", "", "Hetzner vServer", "hetzner"},
		{"plain KVM", "QEMU", "Standard PC (Q35 + ICH9, 2009)", "", "", "QEMU Standard PC (Q35 + ICH9, 2009)", "kvm"},
		{"metal", "Dell Inc.", "PowerEdge R640", "", "", "Dell Inc. PowerEdge R640", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeMachine(t, "model name\t: x\n", c.vendor, c.product)
			if c.assetTag != "" {
				writeSys(t, "chassis_asset_tag", c.assetTag)
			}
			if c.biosVendor != "" {
				writeSys(t, "bios_vendor", c.biosVendor)
			}
			if got := hardware(Options{CloudMetadata: true}); got != c.wantHW {
				t.Errorf("hardware = %q, want %q", got, c.wantHW)
			}
			if got := virtualization(); got != c.wantVirt {
				t.Errorf("virtualization = %q, want %q", got, c.wantVirt)
			}
		})
	}
}
