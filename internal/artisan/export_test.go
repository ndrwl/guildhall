package artisan

import "github.com/ndrwl/guildhall/internal/endpoint"

// ControlProtocol is the attempt control protocol, for the shared endpoint contract.
func ControlProtocol() endpoint.Protocol { return controlProtocol }
