package util

import (
	"testing"

	"tailscale.com/tailcfg"
	"tailscale.com/util/dnsname"
)

func TestGivenNameFromHostinfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		hostname string
		model    string
		want     string
	}{
		{
			name:     "ios_localhost_resolves_to_model",
			hostname: "localhost",
			model:    "iPhone16,1",
			want:     "iphone-15-pro",
		},
		{
			name:     "ipad_localhost_resolves_to_model",
			hostname: "localhost",
			model:    "iPad16,3",
			want:     "ipad-pro-11-inch-m4",
		},
		{
			name:     "unknown_model_falls_back_to_sanitised_identifier",
			hostname: "localhost",
			model:    "iPhone99,9",
			want:     "iphone999",
		},
		{
			name:     "localhost_without_model_stays_localhost",
			hostname: "localhost",
			model:    "",
			want:     "localhost",
		},
		{
			name:     "localhost_match_is_case_insensitive",
			hostname: "LOCALHOST",
			model:    "iPhone16,1",
			want:     "iphone-15-pro",
		},
		{
			// A real hostname must win over DeviceModel, otherwise every
			// Apple device would be named after its model.
			name:     "real_hostname_wins_over_model",
			hostname: "Kota's MacBook Pro.local",
			model:    "MacBookPro18,3",
			want:     "kotas-macbook-pro",
		},
		{
			name:     "non_apple_hostname_is_just_sanitised",
			hostname: "my-server",
			model:    "",
			want:     "my-server",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hi := &tailcfg.Hostinfo{Hostname: tt.hostname, DeviceModel: tt.model}
			if got := GivenNameFromHostinfo(tt.hostname, hi); got != tt.want {
				t.Errorf("GivenNameFromHostinfo(%q, %q) = %q, want %q",
					tt.hostname, tt.model, got, tt.want)
			}
		})
	}
}

// TestGivenNameFromHostinfoMatchesSanitize pins the contract that this
// helper only ever diverges from dnsname.SanitizeHostname for generic
// hostnames. Anything else must pass through untouched, so upstream
// changes to SanitizeHostname keep flowing through.
func TestGivenNameFromHostinfoMatchesSanitize(t *testing.T) {
	t.Parallel()

	for _, hostname := range []string{
		"my-server",
		"Kota's MacBook Pro",
		"WIN-DESKTOP-01",
		"host_with_underscores",
		"",
	} {
		want := dnsname.SanitizeHostname(hostname)

		hi := &tailcfg.Hostinfo{Hostname: hostname, DeviceModel: "iPhone16,1"}
		if got := GivenNameFromHostinfo(hostname, hi); got != want {
			t.Errorf("GivenNameFromHostinfo(%q) = %q, want SanitizeHostname result %q",
				hostname, got, want)
		}

		if got := GivenNameFromHostinfo(hostname, nil); got != want {
			t.Errorf("GivenNameFromHostinfo(%q, nil) = %q, want %q", hostname, got, want)
		}
	}
}
