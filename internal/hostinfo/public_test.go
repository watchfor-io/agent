//go:build linux

package hostinfo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// The filter is the whole safety of the feature: a metadata service is
// trusted for shape, never for content.
func TestPublicAddressesKeepsOnlyPublicOnes(t *testing.T) {
	got := publicAddresses([]string{
		"203.0.113.5/32", " 203.0.113.5 ", // prefix dropped, duplicate folded
		"2600:1f18::1/64", "2600:1f18::1",
		"10.0.0.4", "172.31.21.108", "192.168.1.1", // private
		"169.254.169.254", "fe80::1", // link-local
		"fd00::1", "::1", "127.0.0.1", "", "nope", "<script>", // not public, not addresses
		"::ffff:198.51.100.7", // mapped v4 → v4
	})
	want := []string{"203.0.113.5", "198.51.100.7", "2600:1f18::1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	many := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		many = append(many, "203.0.113."+string(rune('0'+i%10))+string(rune('0'+i/10)))
	}
	if n := len(publicAddresses(many)); n > 8 {
		t.Errorf("cap: %d addresses kept", n)
	}
}

// One fake metadata service answers every provider's documented paths;
// each provider must come back with exactly its own addresses.
func TestCloudPublicAddressesFromMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		awsTok := r.Header.Get("X-aws-ec2-metadata-token") == "TOKEN123"
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
			io.WriteString(w, "TOKEN123")
		case r.Method == http.MethodPut && r.URL.Path == "/v1/token":
			io.WriteString(w, "LTOKEN")
		case r.URL.Path == "/latest/meta-data/public-ipv4":
			if awsTok {
				io.WriteString(w, "54.1.2.3")
			} else {
				io.WriteString(w, "119.28.1.1") // the token-less EC2-style trees
			}
		case r.URL.Path == "/latest/meta-data/eipv4":
			io.WriteString(w, "47.1.2.3")
		case r.URL.Path == "/latest/meta-data/network/interfaces/macs/":
			io.WriteString(w, "0a:bb:cc:dd:ee:ff/\n")
		case r.URL.Path == "/latest/meta-data/network/interfaces/macs/0a:bb:cc:dd:ee:ff/ipv6s":
			io.WriteString(w, "2600:1f18::1\n2600:1f18::2\nfe80::1\n")
		case r.URL.Path == "/computeMetadata/v1/instance/network-interfaces/":
			if r.Header.Get("Metadata-Flavor") != "Google" {
				w.WriteHeader(403)
				return
			}
			io.WriteString(w, `[{"accessConfigs":[{"externalIp":"34.1.2.3"}],"ipv6s":["2600:1900::1"],"ipv6AccessConfigs":[{"externalIpv6":"2600:1900::1"}],"ip":"10.128.0.2"}]`)
		case r.URL.Path == "/metadata/instance/network":
			if r.Header.Get("Metadata") != "true" {
				w.WriteHeader(400)
				return
			}
			io.WriteString(w, `{"interface":[{"ipv4":{"ipAddress":[{"privateIpAddress":"10.0.0.4","publicIpAddress":"20.1.2.3"}]},"ipv6":{"ipAddress":[{"privateIpAddress":"2603:1030::5"}]}}]}`)
		case r.URL.Path == "/conf":
			io.WriteString(w, `{"commercial_type":"DEV1-S","public_ip":{"address":"51.1.2.3"},"public_ips":[{"address":"51.1.2.3","family":"inet"},{"address":"2001:bc8::1","family":"inet6"}],"ipv6":{"address":"2001:bc8::1"}}`)
		case r.URL.Path == "/metadata/v1/interfaces/public/0/ipv4/address":
			io.WriteString(w, "165.1.2.3")
		case r.URL.Path == "/metadata/v1/interfaces/public/0/ipv6/address":
			io.WriteString(w, "2604:a880::1")
		case r.URL.Path == "/hetzner/v1/metadata/public-ipv4":
			io.WriteString(w, "95.1.2.3")
		case r.URL.Path == "/v1/interfaces/0/ipv4/address":
			io.WriteString(w, "45.1.2.3")
		case r.URL.Path == "/v1/interfaces/0/ipv6/address":
			io.WriteString(w, "2001:19f0::1")
		case r.URL.Path == "/v1/network":
			if r.Header.Get("Metadata-Token") != "LTOKEN" {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ipv4": map[string]any{"public": []string{"172.105.1.2/32"}, "private": []string{"192.168.1.2/17"}},
				"ipv6": map[string]any{"slaac": "2600:3c03::f03c:91ff:fe24:3a2f/64", "link_local": "fe80::1/64", "ranges": []string{}},
			})
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
		name string
		dmi  dmi
		want []string
	}{
		{"AWS", dmi{vendor: "Amazon EC2", product: "t4g.micro"}, []string{"54.1.2.3", "2600:1f18::1", "2600:1f18::2"}},
		{"GCE", dmi{vendor: "Google"}, []string{"34.1.2.3", "2600:1900::1"}},
		{"Azure", dmi{assetTag: azureAssetTag}, []string{"20.1.2.3", "2603:1030::5"}},
		{"Alibaba", dmi{vendor: "Alibaba Cloud"}, []string{"47.1.2.3", "119.28.1.1", "2600:1f18::1", "2600:1f18::2"}},
		{"Tencent", dmi{vendor: "Tencent Cloud"}, []string{"119.28.1.1", "2600:1f18::1", "2600:1f18::2"}},
		{"Scaleway", dmi{vendor: "Scaleway"}, []string{"51.1.2.3", "2001:bc8::1"}},
		{"DigitalOcean", dmi{vendor: "DigitalOcean"}, []string{"165.1.2.3", "2604:a880::1"}},
		{"Hetzner", dmi{vendor: "Hetzner", product: "vServer"}, []string{"95.1.2.3"}},
		{"Vultr", dmi{vendor: "Vultr", product: "VC2"}, []string{"45.1.2.3", "2001:19f0::1"}},
		{"Linode", dmi{vendor: "Linode", product: "Compute Instance"}, []string{"172.105.1.2", "2600:3c03::f03c:91ff:fe24:3a2f"}},
		{"OpenStack", dmi{vendor: "OpenStack Foundation", product: "OpenStack Nova"}, []string{"119.28.1.1"}},
	}
	for _, c := range cases {
		cl := detectCloud(c.dmi)
		if cl == nil || cl.public == nil {
			t.Errorf("%s: no public lookup", c.name)
			continue
		}
		got := publicAddresses(cl.public(context.Background()))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if cl := detectCloud(dmi{assetTag: "OracleCloud.com"}); cl == nil || cl.public != nil {
		t.Error("OCI: expected no public lookup (not published)")
	}
	// the lookup is off: nothing is asked, nothing reported
	if _, public := cloudInfo(Options{CloudMetadata: false}); public != nil {
		t.Errorf("lookup off but got %v", public)
	}
	_ = strings.TrimSpace
}
