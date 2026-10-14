package webhook

import (
	"errors"
	"net"
	"net/http"
	"syscall"
	"time"
)

// errPrivateAddress is returned when a webhook resolves to a non-public IP.
var errPrivateAddress = errors.New("webhook: destination address is not publicly routable")

// NewHTTPClient returns a client for webhook delivery. Webhook URLs are
// customer-supplied, so unless allowPrivate is set the dialer refuses
// loopback, private, link-local and other non-public addresses. The check runs
// on the resolved IP at connect time, which also defeats DNS rebinding.
func NewHTTPClient(timeout time.Duration, allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				return errPrivateAddress
			}
			return nil
		}
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
		// Do not follow redirects: a redirect could point at an internal host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
