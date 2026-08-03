package limiter

import "testing"

func TestCheckSafetyDestination(t *testing.T) {
	tests := []struct {
		name        string
		network     string
		destination string
		reject      bool
	}{
		{name: "private IPv4", network: "tcp", destination: "10.0.0.1:443", reject: true},
		{name: "shared IPv4", network: "udp", destination: "100.64.0.1:53", reject: true},
		{name: "documentation IPv4", network: "tcp", destination: "203.0.113.2:80", reject: true},
		{name: "unassigned protocol IPv4", network: "tcp", destination: "192.0.0.11:443", reject: true},
		{name: "IPv4 mapped loopback", network: "tcp", destination: "[::ffff:127.0.0.1]:80", reject: true},
		{name: "unique local IPv6", network: "udp", destination: "[fd00::1]:53", reject: true},
		{name: "documentation IPv6", network: "tcp", destination: "[2001:db8::1]:443", reject: true},
		{name: "local translation IPv6", network: "tcp", destination: "[64:ff9b:1::1]:443", reject: true},
		{name: "localhost name", network: "tcp", destination: "localhost:8080", reject: true},
		{name: "SMTP delivery", network: "tcp", destination: "mail.example.com:25", reject: true},
		{name: "UDP port 25", network: "udp", destination: "1.1.1.1:25", reject: false},
		{name: "public IPv4", network: "tcp", destination: "1.1.1.1:443", reject: false},
		{name: "public IPv6", network: "tcp", destination: "[2606:4700:4700::1111]:443", reject: false},
		{name: "globally reachable IANA anycast", network: "tcp", destination: "192.0.0.9:443", reject: false},
		{name: "globally reachable traversal anycast", network: "tcp", destination: "192.0.0.10:443", reject: false},
		{name: "domain", network: "tcp", destination: "www.bilibili.com:443", reject: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, rejected := CheckSafetyDestination(test.network, test.destination)
			if rejected != test.reject {
				t.Fatalf("CheckSafetyDestination(%q, %q) rejected=%v, want %v", test.network, test.destination, rejected, test.reject)
			}
		})
	}
}
