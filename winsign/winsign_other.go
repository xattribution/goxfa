//go:build !windows

package winsign

type handle struct{}

// Certificates lists the certificates in the user's store that have a
// private key.
func Certificates() ([]*Cert, error) { return nil, ErrUnsupported }

// Signer opens the certificate's private key for signing.
func (c *Cert) Signer() (Signer, error) { return nil, ErrUnsupported }
