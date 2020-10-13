package rpcperms

import (
	"context"
	"fmt"
	"sync"

	"github.com/decred/dcrlnd/macaroons"
	"github.com/decred/dcrlnd/monitoring"
	"github.com/decred/slog"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	"google.golang.org/grpc"
	"gopkg.in/macaroon-bakery.v2/bakery"
)

// InterceptorChain is a struct that can be added to the running GRPC server,
// intercepting API calls. This is useful for logging, enforcing permissions
// etc.
type InterceptorChain struct {
	// noMacaroons should be set true if we don't want to check macaroons.
	noMacaroons bool

	// svc is the macaroon service used to enforce permissions in case
	// macaroons are used.
	svc *macaroons.Service

	// permissionMap is the permissions to enforce if macaroons are used.
	permissionMap map[string][]bakery.Op

	// rpcsLog is the logger used to log calles to the RPCs intercepted.
	rpcsLog slog.Logger

	sync.RWMutex
}

// NewInterceptorChain creates a new InterceptorChain.
func NewInterceptorChain(log slog.Logger, noMacaroons bool) *InterceptorChain {
	return &InterceptorChain{
		noMacaroons:   noMacaroons,
		permissionMap: make(map[string][]bakery.Op),
		rpcsLog:       log,
	}
}

// AddMacaroonService adds a macaroon service to the interceptor. After this is
// done every RPC call made will have to pass a valid macaroon to be accepted.
func (r *InterceptorChain) AddMacaroonService(svc *macaroons.Service) {
	r.Lock()
	defer r.Unlock()

	r.svc = svc
}

// AddPermission adds a new macaroon rule for the given method.
func (r *InterceptorChain) AddPermission(method string, ops []bakery.Op) error {
	r.Lock()
	defer r.Unlock()

	if _, ok := r.permissionMap[method]; ok {
		return fmt.Errorf("detected duplicate macaroon constraints "+
			"for path: %v", method)
	}

	r.permissionMap[method] = ops
	return nil
}

// Permissions returns the current set of macaroon permissions.
func (r *InterceptorChain) Permissions() map[string][]bakery.Op {
	r.RLock()
	defer r.RUnlock()

	// Make a copy under the read lock to avoid races.
	c := make(map[string][]bakery.Op)
	for k, v := range r.permissionMap {
		s := make([]bakery.Op, len(v))
		copy(s, v)
		c[k] = s
	}

	return c
}

// CreateServerOpts creates the GRPC server options that can be added to a GRPC
// server in order to add this InterceptorChain.
func (r *InterceptorChain) CreateServerOpts() []grpc.ServerOption {
	macUnaryInterceptors := []grpc.UnaryServerInterceptor{}
	macStrmInterceptors := []grpc.StreamServerInterceptor{}

	// We'll add the macaroon interceptors. If macaroons aren't disabled,
	// then these interceptors will enforce macaroon authentication.
	unaryInterceptor := r.macaroonUnaryServerInterceptor()
	macUnaryInterceptors = append(macUnaryInterceptors, unaryInterceptor)

	strmInterceptor := r.macaroonStreamServerInterceptor()
	macStrmInterceptors = append(macStrmInterceptors, strmInterceptor)

	// Get interceptors for Prometheus to gather gRPC performance metrics.
	// If monitoring is disabled, GetPromInterceptors() will return empty
	// slices.
	promUnaryInterceptors, promStrmInterceptors :=
		monitoring.GetPromInterceptors()

	// Concatenate the slices of unary and stream interceptors respectively.
	unaryInterceptors := append(macUnaryInterceptors, promUnaryInterceptors...)
	strmInterceptors := append(macStrmInterceptors, promStrmInterceptors...)

	// We'll also add our logging interceptors as well, so we can
	// automatically log all errors that happen during RPC calls.
	unaryInterceptors = append(
		unaryInterceptors, errorLogUnaryServerInterceptor(r.rpcsLog),
	)
	strmInterceptors = append(
		strmInterceptors, errorLogStreamServerInterceptor(r.rpcsLog),
	)

	// Create server options from the interceptors we just set up.
	chainedUnary := grpc_middleware.WithUnaryServerChain(
		unaryInterceptors...,
	)
	chainedStream := grpc_middleware.WithStreamServerChain(
		strmInterceptors...,
	)
	serverOpts := []grpc.ServerOption{chainedUnary, chainedStream}

	return serverOpts
}

// errorLogUnaryServerInterceptor is a simple UnaryServerInterceptor that will
// automatically log any errors that occur when serving a client's unary
// request.
func errorLogUnaryServerInterceptor(logger slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (interface{}, error) {

		resp, err := handler(ctx, req)
		if err != nil {
			// TODO(roasbeef): also log request details?
			logger.Errorf("[%v]: %v", info.FullMethod, err)
		}

		return resp, err
	}
}

// errorLogStreamServerInterceptor is a simple StreamServerInterceptor that
// will log any errors that occur while processing a client or server streaming
// RPC.
func errorLogStreamServerInterceptor(logger slog.Logger) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream,
		info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {

		err := handler(srv, ss)
		if err != nil {
			logger.Errorf("[%v]: %v", info.FullMethod, err)
		}

		return err
	}
}

// macaroonUnaryServerInterceptor is a GRPC interceptor that checks whether the
// request is authorized by the included macaroons.
func (r *InterceptorChain) macaroonUnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (interface{}, error) {

		// If noMacaroons are set, we'll always allow the call.
		if r.noMacaroons {
			return handler(ctx, req)
		}

		r.RLock()
		svc := r.svc
		r.RUnlock()

		// If the macaroon service is not yet active, allow the call.
		// THis means that the wallet has not yet been unlocked, and we
		// allow calls to the WalletUnlockerService.
		if svc == nil {
			return handler(ctx, req)
		}

		r.RLock()
		uriPermissions, ok := r.permissionMap[info.FullMethod]
		r.RUnlock()
		if !ok {
			return nil, fmt.Errorf("%s: unknown permissions "+
				"required for method", info.FullMethod)
		}

		// Find out if there is an external validator registered for
		// this method. Fall back to the internal one if there isn't.
		validator, ok := svc.ExternalValidators[info.FullMethod]
		if !ok {
			validator = svc
		}

		// Now that we know what validator to use, let it do its work.
		err := validator.ValidateMacaroon(
			ctx, uriPermissions, info.FullMethod,
		)
		if err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

// macaroonStreamServerInterceptor is a GRPC interceptor that checks whether
// the request is authorized by the included macaroons.
func (r *InterceptorChain) macaroonStreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream,
		info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {

		// If noMacaroons are set, we'll always allow the call.
		if r.noMacaroons {
			return handler(srv, ss)
		}

		r.RLock()
		svc := r.svc
		r.RUnlock()

		// If the macaroon service is not yet active, allow the call.
		// THis means that the wallet has not yet been unlocked, and we
		// allow calls to the WalletUnlockerService.
		if svc == nil {
			return handler(srv, ss)
		}

		r.RLock()
		uriPermissions, ok := r.permissionMap[info.FullMethod]
		r.RUnlock()
		if !ok {
			return fmt.Errorf("%s: unknown permissions required "+
				"for method", info.FullMethod)
		}

		// Find out if there is an external validator registered for
		// this method. Fall back to the internal one if there isn't.
		validator, ok := svc.ExternalValidators[info.FullMethod]
		if !ok {
			validator = svc
		}

		// Now that we know what validator to use, let it do its work.
		err := validator.ValidateMacaroon(
			ss.Context(), uriPermissions, info.FullMethod,
		)
		if err != nil {
			return err
		}

		return handler(srv, ss)
	}
}
