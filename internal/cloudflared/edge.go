package cloudflared

import (
	"os"
	"sort"
	"strconv"
	"strings"
)

// EdgeConnection is a single live connection from cloudflared to a Cloudflare
// edge data center, parsed from the cloudflared log.
type EdgeConnection struct {
	Index    int    // connIndex
	IP       string // edge IP
	Location string // colo code, e.g. "dfw08"
	City     string // human city for the colo, e.g. "Dallas" (empty if unknown)
	Protocol string // quic, http2, ...
}

// EdgeStatus is a best-effort snapshot of cloudflared's edge connectivity,
// reconstructed from its log file.
type EdgeStatus struct {
	Version     string           // cloudflared version
	MetricsAddr string           // address of the cloudflared metrics server, e.g. "127.0.0.1:20241"
	Connections []EdgeConnection // current connections, ordered by index
}

// ParseEdgeStatus reads the cloudflared log file and extracts the current edge
// connection status. Missing fields are left empty.
func ParseEdgeStatus(logPath string) (EdgeStatus, error) {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return EdgeStatus{}, err
	}
	return parseEdgeStatus(string(data)), nil
}

func parseEdgeStatus(content string) EdgeStatus {
	var status EdgeStatus
	active := map[int]EdgeConnection{}

	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.Contains(line, "Registered tunnel connection"):
			fields := logFields(line)
			idx, err := strconv.Atoi(fields["connIndex"])
			if err != nil {
				continue
			}
			active[idx] = EdgeConnection{
				Index:    idx,
				IP:       fields["ip"],
				Location: fields["location"],
				City:     coloCity(fields["location"]),
				Protocol: fields["protocol"],
			}
		case strings.Contains(line, "Unregistered tunnel connection"):
			if idx, err := strconv.Atoi(logFields(line)["connIndex"]); err == nil {
				delete(active, idx)
			}
		case strings.Contains(line, "Starting metrics server on"):
			if addr := after(line, "Starting metrics server on "); addr != "" {
				status.MetricsAddr = strings.Fields(addr)[0]
			}
		case strings.Contains(line, " Version "):
			if v := after(line, " Version "); v != "" {
				status.Version = strings.Fields(v)[0]
			}
		}
	}

	for _, conn := range active {
		status.Connections = append(status.Connections, conn)
	}
	sort.Slice(status.Connections, func(i, j int) bool {
		return status.Connections[i].Index < status.Connections[j].Index
	})
	return status
}

// logFields parses logfmt-style "key=value" tokens from a log line.
func logFields(line string) map[string]string {
	fields := map[string]string{}
	for _, token := range strings.Fields(line) {
		if key, value, ok := strings.Cut(token, "="); ok {
			fields[key] = value
		}
	}
	return fields
}

func after(line, marker string) string {
	if i := strings.Index(line, marker); i >= 0 {
		return strings.TrimSpace(line[i+len(marker):])
	}
	return ""
}

// coloCity maps a Cloudflare colo code (e.g. "dfw08") to a city name via its
// 3-letter IATA prefix. Unknown colos return "".
func coloCity(location string) string {
	if len(location) < 3 {
		return ""
	}
	return coloCities[strings.ToLower(location[:3])]
}

var coloCities = map[string]string{
	"dfw": "Dallas", "den": "Denver", "iad": "Ashburn", "lax": "Los Angeles",
	"sjc": "San Jose", "ord": "Chicago", "ewr": "Newark", "sea": "Seattle",
	"atl": "Atlanta", "mia": "Miami", "bos": "Boston", "phx": "Phoenix",
	"cdg": "Paris", "lhr": "London", "fra": "Frankfurt", "ams": "Amsterdam",
	"mad": "Madrid", "mrs": "Marseille", "mxp": "Milan", "zrh": "Zurich",
	"vie": "Vienna", "waw": "Warsaw", "arn": "Stockholm", "dub": "Dublin",
	"otp": "Bucharest", "muc": "Munich", "lis": "Lisbon", "bru": "Brussels",
	"sin": "Singapore", "nrt": "Tokyo", "hkg": "Hong Kong", "icn": "Seoul",
	"bom": "Mumbai", "syd": "Sydney", "gru": "São Paulo", "yyz": "Toronto",
	"yul": "Montreal", "scl": "Santiago", "jnb": "Johannesburg", "dxb": "Dubai",
}
