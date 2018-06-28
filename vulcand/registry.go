package vulcand

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	etcd "github.com/coreos/etcd/clientv3"
	"github.com/mailgun/log"
	"github.com/pkg/errors"
)

const (
	localEtcdProxy = "127.0.0.1:2379"
	frontendFmt    = "%s/frontends/%s.%s/frontend"
	middlewareFmt  = "%s/frontends/%s.%s/middlewares/%s"
	backendFmt     = "%s/backends/%s/backend"
	serverFmt      = "%s/backends/%s/servers/%s"

	defaultRegistrationTTL = 30 * time.Second
)

type Config struct {
	Etcd   *etcd.Config
	Chroot string
	TTL    time.Duration
}

type Registry struct {
	cfg           Config
	client        *etcd.Client
	backendSpec   *backendSpec
	frontendSpecs []*frontendSpec
	ctx           context.Context
	cancelFunc    context.CancelFunc
	wg            sync.WaitGroup
	leaseID       etcd.LeaseID
}

func NewRegistry(cfg Config, appName, ip string, port int) (*Registry, error) {
	backendSpec, err := newBackendSpec(appName, ip, port)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create backend")
	}

	if cfg.TTL <= 0 {
		cfg.TTL = defaultRegistrationTTL
	}

	if cfg.Etcd == nil {
		cfg.Etcd = &etcd.Config{Endpoints: []string{localEtcdProxy}}
	}

	client, err := etcd.New(*cfg.Etcd)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create Etcd client, cfg=%v", *cfg.Etcd)
	}
	ctx, cancelFunc := context.WithCancel(context.Background())

	// Grant a new lease for this client instance
	resp, err := client.Grant(ctx, int64(cfg.TTL.Seconds()))
	if err != nil {
		return nil, errors.Wrapf(err, "failed to grant a new lease, cfg=%v", *cfg.Etcd)
	}

	// Keep the lease alive for as long as we live
	_, err = client.KeepAlive(ctx, resp.ID)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to start keep alive, cfg=%v", *cfg.Etcd)
	}

	c := Registry{
		cfg:         cfg,
		backendSpec: backendSpec,
		client:      client,
		leaseID:     resp.ID,
		ctx:         ctx,
		cancelFunc:  cancelFunc,
	}
	return &c, nil
}

func (r *Registry) AddFrontend(host, path string, methods []string, middlewares []Middleware) {
	r.frontendSpecs = append(r.frontendSpecs, newFrontendSpec(r.backendSpec.AppName, host, path, methods, middlewares))
}

func (r *Registry) Start() error {
	if err := r.registerBackend(r.backendSpec); err != nil {
		return errors.Wrapf(err, "failed to register backend, %s", r.backendSpec.ID)
	}

	// Write our backend spec config
	key := fmt.Sprintf(serverFmt, r.cfg.Chroot, r.backendSpec.AppName, r.backendSpec.ID)
	_, err := r.client.Put(r.ctx, key, r.backendSpec.serverSpec(), etcd.WithLease(r.leaseID))
	if err != nil {
		return errors.Wrap(err, "failed to write backend spec")
	}

	go func() {
		for {
			select {
			case <-r.ctx.Done():
				_, err := r.client.Revoke(context.Background(), r.leaseID)
				log.Infof("lease revoked err=(%v)", err)
				return
			}
		}
	}()

	for _, fes := range r.frontendSpecs {
		if err := r.registerFrontend(fes); err != nil {
			r.cancelFunc()
			return errors.Wrapf(err, "failed to register frontend, %s", fes.ID)
		}
	}
	return nil
}

func (r *Registry) Stop() {
	r.cancelFunc()
	r.wg.Wait()
}

func (r *Registry) registerBackend(bes *backendSpec) error {
	betKey := fmt.Sprintf(backendFmt, r.cfg.Chroot, bes.AppName)
	betVal := bes.typeSpec()
	_, err := r.client.Put(r.ctx, betKey, betVal)
	if err != nil {
		return errors.Wrapf(err, "failed to set backend type, %s", betKey)
	}
	besKey := fmt.Sprintf(serverFmt, r.cfg.Chroot, bes.AppName, bes.ID)
	besVar := bes.serverSpec()
	_, err = r.client.Put(r.ctx, besKey, besVar, etcd.WithLease(r.leaseID))
	return errors.Wrapf(err, "failed to set backend spec, %s", besKey)
}

func (r *Registry) registerFrontend(fes *frontendSpec) error {
	fesKey := fmt.Sprintf(frontendFmt, r.cfg.Chroot, fes.Host, fes.ID)
	fesVal := fes.spec()
	_, err := r.client.Put(r.ctx, fesKey, fesVal, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to set frontend spec, %s", fesKey)
	}
	for i, mw := range fes.Middlewares {
		mw.Priority = i
		mwKey := fmt.Sprintf(middlewareFmt, r.cfg.Chroot, fes.Host, fes.ID, mw.ID)
		mwVal, err := json.Marshal(mw)
		if err != nil {
			return errors.Wrapf(err, "failed to JSON middleware, %v", mw)
		}
		_, err = r.client.Put(r.ctx, mwKey, string(mwVal))
		if err != nil {
			return errors.Wrapf(err, "failed to set middleware, %s", mwKey)
		}
	}
	return nil
}
