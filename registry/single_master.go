package registry

import (
	"encoding/json"
	"fmt"

	"github.com/mailgun/go-etcd/etcd"
	"github.com/mailgun/scroll/vulcan"
)

const (
	frontendKey   = "%s/frontends/%s.%s/frontend"
	middlewareKey = "%s/frontends/%s.%s/middlewares/%s"
	backendKey    = "%s/backends/%s/backend"
	serverKey     = "%s/backends/%s/servers/%s"
	masterNodeID  = "master"
)

/*
SingleMasterRegistry is an implementation of Registry that uses a single master
instance of an application to handle requests. When the master instance fails,
request handling will automatically failover to a slave instance.
*/
type SingleMasterRegistry struct {
	Key      string
	TTL      uint64
	IsMaster bool
	Client   *etcd.Client
}

// NewSingleMasterRegistry creates a new SingleMasterRegistry from the provided etcd Client.
func NewSingleMasterRegistry(key string, ttl uint64) *SingleMasterRegistry {
	client := etcd.NewClient([]string{"http://127.0.0.1:4001"})

	return &SingleMasterRegistry{
		Key:      key,
		TTL:      ttl,
		Client:   client,
		IsMaster: false,
	}
}

// RegisterApp adds a new backend and a single server with Vulcand.
func (s *SingleMasterRegistry) RegisterApp(registration *AppRegistration) error {
	endpoint, err := vulcan.NewEndpointWithID(masterNodeID, registration.Name, registration.Host, registration.Port)
	if err != nil {
		return nil
	}

	err = s.registerBackend(endpoint)
	if err != nil {
		return err
	}

	err = s.registerServer(endpoint)
	if err != nil {
		return err
	}

	return nil
}

func (s *SingleMasterRegistry) registerBackend(endpoint *vulcan.Endpoint) error {
	key := fmt.Sprintf(backendKey, s.Key, endpoint.Name)
	backend, err := endpoint.BackendSpec()
	if err != nil {
		return err
	}

	_, err = s.Client.Set(key, backend, 0)
	if err != nil {
		return err
	}

	return err
}

func (s *SingleMasterRegistry) registerServer(endpoint *vulcan.Endpoint) error {
	if s.IsMaster {
		return s.maintainMasterRole(endpoint)
	}

	return s.assumeMasterRole(endpoint)
}

func (s *SingleMasterRegistry) assumeMasterRole(endpoint *vulcan.Endpoint) error {
	key := fmt.Sprintf(serverKey, s.Key, endpoint.Name, endpoint.ID)
	server, err := endpoint.ServerSpec()
	if err != nil {
		return nil
	}

	_, err = s.Client.Create(key, server, s.TTL)
	if err != nil {
		return err
	}

	s.IsMaster = true

	return nil
}

func (s *SingleMasterRegistry) maintainMasterRole(endpoint *vulcan.Endpoint) error {
	key := fmt.Sprintf(serverKey, s.Key, endpoint.Name, endpoint.ID)
	server, err := endpoint.ServerSpec()
	if err != nil {
		return nil
	}

	_, err = s.Client.CompareAndSwap(key, server, s.TTL, server, 0)
	if err != nil {
		s.IsMaster = false
		return err
	}

	s.IsMaster = true
	return nil
}

// RegisterHandler registers the frontends and middlewares with Vulcand.
func (s *SingleMasterRegistry) RegisterHandler(registration *HandlerRegistration) error {
	location := vulcan.NewLocation(registration.Host, registration.Methods, registration.Path, registration.Name, registration.Middlewares)
	err := s.registerFrontend(location)
	if err != nil {
		return err
	}

	err = s.registerMiddleware(location)
	if err != nil {
		return err
	}

	return nil
}

func (s *SingleMasterRegistry) registerFrontend(location *vulcan.Location) error {
	key := fmt.Sprintf(frontendKey, s.Key, location.Host, location.ID)
	frontend, err := location.Spec()
	if err != nil {
		return err
	}

	_, err = s.Client.Set(key, frontend, 0)
	if err != nil {
		return err
	}

	return nil
}

func (s *SingleMasterRegistry) registerMiddleware(location *vulcan.Location) error {
	for i, m := range location.Middlewares {
		m.Priority = i

		key := fmt.Sprintf(middlewareKey, s.Key, location.Host, location.ID, m.ID)
		middleware, err := json.Marshal(m)
		if err != nil {
			return err
		}

		_, err = s.Client.Set(key, string(middleware), 0)
		if err != nil {
			return err
		}
	}

	return nil
}
