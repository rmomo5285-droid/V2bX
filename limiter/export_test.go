package limiter

import "net/netip"

// AllowLoopbackForTest is linked only into tests. Production nodes always retain
// the private-address block. Call before starting cores and restore after Close.
func AllowLoopbackForTest() func() {
	previous := safeDestinationExceptions
	safeDestinationExceptions = append(append([]netip.Prefix(nil), previous...), netip.MustParsePrefix("127.0.0.1/32"))
	return func() { safeDestinationExceptions = previous }
}
