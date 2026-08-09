package types

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/tailcfg"
	"tailscale.com/types/dnstype"
)

// writeConfig writes cfg to a temporary config.yaml and loads it into viper,
// returning the path so the test can rewrite it and reload.
func writeConfig(t *testing.T, cfg string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0o600))
	require.NoError(t, LoadConfig(path, true))

	return path
}

const baseConfig = `
server_url: https://headscale.example.com
listen_addr: 0.0.0.0:8080
noise:
  private_key_path: /tmp/noise.key
dns:
  magic_dns: true
  base_domain: example.com
  nameservers:
    global:
      - 1.1.1.1
`

// TestBuildTailcfgDNSConfigErrorsInsteadOfFatal covers the reload path's most
// dangerous input: MagicDNS on with no base domain. Before, this called
// log.Fatal and would have taken the daemon down on SIGHUP.
func TestBuildTailcfgDNSConfigErrorsInsteadOfFatal(t *testing.T) {
	_, err := BuildTailcfgDNSConfig(DNSConfig{MagicDNS: true, BaseDomain: ""})
	require.ErrorIs(t, err, ErrBaseDomainRequired)
}

// TestReloadConfigFilePicksUpDNSChanges is the core of the SIGHUP reload: an
// edit to the dns section on disk must be visible after re-reading.
func TestReloadConfigFilePicksUpDNSChanges(t *testing.T) {
	path := writeConfig(t, baseConfig)

	before, err := DNSConfigFromViper()
	require.NoError(t, err)
	assert.Equal(t, []string{"1.1.1.1"}, before.Nameservers.Global)
	assert.Empty(t, before.SearchDomains)

	require.NoError(t, os.WriteFile(path, []byte(`
server_url: https://headscale.example.com
listen_addr: 0.0.0.0:8080
noise:
  private_key_path: /tmp/noise.key
dns:
  magic_dns: true
  base_domain: example.com
  search_domains:
    - corp.example.com
  nameservers:
    global:
      - 9.9.9.9
    split:
      internal.example.com:
        - 10.0.0.1
`), 0o600))

	require.NoError(t, ReloadConfigFile())

	after, err := DNSConfigFromViper()
	require.NoError(t, err)
	assert.Equal(t, []string{"9.9.9.9"}, after.Nameservers.Global)
	assert.Equal(t, []string{"corp.example.com"}, after.SearchDomains)
	assert.Equal(t, map[string][]string{"internal.example.com": {"10.0.0.1"}}, after.Nameservers.Split)
}

// TestReloadConfigFilePicksUpOIDCRestrictions guards the security-relevant
// half: removing a user from the allow-list must actually be observable.
func TestReloadConfigFilePicksUpOIDCRestrictions(t *testing.T) {
	path := writeConfig(t, baseConfig+`
oidc:
  issuer: https://idp.example.com
  client_id: headscale
  allowed_users:
    - alice@example.com
    - bob@example.com
`)

	before := OIDCRestrictionsFromViper()
	assert.Equal(t, []string{"alice@example.com", "bob@example.com"}, before.Users)

	require.NoError(t, os.WriteFile(path, []byte(baseConfig+`
oidc:
  issuer: https://idp.example.com
  client_id: headscale
  allowed_users:
    - alice@example.com
  allowed_domains:
    - example.com
`), 0o600))

	require.NoError(t, ReloadConfigFile())

	after := OIDCRestrictionsFromViper()
	assert.Equal(t, []string{"alice@example.com"}, after.Users)
	assert.Equal(t, []string{"example.com"}, after.Domains)
	assert.False(t, before.Equal(after))
}

// TestOIDCRestrictionsRoundTrip checks the accessors used by the reload path
// and by doOIDCAuthorization.
func TestOIDCRestrictionsRoundTrip(t *testing.T) {
	cfg := &OIDCConfig{
		AllowedDomains: []string{"old.example.com"},
		AllowedUsers:   []string{"alice@example.com", "bob@example.com"},
	}

	assert.Equal(t, []string{"bob@example.com"}, []string{cfg.Restrictions().Users[1]})

	cfg.SetRestrictions(OIDCRestrictions{
		Domains: []string{"new.example.com"},
		Users:   []string{"alice@example.com"},
		Groups:  []string{"admins"},
	})

	got := cfg.Restrictions()
	assert.Equal(t, []string{"new.example.com"}, got.Domains)
	assert.Equal(t, []string{"alice@example.com"}, got.Users)
	assert.Equal(t, []string{"admins"}, got.Groups)

	// The struct fields must be updated too: the OIDC provider holds a pointer
	// to this same struct.
	assert.Equal(t, []string{"alice@example.com"}, cfg.AllowedUsers)
}

// TestApplyMagicDNSRoutesReinjectsReverseZones covers the landmine that a
// rebuilt tailcfg DNS config loses the MagicDNS reverse-DNS routes unless they
// are re-applied. Dropping them makes clients clobber /etc/resolv.conf.
func TestApplyMagicDNSRoutesReinjectsReverseZones(t *testing.T) {
	v4 := netip.MustParsePrefix("100.64.0.0/10")
	cfg := &Config{PrefixV4: &v4}

	dnsCfg, err := BuildTailcfgDNSConfig(DNSConfig{
		MagicDNS:   true,
		BaseDomain: "example.com",
		Nameservers: Nameservers{
			Split: map[string][]string{"internal.example.com": {"10.0.0.1"}},
		},
	})
	require.NoError(t, err)

	splitRoutes := len(dnsCfg.Routes)
	require.Positive(t, splitRoutes, "split DNS should have produced routes")

	cfg.ApplyMagicDNSRoutes(dnsCfg)
	require.Greater(t, len(dnsCfg.Routes), splitRoutes, "magic DNS routes should have been added")

	var reverse int

	for name, resolvers := range dnsCfg.Routes {
		if name == "internal.example.com" {
			continue
		}

		reverse++
		// Empty non-nil slice, not nil: nil entries are dropped by
		// tailcfg.DNSConfig.Clone on the client.
		assert.NotNil(t, resolvers, "route %q must carry an empty non-nil resolver slice", name)
		assert.Empty(t, resolvers, "route %q must not carry resolvers", name)
	}

	assert.Positive(t, reverse)

	// Split DNS entries must survive.
	assert.Len(t, dnsCfg.Routes["internal.example.com"], 1)
}

// TestApplyMagicDNSRoutesNoopWhenMagicDNSDisabled makes sure we do not inject
// reverse zones when MagicDNS is off.
func TestApplyMagicDNSRoutesNoopWhenMagicDNSDisabled(t *testing.T) {
	v4 := netip.MustParsePrefix("100.64.0.0/10")
	cfg := &Config{PrefixV4: &v4}

	dnsCfg, err := BuildTailcfgDNSConfig(DNSConfig{MagicDNS: false, BaseDomain: "example.com"})
	require.NoError(t, err)

	cfg.ApplyMagicDNSRoutes(dnsCfg)
	assert.Empty(t, dnsCfg.Routes)

	cfg.ApplyMagicDNSRoutes(nil) // must not panic
}

// TestSetTailcfgDNSConfigIsVisibleToClone checks the swap used by the reload.
func TestSetTailcfgDNSConfigIsVisibleToClone(t *testing.T) {
	cfg := &Config{TailcfgDNSConfig: &tailcfg.DNSConfig{Domains: []string{"old.example.com"}}}

	cfg.SetDNSConfig(DNSConfig{}, &tailcfg.DNSConfig{
		Domains:   []string{"new.example.com"},
		Resolvers: []*dnstype.Resolver{{Addr: "9.9.9.9"}},
	})

	got := cfg.CloneTailcfgDNSConfig()
	require.NotNil(t, got)
	assert.Equal(t, []string{"new.example.com"}, got.Domains)
	require.Len(t, got.Resolvers, 1)
	assert.Equal(t, "9.9.9.9", got.Resolvers[0].Addr)
}

// TestDebugCloneDoesNotShareDNSConfig makes sure debug serialisation walks a
// copy, not the live config that the reload path swaps under it.
func TestDebugCloneDoesNotShareDNSConfig(t *testing.T) {
	cfg := &Config{
		BaseDomain:       "example.com",
		TailcfgDNSConfig: &tailcfg.DNSConfig{Domains: []string{"example.com"}},
	}

	clone := cfg.DebugClone()
	require.NotNil(t, clone.TailcfgDNSConfig)
	assert.Equal(t, cfg.BaseDomain, clone.BaseDomain)
	assert.NotSame(t, cfg.TailcfgDNSConfig, clone.TailcfgDNSConfig)

	cfg.SetDNSConfig(DNSConfig{}, &tailcfg.DNSConfig{Domains: []string{"changed.example.com"}})
	assert.Equal(t, []string{"example.com"}, clone.TailcfgDNSConfig.Domains)
}
