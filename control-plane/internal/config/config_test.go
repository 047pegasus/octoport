package config

import "testing"

func TestIsReservedSubdomain(t *testing.T) {
	c := &Config{ReservedSubdomains: []string{"www", "octoport", "Portainer", "control-plane-octoport"}}
	cases := []struct {
		sub  string
		want bool
	}{
		{"portainer", true},           // case-insensitive
		{"Portainer", true},
		{"Portainer", true},
		{"www", true},
		{"octoport", true},
		{"control-plane-octoport", true},
		{"traefik", false},            // not in this list
		{"my-app", false},             // ordinary tunnel label
		{"o7ndcbri", false},
	}
	for _, tc := range cases {
		if got := c.IsReservedSubdomain(tc.sub); got != tc.want {
			t.Errorf("IsReservedSubdomain(%q) = %v want %v", tc.sub, got, tc.want)
		}
	}
}