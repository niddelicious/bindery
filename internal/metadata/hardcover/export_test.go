package hardcover

import "net/http"

// WithTransportForTest lets the external test package point a real client at a
// counting transport without exporting the field in production code.
func (c *Client) WithTransportForTest(rt http.RoundTripper) *Client {
	clone := *c
	clone.http = &http.Client{Transport: rt}
	return &clone
}
