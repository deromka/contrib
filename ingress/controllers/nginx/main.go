/*
Copyright 2015 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"time"

	"github.com/golang/glog"
	"github.com/spf13/pflag"

	"k8s.io/contrib/ingress/controllers/nginx/nginx"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/healthz"
	"k8s.io/kubernetes/pkg/runtime"
)

const (
	healthPort = 10249
)

var (
	flags = pflag.NewFlagSet("", pflag.ExitOnError)

	defaultSvc = flags.String("default-backend-service", "",
		`Service used to serve a 404 page for the default backend. Takes the form
    namespace/name. The controller uses the first node port of this Service for
    the default backend.`)

	nxgConfigMap = flags.String("nginx-configmap", "",
		`Name of the ConfigMap that containes the custom nginx configuration to use`)

	tcpConfigMapName = flags.String("tcp-services-configmap", "",
		`Name of the ConfigMap that containes the definition of the TCP services to expose.
		The key in the map indicates the external port to be used. The value is the name of the 
		service with the format namespace/serviceName and the port of the service could be a number of the
		name of the port.
		The ports 80 and 443 are not allowed as external ports. This ports are reserved for nginx`)

	resyncPeriod = flags.Duration("sync-period", 30*time.Second,
		`Relist and confirm cloud resources this often.`)

	watchNamespace = flags.String("watch-namespace", api.NamespaceAll,
		`Namespace to watch for Ingress. Default is to watch all namespaces`)

	healthzPort = flags.Int("healthz-port", healthPort, "port for healthz endpoint.")

	buildCfg = flags.Bool("dump-nginx—configuration", false, `Returns a ConfigMap with the default nginx conguration.
		This can be used as a guide to create a custom configuration.`)

	profiling = flags.Bool("profiling", true, `Enable profiling via web interface host:port/debug/pprof/`)
)

func main() {
	flags.AddGoFlagSet(flag.CommandLine)
	flags.Parse(os.Args)

	if *buildCfg {
		fmt.Printf("Example of ConfigMap to customize NGINX configuration:\n%v", nginx.ConfigMapAsString())
		os.Exit(0)
	}

	if *defaultSvc == "" {
		glog.Fatalf("Please specify --default-backend")
	}

	kubeClient, err := unversioned.NewInCluster()
	if err != nil {
		glog.Fatalf("failed to create client: %v", err)
	}

	lbInfo, err := getLBDetails(kubeClient)
	if err != nil {
		glog.Fatalf("unexpected error getting runtime information: %v", err)
	}

	err = isValidService(kubeClient, *defaultSvc)
	if err != nil {
		glog.Fatalf("no service with name %v found: %v", *defaultSvc, err)
	}

	lbc, err := newLoadBalancerController(kubeClient, *resyncPeriod, *defaultSvc, *watchNamespace, *nxgConfigMap, *tcpConfigMapName, lbInfo)
	if err != nil {
		glog.Fatalf("%v", err)
	}

	go registerHandlers(lbc)

	lbc.Run()

	for {
		glog.Infof("Handled quit, awaiting pod deletion")
		time.Sleep(30 * time.Second)
	}
}

// lbInfo contains runtime information about the pod
type lbInfo struct {
	ObjectName   string
	DeployType   runtime.Object
	Podname      string
	PodIP        string
	PodNamespace string
}

func registerHandlers(lbc *loadBalancerController) {
	mux := http.NewServeMux()
	healthz.InstallHandler(mux, lbc.nginx)

	http.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		lbc.Stop()
	})

	if *profiling {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	}

	server := &http.Server{
		Addr:    fmt.Sprintf(":%v", *healthzPort),
		Handler: mux,
	}
	glog.Fatal(server.ListenAndServe())
}
