package imageproc

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	errors "github.com/Laisky/errors/v2"
	"github.com/stretchr/testify/require"
)

// TestURLFetchSecurityAddressPolicy covers non-public destinations that the
// standard library's IsPrivate check alone does not reject.
func TestURLFetchSecurityAddressPolicy(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"0.1.2.3", "100.64.0.1", "100.100.100.200", "100.127.255.254",
		"192.0.0.1", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "240.1.2.3",
		"127.0.0.1", "169.254.169.254", "10.1.2.3", "224.0.0.1", "::1", "fe80::1", "fd00::1",
		"::ffff:100.100.100.200", "64:ff9b::a00:1", "2001:db8::1", "2002:7f00:1::", "3fff::1",
	} {
		t.Run(value, func(t *testing.T) {
			require.False(t, isPublicIP(net.ParseIP(value)), "must not permit a non-public image destination")
		})
	}
	for _, value := range []string{"1.1.1.1", "8.8.8.8", "100.63.255.254", "100.128.0.1", "2606:4700:4700::1111"} {
		require.True(t, isPublicIP(net.ParseIP(value)), "ordinary public addresses remain usable: %s", value)
	}
}

// TestURLFetchSecurityBlocksBeforeDial proves that a blocked DNS answer never
// reaches even the injected dialer. No metadata endpoint is actually contacted.
func TestURLFetchSecurityBlocksBeforeDial(t *testing.T) {
	for _, answers := range [][]net.IP{
		{net.ParseIP("100.100.100.200")},
		{net.ParseIP("1.1.1.1"), net.ParseIP("100.100.100.200")},
		{},
	} {
		var dials atomic.Int32
		fetcher := NewURLFetcher(URLFetchConfig{
			AllowHTTP:  true,
			LookupHost: staticResolver(answers...),
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("unexpected synthetic dial")
			},
		})
		_, err := fetcher.Fetch(context.Background(), "http://image.example.test/image.png")
		require.ErrorIs(t, err, ErrURLBlocked)
		require.Zero(t, dials.Load())
	}
}
