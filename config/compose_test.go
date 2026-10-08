package config

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// published returns the address of the host and the port a port of a service
// of a compose is published on, of its short syntax ([HOST:]HOST_PORT:PORT
// [/PROTOCOL], the host of IPv6 in brackets) or its long one (host_ip,
// published); an empty host is every interface.
func published(t *testing.T, port any) (host, hostPort string) {
	t.Helper()
	switch port := port.(type) {
	case string:
		port, _, _ = strings.Cut(port, "/")
		if rest, ok := strings.CutPrefix(port, "["); ok {
			host, rest, _ = strings.Cut(rest, "]")
			parts := strings.Split(strings.TrimPrefix(rest, ":"), ":")
			return host, parts[0]
		}
		parts := strings.Split(port, ":")
		switch len(parts) {
		case 3:
			return parts[0], parts[1]
		case 2:
			return "", parts[0]
		}
		return "", "" // the port of the container only: a port of the host at random
	case map[string]any:
		host, _ := port["host_ip"].(string)
		return host, fmt.Sprint(port["published"])
	}
	t.Fatalf("a port of an unknown syntax: %#v", port)
	return "", ""
}

// The compose publishes its ports on the loopback of the host only: Docker
// publishes them past the firewall of the host (ufw), and the controller
// believes the X-Forwarded-* of the private range of Docker, by which ngrok on
// the host reaches it (ui.trusted_proxies); another machine goes through an
// SSH tunnel.
func TestCompose_PublishesOnTheLoopbackOnly(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "deploy", "docker", "docker-compose.yml"))
	require.NoError(t, err)
	var compose struct {
		Services map[string]struct {
			Ports []any `yaml:"ports"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(content, &compose))
	require.NotEmpty(t, compose.Services)

	ports := map[string]string{} // the port of the host: the service
	for name, service := range compose.Services {
		for _, port := range service.Ports {
			host, hostPort := published(t, port)
			addr, err := netip.ParseAddr(host)
			require.NoError(t, err, "service %s: port %v published on every interface", name, port)
			require.True(t, addr.IsLoopback(), "service %s: port %v published off the loopback", name, port)
			ports[hostPort] = name
		}
	}
	require.Equal(t, map[string]string{"8082": "controller", "5900": "mt5", "5432": "postgres"}, ports,
		"the dashboard on 127.0.0.1:8082, where ngrok on the host tunnels to; VNC and PostgreSQL by an SSH tunnel")
}

func TestPublished(t *testing.T) {
	for port, want := range map[any][2]string{
		"8082:8080":             {"", "8082"},
		"127.0.0.1:8082:8080":   {"127.0.0.1", "8082"},
		"0.0.0.0:5432:5432/tcp": {"0.0.0.0", "5432"},
		"[::1]:5900:5900":       {"::1", "5900"},
		"5900":                  {"", ""},
	} {
		host, hostPort := published(t, port)
		require.Equal(t, want, [2]string{host, hostPort}, port)
	}
	host, hostPort := published(t, map[string]any{"target": 8080, "published": "8082", "host_ip": "127.0.0.1"})
	require.Equal(t, [2]string{"127.0.0.1", "8082"}, [2]string{host, hostPort})
}
