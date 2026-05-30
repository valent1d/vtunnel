// Package orbstack discovers running OrbStack containers so vtunnel can expose
// them. OrbStack gives every container a host-reachable domain (<name>.orb.local)
// plus any custom domains declared via the dev.orbstack.domains label, so a
// vtunnel route only needs to point its target at that domain — OrbStack routes
// the rest by Host header.
//
// Discovery shells out to the docker CLI (the same pattern vtunnel uses for
// cloudflared); the Runner is injectable so tests feed canned output.
package orbstack

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

const (
	domainsLabel        = "dev.orbstack.domains"
	httpPortLabel       = "dev.orbstack.http-port"
	composeProjectLabel = "com.docker.compose.project"
	composeServiceLabel = "com.docker.compose.service"
)

// Runner executes a docker command and returns its combined output.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

func execRunner(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

// Client lists OrbStack containers via docker.
type Client struct {
	run Runner
}

// New returns a Client. A nil runner shells out to the real docker binary.
func New(run Runner) Client {
	if run == nil {
		run = execRunner
	}
	return Client{run: run}
}

// Container is a running OrbStack container and the bits vtunnel needs to expose it.
type Container struct {
	Name           string
	Image          string
	OrbDomain      string // <name>.orb.local
	CustomDomains  []string
	HTTPPort       int // from dev.orbstack.http-port; 0 when unset
	ExposedPorts   []int
	ComposeProject string
	ComposeService string
	HTTP           bool // reachable over HTTP (exposable through vtunnel)
}

// Target is the proxy upstream for this container: its OrbStack domain over
// HTTP. OrbStack serves the container's HTTP port on this domain, so the port
// never needs to appear in the URL.
func (c Container) Target() string {
	return "http://" + c.OrbDomain
}

// DefaultSubdomain is the public subdomain proposed for this container: the
// first label of its first custom domain (doli23.local -> "doli23") when set,
// otherwise the sanitized container name.
func (c Container) DefaultSubdomain() string {
	if len(c.CustomDomains) > 0 {
		if label := sanitizeSubdomain(firstLabel(c.CustomDomains[0])); label != "" {
			return label
		}
	}
	return sanitizeSubdomain(c.Name)
}

// Available reports whether docker is talking to OrbStack (its context is
// selected). A false result is a hint, not a hard error: OrbStack is the only
// runtime that publishes .orb.local domains.
func Available(ctx context.Context, run Runner) bool {
	if run == nil {
		run = execRunner
	}
	out, err := run(ctx, "context", "show")
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(string(out)), "orbstack")
}

// List returns the running containers, sorted by name.
func (c Client) List(ctx context.Context) ([]Container, error) {
	out, err := c.run(ctx, "ps", "--no-trunc", "--quiet")
	if err != nil {
		return nil, fmt.Errorf("docker ps: %s", strings.TrimSpace(string(out)))
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil, nil
	}

	inspectOut, err := c.run(ctx, append([]string{"inspect"}, ids...)...)
	if err != nil {
		return nil, fmt.Errorf("docker inspect: %s", strings.TrimSpace(string(inspectOut)))
	}
	return parseInspect(inspectOut)
}

// inspectResult captures only the docker inspect fields vtunnel needs; unknown
// fields are ignored by encoding/json.
type inspectResult struct {
	Name   string `json:"Name"`
	Config struct {
		Image        string              `json:"Image"`
		Labels       map[string]string   `json:"Labels"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
	} `json:"Config"`
}

func parseInspect(data []byte) ([]Container, error) {
	var results []inspectResult
	if err := json.Unmarshal(data, &results); err != nil {
		return nil, fmt.Errorf("parse docker inspect: %w", err)
	}

	containers := make([]Container, 0, len(results))
	for _, result := range results {
		name := strings.TrimPrefix(strings.TrimSpace(result.Name), "/")
		if name == "" {
			continue
		}
		container := Container{
			Name:           name,
			Image:          result.Config.Image,
			OrbDomain:      name + ".orb.local",
			CustomDomains:  splitDomains(result.Config.Labels[domainsLabel]),
			HTTPPort:       atoiSafe(result.Config.Labels[httpPortLabel]),
			ExposedPorts:   exposedPorts(result.Config.ExposedPorts),
			ComposeProject: result.Config.Labels[composeProjectLabel],
			ComposeService: result.Config.Labels[composeServiceLabel],
		}
		container.HTTP = isHTTP(container.HTTPPort, container.ExposedPorts)
		containers = append(containers, container)
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })
	return containers, nil
}

// FindContainer matches a container by exact name, OrbStack domain, or proposed
// subdomain so `vtunnel orbstack expose <ref>` is forgiving.
func FindContainer(containers []Container, ref string) (Container, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Container{}, false
	}
	for _, container := range containers {
		if container.Name == ref || container.OrbDomain == ref || container.DefaultSubdomain() == ref {
			return container, true
		}
	}
	return Container{}, false
}

var commonHTTPPorts = map[int]bool{
	80: true, 443: true, 3000: true, 3001: true, 4000: true, 5000: true,
	5173: true, 8000: true, 8080: true, 8081: true, 8443: true, 9000: true,
}

func isHTTP(httpPort int, exposed []int) bool {
	if httpPort > 0 {
		return true
	}
	for _, port := range exposed {
		if commonHTTPPorts[port] {
			return true
		}
	}
	return false
}

func exposedPorts(ports map[string]struct{}) []int {
	out := make([]int, 0, len(ports))
	for spec := range ports {
		// spec looks like "80/tcp".
		number := spec
		if i := strings.IndexByte(spec, '/'); i >= 0 {
			number = spec[:i]
		}
		if port := atoiSafe(number); port > 0 {
			out = append(out, port)
		}
	}
	sort.Ints(out)
	return out
}

func splitDomains(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	domains := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			domains = append(domains, trimmed)
		}
	}
	return domains
}

func firstLabel(domain string) string {
	domain = strings.TrimSpace(domain)
	if i := strings.IndexByte(domain, '.'); i >= 0 {
		return domain[:i]
	}
	return domain
}

// sanitizeSubdomain reduces a string to a valid DNS label: lowercase
// [a-z0-9-], with runs of other characters collapsed to a single dash and the
// result trimmed of leading/trailing dashes.
func sanitizeSubdomain(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func atoiSafe(value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return n
}
