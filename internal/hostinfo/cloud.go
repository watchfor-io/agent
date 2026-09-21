//go:build linux

package hostinfo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
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
	// public asks it for the addresses the cloud assigned from the
	// outside; nil where the provider does not publish them (OCI).
	public func(ctx context.Context) []string
}

const azureAssetTag = "7783-7084-3265-9085-8269-3286-77"

var ec2InstanceType = regexp.MustCompile(`^[a-z0-9-]+\.[a-z0-9-]+$`)

// metadataHosts can be pointed at a test server per provider.
var metadataHosts = map[string]string{
	"aws":          "http://169.254.169.254",
	"gce":          "http://169.254.169.254",
	"azure":        "http://169.254.169.254",
	"oci":          "http://169.254.169.254",
	"alibaba":      "http://100.100.100.200",
	"tencent":      "http://169.254.0.23",
	"scaleway":     "http://169.254.42.42",
	"digitalocean": "http://169.254.169.254",
	"hetzner":      "http://169.254.169.254",
	"vultr":        "http://169.254.169.254",
	"linode":       "http://169.254.169.254",
	"openstack":    "http://169.254.169.254",
}

var metadataClient = &http.Client{
	Timeout:       700 * time.Millisecond,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// instanceSize is the shape of every provider's size name (t4g.small,
// Standard_B2s, VM.Standard.E4.Flex, DEV1-S); anything else the metadata
// service says is not a size and is dropped.
var instanceSize = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func detectCloud(d dmi) *cloud {
	has := func(field, s string) bool { return strings.Contains(strings.ToLower(field), strings.ToLower(s)) }
	switch {
	case d.assetTag == azureAssetTag:
		return &cloud{name: "Microsoft Azure", virt: "azure", fetch: fetchAzure, public: publicAzure}
	case has(d.assetTag, "OracleCloud"):
		return &cloud{name: "Oracle Cloud", virt: "oci", fetch: fetchOCI}
	case has(d.vendor, "Amazon EC2") || has(d.biosVendor, "Amazon EC2") || has(d.biosVersion, "amazon"):
		c := &cloud{name: "Amazon EC2", virt: "ec2", public: publicAWS}
		if ec2InstanceType.MatchString(d.product) {
			c.size = d.product // Nitro puts the type in the product name
		} else {
			c.fetch = fetchAWS // Xen-era instances need IMDS
		}
		return c
	case has(d.vendor, "Google"):
		return &cloud{name: "Google Compute Engine", virt: "gce", fetch: fetchGCE, public: publicGCE}
	case has(d.vendor, "Alibaba"):
		return &cloud{name: "Alibaba Cloud ECS", virt: "alibaba", fetch: fetchAlibaba, public: publicAlibaba}
	case has(d.vendor, "Tencent"):
		return &cloud{name: "Tencent Cloud CVM", virt: "tencent", fetch: fetchTencent, public: publicTencent}
	case has(d.vendor, "Scaleway"):
		return &cloud{name: "Scaleway", virt: "scaleway", fetch: fetchScaleway, public: publicScaleway}
	case has(d.vendor, "DigitalOcean"):
		return &cloud{name: "DigitalOcean Droplet", virt: "digitalocean", public: publicDigitalOcean}
	case has(d.vendor, "Hetzner"):
		return &cloud{name: "Hetzner " + firstNonEmpty(d.product, "Cloud"), virt: "hetzner", public: publicHetzner}
	case has(d.vendor, "Vultr"):
		return &cloud{name: "Vultr " + d.product, virt: "vultr", public: publicVultr}
	case has(d.vendor, "Linode") || has(d.vendor, "Akamai"):
		return &cloud{name: "Linode " + d.product, virt: "linode", public: publicLinode}
	case has(d.vendor, "OpenStack") || has(d.product, "OpenStack"):
		return &cloud{name: "OpenStack Nova", virt: "openstack", public: publicOpenStack}
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
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<10))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// sessionToken is the PUT-then-use handshake IMDSv2 (AWS) and Linode's
// metadata service both require: the token header on the PUT names the
// lifetime, the token itself rides on every GET after it.
func sessionToken(ctx context.Context, url, ttlHeader string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set(ttlHeader, "60")
	res, err := metadataClient.Do(req)
	if err != nil {
		return ""
	}
	tok, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ""
	}
	return strings.TrimSpace(string(tok))
}

func awsHeaders(ctx context.Context) map[string]string {
	tok := sessionToken(ctx, metadataHosts["aws"]+"/latest/api/token", "X-aws-ec2-metadata-token-ttl-seconds")
	if tok == "" {
		return nil
	}
	return map[string]string{"X-aws-ec2-metadata-token": tok}
}

func fetchAWS(ctx context.Context) string {
	h := awsHeaders(ctx)
	if h == nil {
		return ""
	}
	return metadataGet(ctx, metadataHosts["aws"]+"/latest/meta-data/instance-type", h)
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
func hardware(o Options) string {
	hw, _ := cloudInfo(o)
	return hw
}

// cloudInfo reads the firmware once and, on a recognised cloud with the
// lookup on, asks that provider's metadata service for what the firmware
// does not carry: the instance size and the addresses the cloud assigned
// from the outside. Link-local, sub-second, a handful of fixed paths.
func cloudInfo(o Options) (hw string, public []string) {
	d := readDMI()
	c := detectCloud(d)
	if c == nil {
		if d.vendor != "" || d.product != "" {
			o.debug("no cloud recognised from firmware", "vendor", d.vendor, "product", d.product)
		}
		switch {
		case d.vendor == "" && d.product == "":
			return "", nil
		case d.vendor == "":
			return d.product, nil
		case d.product == "" || strings.Contains(d.product, d.vendor):
			return firstNonEmpty(d.product, d.vendor), nil
		}
		return d.vendor + " " + d.product, nil
	}
	size := c.size
	switch {
	case size != "":
		o.debug("cloud recognised from firmware", "provider", c.virt, "size", size, "metadata", "not needed")
	case c.fetch == nil:
		o.debug("cloud recognised from firmware", "provider", c.virt, "size", "not published by this provider")
	case !o.CloudMetadata:
		o.debug("cloud recognised from firmware", "provider", c.virt, "metadata", "lookup disabled by facts.cloud_metadata")
	default:
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		size = c.fetch(ctx)
		cancel()
		if !instanceSize.MatchString(size) {
			size = ""
		}
		o.debug("cloud metadata service asked for the instance size", "provider", c.virt, "host", metadataHosts[c.virt], "size", size, "found", size != "")
	}
	if o.CloudMetadata && c.public != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		public = publicAddresses(c.public(ctx))
		cancel()
		o.debug("cloud metadata service asked for the public addresses", "provider", c.virt, "host", metadataHosts[c.virt], "found", len(public))
	}
	return strings.TrimSpace(c.name + " " + size), public
}

// publicAddresses keeps what a metadata service said only if it really is
// a public address: parsed, global unicast, not a private or link-local
// range, prefix length dropped ("203.0.113.5/32"), duplicates folded,
// IPv4 before IPv6, at most eight. A service that answered with anything
// else contributes nothing.
func publicAddresses(raw []string) []string {
	seen := map[string]bool{}
	var v4, v6 []string
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if i := strings.IndexByte(s, '/'); i > 0 {
			s = s[:i]
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		a = a.Unmap().WithZone("")
		if !a.IsGlobalUnicast() || a.IsPrivate() {
			continue
		}
		k := a.String()
		if seen[k] {
			continue
		}
		seen[k] = true
		if a.Is4() {
			v4 = append(v4, k)
		} else {
			v6 = append(v6, k)
		}
	}
	out := append(v4, v6...)
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// lines splits a metadata listing (one value per line) and caps it, so a
// service that lists hundreds of interfaces cannot turn into hundreds of
// requests.
func lines(s string, max int) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
		if len(out) == max {
			break
		}
	}
	return out
}

// ec2Style walks the EC2-shaped tree AWS, Alibaba and Tencent share:
// the public IPv4 at the top, IPv6 per interface under the MAC list.
func ec2Style(ctx context.Context, base string, h map[string]string, top ...string) []string {
	var out []string
	for _, p := range top {
		out = append(out, metadataGet(ctx, base+p, h))
	}
	for _, mac := range lines(metadataGet(ctx, base+"network/interfaces/macs/", h), 8) {
		mac = strings.TrimSuffix(mac, "/")
		out = append(out, lines(metadataGet(ctx, base+"network/interfaces/macs/"+mac+"/ipv6s", h), 8)...)
	}
	return out
}

func publicAWS(ctx context.Context) []string {
	h := awsHeaders(ctx)
	if h == nil {
		return nil
	}
	return ec2Style(ctx, metadataHosts["aws"]+"/latest/meta-data/", h, "public-ipv4")
}

func publicAlibaba(ctx context.Context) []string {
	// eipv4 is the elastic IP; public-ipv4 the classic-network one
	return ec2Style(ctx, metadataHosts["alibaba"]+"/latest/meta-data/", nil, "eipv4", "public-ipv4")
}

func publicTencent(ctx context.Context) []string {
	return ec2Style(ctx, metadataHosts["tencent"]+"/latest/meta-data/", nil, "public-ipv4")
}

func publicOpenStack(ctx context.Context) []string {
	// the EC2-compatible tree most Nova deployments serve; absent → nothing
	return []string{metadataGet(ctx, metadataHosts["openstack"]+"/latest/meta-data/public-ipv4", nil)}
}

func publicGCE(ctx context.Context) []string {
	body := metadataGet(ctx, metadataHosts["gce"]+"/computeMetadata/v1/instance/network-interfaces/?recursive=true", map[string]string{"Metadata-Flavor": "Google"})
	var ifaces []struct {
		AccessConfigs []struct {
			ExternalIP string `json:"externalIp"`
		} `json:"accessConfigs"`
		IPv6AccessConfigs []struct {
			ExternalIPv6 string `json:"externalIpv6"`
		} `json:"ipv6AccessConfigs"`
		IPv6s []string `json:"ipv6s"`
	}
	if json.Unmarshal([]byte(body), &ifaces) != nil {
		return nil
	}
	var out []string
	for _, i := range ifaces {
		for _, a := range i.AccessConfigs {
			out = append(out, a.ExternalIP)
		}
		for _, a := range i.IPv6AccessConfigs {
			out = append(out, a.ExternalIPv6)
		}
		out = append(out, i.IPv6s...)
	}
	return out
}

func publicAzure(ctx context.Context) []string {
	body := metadataGet(ctx, metadataHosts["azure"]+"/metadata/instance/network?api-version=2021-02-01", map[string]string{"Metadata": "true"})
	var network struct {
		Interface []struct {
			IPv4 struct {
				IPAddress []struct {
					PublicIPAddress string `json:"publicIpAddress"`
				} `json:"ipAddress"`
			} `json:"ipv4"`
			IPv6 struct {
				IPAddress []struct {
					// Azure calls every v6 on the NIC "private"; a routed
					// one is global unicast and the filter keeps it
					PrivateIPAddress string `json:"privateIpAddress"`
				} `json:"ipAddress"`
			} `json:"ipv6"`
		} `json:"interface"`
	}
	if json.Unmarshal([]byte(body), &network) != nil {
		return nil
	}
	var out []string
	for _, i := range network.Interface {
		for _, a := range i.IPv4.IPAddress {
			out = append(out, a.PublicIPAddress)
		}
		for _, a := range i.IPv6.IPAddress {
			out = append(out, a.PrivateIPAddress)
		}
	}
	return out
}

func publicScaleway(ctx context.Context) []string {
	body := metadataGet(ctx, metadataHosts["scaleway"]+"/conf?format=json", nil)
	var conf struct {
		PublicIP struct {
			Address string `json:"address"`
		} `json:"public_ip"`
		PublicIPs []struct {
			Address string `json:"address"`
		} `json:"public_ips"`
		IPv6 struct {
			Address string `json:"address"`
		} `json:"ipv6"`
	}
	if json.Unmarshal([]byte(body), &conf) != nil {
		return nil
	}
	out := []string{conf.PublicIP.Address, conf.IPv6.Address}
	for _, p := range conf.PublicIPs {
		out = append(out, p.Address)
	}
	return out
}

func publicDigitalOcean(ctx context.Context) []string {
	base := metadataHosts["digitalocean"] + "/metadata/v1/interfaces/public/0/"
	return []string{metadataGet(ctx, base+"ipv4/address", nil), metadataGet(ctx, base+"ipv6/address", nil)}
}

func publicHetzner(ctx context.Context) []string {
	return []string{metadataGet(ctx, metadataHosts["hetzner"]+"/hetzner/v1/metadata/public-ipv4", nil)}
}

func publicVultr(ctx context.Context) []string {
	base := metadataHosts["vultr"] + "/v1/interfaces/0/"
	return []string{metadataGet(ctx, base+"ipv4/address", nil), metadataGet(ctx, base+"ipv6/address", nil)}
}

func publicLinode(ctx context.Context) []string {
	tok := sessionToken(ctx, metadataHosts["linode"]+"/v1/token", "Metadata-Token-Expiry-Seconds")
	if tok == "" {
		return nil
	}
	body := metadataGet(ctx, metadataHosts["linode"]+"/v1/network", map[string]string{"Metadata-Token": tok, "Accept": "application/json"})
	var network struct {
		IPv4 struct {
			Public []string `json:"public"`
		} `json:"ipv4"`
		IPv6 struct {
			SLAAC  string   `json:"slaac"`
			Ranges []string `json:"ranges"`
		} `json:"ipv6"`
	}
	if json.Unmarshal([]byte(body), &network) != nil {
		return nil
	}
	return append(append(network.IPv4.Public, network.IPv6.SLAAC), network.IPv6.Ranges...)
}
