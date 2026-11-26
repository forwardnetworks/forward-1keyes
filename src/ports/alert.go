package ports

import "github.com/forwardnetworks/forward-1keyes/src/domain"

// Alert is what the listener hands on from a request.
type Alert = domain.Alert

// ParseAlert reads the three fields of an alert body.
func ParseAlert(body []byte) (Alert, error) { return domain.ParseAlert(body) }

// ValidateListenAddress accepts ip:port with a loopback address literal.
func ValidateListenAddress(address string) (string, error) {
	return domain.ValidateListenAddress(address)
}
