// Copyright 2016-2017 Authors of Cilium
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package k8s abstracts all Kubernetes specific behaviour
package k8s

import (
	goerrors "errors"
	"fmt"
	"time"

	"github.com/cilium/cilium/api/v1/models"
	"github.com/cilium/cilium/pkg/logging/logfields"

	go_version "github.com/hashicorp/go-version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	// ErrNilNode is returned when the Kubernetes API server has returned a nil node
	ErrNilNode = goerrors.New("API server returned nil node")

	// k8sCli is the default client.
	k8sCli = &K8sClient{}
)

// CreateConfig creates a rest.Config for a given endpoint using a kubeconfig file.
func createConfig(endpoint, kubeCfgPath string) (*rest.Config, error) {
	// If the endpoint and the kubeCfgPath are empty then we can try getting
	// the rest.Config from the InClusterConfig
	if endpoint == "" && kubeCfgPath == "" {
		return rest.InClusterConfig()
	}

	if kubeCfgPath != "" {
		return clientcmd.BuildConfigFromFlags("", kubeCfgPath)
	}

	config := &rest.Config{Host: endpoint}
	err := rest.SetKubernetesDefaults(config)

	return config, err
}

// CreateConfigFromAgentResponse creates a client configuration from a
// models.DaemonConfigurationResponse
func CreateConfigFromAgentResponse(resp *models.DaemonConfiguration) (*rest.Config, error) {
	return createConfig(resp.Status.K8sEndpoint, resp.Status.K8sConfiguration)
}

// CreateConfig creates a client configuration based on the configured API
// server and Kubeconfig path
func CreateConfig() (*rest.Config, error) {
	return createConfig(GetAPIServer(), GetKubeconfigPath())
}

// CreateClient creates a new client to access the Kubernetes API
func CreateClient(config *rest.Config) (*kubernetes.Clientset, error) {
	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	stop := make(chan struct{})
	timeout := time.NewTimer(time.Minute)
	defer timeout.Stop()
	wait.Until(func() {
		// FIXME: Use config.String() when we rebase to latest go-client
		log.WithField("host", config.Host).Info("Establishing connection to apiserver")
		err = isConnReady(cs)
		if err == nil {
			close(stop)
			return
		}
		select {
		case <-timeout.C:
			log.WithError(err).WithField(logfields.IPAddr, config.Host).Error("Unable to contact k8s api-server")
			close(stop)
		default:
		}
	}, 5*time.Second, stop)
	if err == nil {
		log.Info("Connected to apiserver")
	}
	return cs, err
}

// GetServerVersion returns the kubernetes api-server version.
func GetServerVersion() (ver *go_version.Version, err error) {
	sv, err := Client().Discovery().ServerVersion()
	if err != nil {
		return nil, err
	}

	// Try GitVersion first. In case of error fallback to MajorMinor
	if sv.GitVersion != "" {
		// This is a string like "v1.9.0"
		ver, err = go_version.NewVersion(sv.GitVersion)
		if err == nil {
			return ver, err
		}
	}

	if sv.Major != "" && sv.Minor != "" {
		ver, err = go_version.NewVersion(fmt.Sprintf("%s.%s", sv.Major, sv.Minor))
		if err == nil {
			return ver, nil
		}
	}

	if err != nil {
		return nil, fmt.Errorf("cannot parse k8s server version from %+v: %s", sv, err)
	}
	return nil, fmt.Errorf("cannot parse k8s server version from %+v", sv)
}

// isConnReady returns the err for the controller-manager status
func isConnReady(c *kubernetes.Clientset) error {
	_, err := c.CoreV1().ComponentStatuses().Get("controller-manager", metav1.GetOptions{})
	return err
}

// Client returns the default Kubernetes client.
func Client() *K8sClient {
	return k8sCli
}

func createDefaultClient() error {
	restConfig, err := CreateConfig()
	if err != nil {
		return fmt.Errorf("unable to create k8s client rest configuration: %s", err)
	}

	createdK8sClient, err := CreateClient(restConfig)
	if err != nil {
		return fmt.Errorf("unable to create k8s client: %s", err)
	}

	k8sCli.Interface = createdK8sClient

	return nil
}
