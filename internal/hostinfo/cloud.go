package hostinfo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// The firmware tables every cloud fills in its own way, read once per
// facts collection.
type dmi struct {
	vendor, product, assetTag, biosVendor, biosVersion string
}

func readDMI() dmi {
	return dmi{
		vendor:      cleanDMI(readSys("class/dmi/id/sys_vendor")),
		product:     cleanDMI(readSys("class/dmi/id/product_name")),
		assetTag:    cleanDMI(readSys("class/dmi/id/chassis_asset_tag")),
		biosVendor:  cleanDMI(readSys("class/dmi/id/bios_vendor")),
		biosVersion: cleanDMI(readSys("class/dmi/id/bios_version")),
	}
}

// cloud is what the firmware says about the provider, and how to ask that
// provider's metadata service for the instance size when the firmware
// does not carry it. Only the detected provider's endpoint is ever
// contacted — link-local, sub-second, once an hour with the other facts.
type cloud struct {
	name string // shown in front of the size: "Google Compute Engine"
	virt string // the virtualization fact: "gce"
	size string // instance type when the firmware has it
	// fetch asks the metadata service; nil when there is nothing to ask.
	fetch func(ctx context.Context) string
}

const azureAssetTag = "7783-7084-3265-9085-8269-3286-77"

var ec2InstanceType = regexp.MustCompile(`^[a-z0-9-]+\.[a-z0-9-]+$`)

// metadataHosts can be pointed at a test server per provider.
var metadataHosts = map[string]string{
	"aws":      "http://169.254.169.254",
	"gce":      "http://169.254.169.254",
	"azure":    "http://169.254.169.254",
	"oci":      "http://169.254.169.254",
	"alibaba":  "http://100.100.100.200",
	"tencent":  "http://169.254.0.23",
	"scaleway": "http://169.254.42.42",
}

var metadataClient = &http.Client{Timeout: 700 * time.Millisecond}

func detectCloud(d dmi) *cloud {
	has := func(field, s string) bool { return strings.Contains(strings.ToLower(field), strings.ToLower(s)) }
	switch {
	case d.assetTag == azureAssetTag:
		return &cloud{name: "Microsoft Azure", virt: "azure", fetch: fetchAzure}
	case has(d.assetTag, "OracleCloud"):
		return &cloud{name: "Oracle Cloud", virt: "oci", fetch: fetchOCI}
	case has(d.vendor, "Amazon EC2") || has(d.biosVendor, "Amazon EC2") || has(d.biosVersion, "amazon"):
		c := &cloud{name: "Amazon EC2", virt: "ec2"}
		if ec2InstanceType.MatchString(d.product) {
			c.size = d.product // Nitro puts the type in the product name
		} else {
			c.fetch = fetchAWS // Xen-era instances need IMDS
		}
		return c
	case has(d.vendor, "Google"):
		return &cloud{name: "Google Compute Engine", virt: "gce", fetch: fetchGCE}
	case has(d.vendor, "Alibaba"):
		return &cloud{name: "Alibaba Cloud ECS", virt: "alibaba", fetch: fetchAlibaba}
	case has(d.vendor, "Tencent"):
		return &cloud{name: "Tencent Cloud CVM", virt: "tencent", fetch: fetchTencent}
	case has(d.vendor, "Scaleway"):
		return &cloud{name: "Scaleway", virt: "scaleway", fetch: fetchScaleway}
	case has(d.vendor, "DigitalOcean"):
		return &cloud{name: "DigitalOcean Droplet", virt: "digitalocean"}
	case has(d.vendor, "Hetzner"):
		return &cloud{name: "Hetzner " + firstNonEmpty(d.product, "Cloud"), virt: "hetzner"}
	case has(d.vendor, "Vultr"):
		return &cloud{name: "Vultr " + d.product, virt: "vultr"}
	case has(d.vendor, "Linode") || has(d.vendor, "Akamai"):
		return &cloud{name: "Linode " + d.product, virt: "linode"}
	case has(d.vendor, "OpenStack") || has(d.product, "OpenStack"):
		return &cloud{name: "OpenStack Nova", virt: "openstack"}
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// metadataGet fetches one small text value; anything but 200 is "".
func metadataGet(ctx context.Context, url string, headers map[string]string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := metadataClient.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 4096))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func fetchAWS(ctx context.Context) string {
	// IMDSv2: a session token first, then the value.
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, metadataHosts["aws"]+"/latest/api/token", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "60")
	res, err := metadataClient.Do(req)
	if err != nil {
		return ""
	}
	tok, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ""
	}
	return metadataGet(ctx, metadataHosts["aws"]+"/latest/meta-data/instance-type", map[string]string{"X-aws-ec2-metadata-token": strings.TrimSpace(string(tok))})
}

func fetchGCE(ctx context.Context) string {
	// "projects/123/machineTypes/e2-medium"
	v := metadataGet(ctx, metadataHosts["gce"]+"/computeMetadata/v1/instance/machine-type", map[string]string{"Metadata-Flavor": "Google"})
	return v[strings.LastIndex(v, "/")+1:]
}

func fetchAzure(ctx context.Context) string {
	return metadataGet(ctx, metadataHosts["azure"]+"/metadata/instance/compute/vmSize?api-version=2021-02-01&format=text", map[string]string{"Metadata": "true"})
}

func fetchOCI(ctx context.Context) string {
	return metadataGet(ctx, metadataHosts["oci"]+"/opc/v2/instance/shape", map[string]string{"Authorization": "Bearer Oracle"})
}

func fetchAlibaba(ctx context.Context) string {
	return metadataGet(ctx, metadataHosts["alibaba"]+"/latest/meta-data/instance/instance-type", nil)
}

func fetchTencent(ctx context.Context) string {
	return metadataGet(ctx, metadataHosts["tencent"]+"/latest/meta-data/instance/instance-type", nil)
}

func fetchScaleway(ctx context.Context) string {
	body := metadataGet(ctx, metadataHosts["scaleway"]+"/conf?format=json", nil)
	var conf struct {
		CommercialType string `json:"commercial_type"`
	}
	if json.Unmarshal([]byte(body), &conf) != nil {
		return ""
	}
	return conf.CommercialType
}

// hardware is the machine as the firmware — and, on a cloud, its metadata
// service — describes it: "Amazon EC2 t4g.small", "Google Compute Engine
// e2-medium", "Microsoft Azure Standard_B2s", "Dell Inc. PowerEdge R640".
func hardware() string {
	d := readDMI()
	if c := detectCloud(d); c != nil {
		size := c.size
		if size == "" && c.fetch != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			size = c.fetch(ctx)
			cancel()
		}
		return strings.TrimSpace(c.name + " " + size)
	}
	switch {
	case d.vendor == "" && d.product == "":
		return ""
	case d.vendor == "":
		return d.product
	case d.product == "" || strings.Contains(d.product, d.vendor):
		return firstNonEmpty(d.product, d.vendor)
	}
	return d.vendor + " " + d.product
}
