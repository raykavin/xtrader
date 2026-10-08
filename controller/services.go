package controller

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/tools/log"
)

// healthTimeout is how long a request to the health of a service may take.
var healthTimeout = 2 * time.Second

// StartServices launches the services of the host the agents need, eg: the
// bridge of MT5, from dir, but the ones up already, and waits for the health
// of each one: a service not up after its wait only warns, as the agents
// retry. It returns the names of the ones launched, to stop after the agents,
// each with its stop timeout. One a controller before launched and left
// running as it died isn't up already: the controller stops it before (see
// Supervisor.StopLeftovers).
func StartServices(ctx context.Context, sup *Supervisor, services []config.Service, dir string) ([]string, error) {
	var launched []string
	for _, service := range services {
		if service.Health != "" && healthy(ctx, service.Health) {
			log.Warnf("service %s answers at %s already: it isn't launched, nor stopped with the controller",
				service.Name, service.Health)
			continue
		}
		command := strings.Fields(service.Exec)
		for _, arg := range service.Args {
			command = append(command, strings.Fields(arg)...)
		}
		spec := ProcessSpec{Name: service.Name, Command: command, Dir: dir, Env: service.Env.Map(),
			Restart: service.Restart, StopTimeout: service.StopTimeout}
		if err := sup.Start(spec, nil); err != nil {
			for _, name := range launched {
				sup.Stop(name)
			}
			return nil, fmt.Errorf("service %w", err)
		}
		launched = append(launched, service.Name)
		if service.Health == "" {
			continue
		}
		log.Infof("[SETUP] service %s: waiting for %s (%s at most)", service.Name, service.Health, service.Wait)
		if err := waitHealthy(ctx, service.Health, service.Wait, func() bool { return sup.Running(service.Name) }); err != nil {
			log.Warnf("service %s: %v, the agents are launched anyway", service.Name, err)
			continue
		}
		log.Infof("[SETUP] service %s up", service.Name)
	}
	return launched, nil
}

// healthy tells whether url answers, whatever its status.
func healthy(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return true
}

// waitHealthy waits for url to answer, for timeout at most, or until the
// service stops running.
func waitHealthy(ctx context.Context, url string, timeout time.Duration, running func() bool) error {
	expired := time.After(timeout)
	for {
		if healthy(ctx, url) {
			return nil
		}
		if !running() {
			return fmt.Errorf("exited before %s answered", url)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-expired:
			return fmt.Errorf("%s not answering after %s", url, timeout)
		case <-time.After(time.Second):
		}
	}
}
