package edgeconfig

// cloudflareRanges is Cloudflare's published edge ranges
// (https://www.cloudflare.com/ips-v4 and /ips-v6), shipped with the API
// release and never fetched at runtime (spec D8). Checked 2026-11-17
// against the list Cloudflare has published unchanged since 2023. Refresh
// it at release time; a change here changes every Cloudflare route's
// config hash, so edges re-apply on the next hello.
var cloudflareRanges = []string{
	"103.21.244.0/22",
	"103.22.200.0/22",
	"103.31.4.0/22",
	"104.16.0.0/13",
	"104.24.0.0/14",
	"108.162.192.0/18",
	"131.0.72.0/22",
	"141.101.64.0/18",
	"162.158.0.0/15",
	"172.64.0.0/13",
	"173.245.48.0/20",
	"188.114.96.0/20",
	"190.93.240.0/20",
	"197.234.240.0/22",
	"198.41.128.0/17",
	"2400:cb00::/32",
	"2405:8100::/32",
	"2405:b500::/32",
	"2606:4700::/32",
	"2803:f800::/32",
	"2a06:98c0::/29",
	"2c0f:f248::/32",
}

// CloudflareRanges returns a copy of the trusted Cloudflare ranges.
func CloudflareRanges() []string {
	return append([]string(nil), cloudflareRanges...)
}
