package util

// appleModelNames maps Apple internal device model identifiers to
// DNS-safe friendly hostnames.
//
// Apple model identifiers use the format ProductFamily + generation + comma +
// variant (e.g. "iPhone16,1"). This table maps them to lowercase hyphenated
// names suitable as DNS hostnames.
//
// Sources:
//   - https://www.theiphonewiki.com/wiki/Models
//   - https://gist.github.com/adamawolf/3048717
//
// To expand this table: see hscontrol/util/apple_devices_full.go (generated).
// Entries here are hand-curated; the generated file contains the full list.
var appleModelNames = map[string]string{
	// iPhone 15 series (2023)
	"iPhone16,1": "iphone-15-pro",
}

// lookupAppleDeviceModel returns the DNS-safe hostname for a known Apple
// model identifier, or empty string if the identifier is not in the table.
func lookupAppleDeviceModel(model string) string {
	return appleModelNames[model]
}
