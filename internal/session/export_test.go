package session

import "github.com/ndrwl/guildhall/internal/endpoint"

// SessionProtocol is the session control protocol, for the shared endpoint contract.
func SessionProtocol() endpoint.Protocol { return sessionProtocol }
